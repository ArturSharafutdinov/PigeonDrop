"""The HTTP service: a WAV in, the text out, one recording at a time. The protocol is in stt/README.md.

Only the standard library serves HTTP. The service sits on the server's internal network, takes one request type
and runs one model; a web framework would add memory and dependencies and no feature that is used here."""
from __future__ import annotations

import hmac
import json
import logging
import os
import re
import signal
import socket
import sys
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Callable, Optional

from . import config as configuration
from .engine import Engine, ParakeetEngine
from .wavio import BadAudio, TooLong, read_wav

LOGGER = logging.getLogger("stt")
# A library-style logger stays silent until the program (main) or a test attaches a handler.
LOGGER.addHandler(logging.NullHandler())

# The content types a client may use for a WAV.
WAV_TYPES = ("audio/wav", "audio/x-wav", "audio/wave", "audio/vnd.wave")
REQUEST_ID = re.compile(r"[A-Za-z0-9._-]{1,64}")
BUSY_RETRY_AFTER_S = 5
# A client that stops sending is dropped after this long, so a stuck connection does not hold a thread forever.
SOCKET_TIMEOUT_S = 30
# How many connections are served at the same time, each by a thread of its own. The model takes one recording at
# a time and the only client is the Go server, so this is generous; it keeps anyone who can reach the port from
# starting threads without end by opening connections and sending nothing.
MAX_CONNECTIONS = 8
# A request that is refused before its body is read still gets its answer: the connection is closed only after the
# client has had this long, and up to the size limit, to finish sending (see Handler._refuse_unread).
LINGER_S = 2.0


class JsonFormatter(logging.Formatter):
    """One JSON object per line, like the Go server's log. Fields come from `log(...)`; the text of a recording and
    the audio are never logged."""

    def format(self, record: logging.LogRecord) -> str:
        entry = {"time": self.formatTime(record, "%Y-%m-%dT%H:%M:%S%z"), "level": record.levelname.lower(),
                 "msg": record.getMessage()}
        entry.update(getattr(record, "fields", {}))
        if record.exc_info:
            entry["error"] = f"{record.exc_info[0].__name__}: {record.exc_info[1]}"
        return json.dumps(entry, ensure_ascii=False)


def log(level: int, message: str, **fields) -> None:
    LOGGER.log(level, message, extra={"fields": fields})


def configure_logging(level: str) -> None:
    handler = logging.StreamHandler(sys.stderr)
    handler.setFormatter(JsonFormatter())
    LOGGER.handlers[:] = [handler]
    LOGGER.setLevel(level.upper())
    LOGGER.propagate = False


class Service:
    """The state the handlers share: the settings, the model once it is loaded, and the lock that lets one
    recording at a time through the model."""

    def __init__(self, config: configuration.Config, engine_factory: Callable[[], Engine]):
        self.config = config
        self._engine_factory = engine_factory
        self.engine: Optional[Engine] = None
        self.load_error: Optional[BaseException] = None
        self.ready = threading.Event()
        self._busy = threading.Lock()

    def load(self) -> None:
        """Loads the model; meant to run in a thread so that the health check can answer "loading" meanwhile."""
        started = time.perf_counter()
        try:
            self.engine = self._engine_factory()
        except BaseException as error:  # noqa: BLE001 (whatever went wrong, the service must say so and stop)
            self.load_error = error
            log(logging.ERROR, "the model could not be loaded", error=f"{type(error).__name__}: {error}")
            return
        self.ready.set()
        log(logging.INFO, "the model is loaded", engine=self.engine.name, seconds=round(time.perf_counter() - started, 2))

    def try_acquire(self) -> bool:
        return self._busy.acquire(blocking=False)

    def release(self) -> None:
        self._busy.release()


def make_handler(service: Service):
    class Handler(BaseHTTPRequestHandler):
        server_version = "pigeondrop-stt"
        # HTTP/1.0: the connection closes after the answer, which keeps the protocol and the client's accounting
        # simple (one request, one connection).
        timeout = SOCKET_TIMEOUT_S

        def log_message(self, format, *args):  # noqa: A002 (the base class's signature)
            pass  # requests are logged once, with their outcome, in `_answer`

        # --- answers

        def _start(self) -> None:
            supplied = self.headers.get("X-Request-Id", "")
            self.request_id = supplied if REQUEST_ID.fullmatch(supplied) else uuid.uuid4().hex
            self.started = time.perf_counter()

        def _answer(self, status: int, payload: dict, headers: Optional[dict] = None, **fields) -> None:
            body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
            # Logged before the answer goes out, so that the line exists by the time the client has the answer: a
            # client (or a test) that reads the log right after its response must find the request in it.
            log(logging.INFO if status < 500 else logging.ERROR, "request", request_id=self.request_id, method=self.command,
                path=self.path, status=status, ms=round((time.perf_counter() - self.started) * 1000), **fields)
            self.send_response(status)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.send_header("X-Request-Id", self.request_id)
            for name, value in (headers or {}).items():
                self.send_header(name, value)
            self.end_headers()
            self.wfile.write(body)

        def _error(self, status: int, code: str, message: str, headers: Optional[dict] = None, **fields) -> None:
            self._answer(status, {"code": code, "message": message}, headers, **fields)

        def _refuse_unread(self, status: int, code: str, message: str, **fields) -> None:
            """Refuses a request without reading its body, which nobody who has not been let in should be able to make
            the service do, and still lets the client see why. A server that answers and closes while the client is
            still sending makes the client see a reset connection instead of the answer, so after the answer is
            sent the sending side is closed, and what the client goes on sending is read and thrown away, for a
            short time and up to the size limit. Nothing of it is kept."""
            self._error(status, code, message, **fields)
            try:
                self.wfile.flush()
                self.connection.shutdown(socket.SHUT_WR)
                deadline = time.monotonic() + LINGER_S
                remaining = service.config.max_body_bytes
                while remaining > 0 and time.monotonic() < deadline:
                    self.connection.settimeout(max(deadline - time.monotonic(), 0.001))
                    chunk = self.connection.recv(min(65536, remaining))
                    if not chunk:
                        break
                    remaining -= len(chunk)
            except OSError:  # the client went away, or did not finish in time: either way it is done
                pass

        # --- routes

        def do_GET(self) -> None:
            self._start()
            if self.path != "/healthz":
                return self._error(404, "not_found", "no such route")
            if service.ready.is_set():
                return self._answer(200, {"ok": True, "engine": service.engine.name})
            self._answer(503, {"ok": False, "code": "loading"})

        def do_POST(self) -> None:
            self._start()
            if self.path != "/v1/transcribe":
                return self._error(404, "not_found", "no such route")
            config = service.config

            # The token comes first, before a byte of the body is read or any size is looked at: a request from
            # somebody who has not been let in costs the service nothing but the answer.
            if not config.insecure:
                supplied = self.headers.get("Authorization", "")
                expected = "Bearer " + config.token
                if not hmac.compare_digest(supplied.encode("utf-8"), expected.encode("utf-8")):
                    return self._refuse_unread(401, "unauthorized", "missing or wrong token")

            # Then the size, from what the request declares. The body is read in full once its size is acceptable,
            # also when the request is going to be refused for what is in it (that is the client's own mistake,
            # and it is told).
            declared = self.headers.get("Content-Length", "")
            if not (declared.isascii() and declared.isdigit()):
                return self._refuse_unread(411, "length_required", "Content-Length is required")
            length = int(declared)
            if length > config.max_body_bytes:
                return self._refuse_unread(413, "too_long", f"the body may be at most {config.max_body_bytes} bytes")
            body = self.rfile.read(length)
            if len(body) != length:
                return self._error(400, "bad_audio", "the body ended before its declared length")

            if self.headers.get("Content-Type", "").split(";")[0].strip().lower() not in WAV_TYPES:
                return self._error(415, "unsupported_media_type", "send the recording as audio/wav")
            if not service.ready.is_set():
                return self._error(503, "loading", "the model is still loading", {"Retry-After": "10"})

            try:
                audio = read_wav(body, config.max_audio_seconds)
            except TooLong as error:
                return self._error(413, "too_long", str(error))
            except BadAudio as error:
                return self._error(400, "bad_audio", str(error))

            if not service.try_acquire():
                return self._error(429, "busy", "another recording is being recognized",
                                   {"Retry-After": str(BUSY_RETRY_AFTER_S)}, audio_ms=audio.milliseconds)
            failure = None
            try:
                began = time.perf_counter()
                transcript = service.engine.transcribe(audio.pcm)
                stt_ms = round((time.perf_counter() - began) * 1000)
            except Exception as error:  # noqa: BLE001 (any failure of the model is this request's 500, never the service's end)
                failure = error
            finally:
                # Before anything is written, whatever the outcome: a client that has the answer may ask again at
                # once, and must not be told "busy" by the request it has just heard back from.
                service.release()
            if failure is not None:
                log(logging.ERROR, "recognition failed", request_id=self.request_id, error=f"{type(failure).__name__}: {failure}")
                return self._error(500, "internal", "recognition failed", audio_ms=audio.milliseconds)
            self._answer(200, {
                "text": transcript.text, "language": transcript.language, "engine": service.engine.name,
                "audio_ms": audio.milliseconds, "stt_ms": stt_ms,
            }, audio_ms=audio.milliseconds, stt_ms=stt_ms, chars=len(transcript.text))

        def _not_allowed(self) -> None:
            self._start()
            self._error(405, "method_not_allowed", "use GET /healthz or POST /v1/transcribe", {"Allow": "GET, POST"})

        do_PUT = do_DELETE = do_PATCH = do_HEAD = do_OPTIONS = _not_allowed

    return Handler


class Server(ThreadingHTTPServer):
    # A request in progress is finished when the service is stopped (docker stop): the client would otherwise see a
    # dropped connection, and the Go server would count that as a failure of the recording.
    daemon_threads = False

    def __init__(self, address, handler, max_connections: int = MAX_CONNECTIONS):
        super().__init__(address, handler)
        self.max_connections = max_connections
        self._slots = threading.BoundedSemaphore(max_connections)
        self._last_warning = float("-inf")

    def process_request(self, request, client_address) -> None:
        # One thread per connection, but no more than there are slots; the next connection is told to come back
        # later and closed, without a thread, so the number of threads has a ceiling.
        if not self._slots.acquire(blocking=False):
            self._turn_away(request, client_address)
            return
        try:
            super().process_request(request, client_address)
        except BaseException:  # the thread could not be started
            self._slots.release()
            raise

    def process_request_thread(self, request, client_address) -> None:
        try:
            super().process_request_thread(request, client_address)
        finally:
            self._slots.release()

    def handle_error(self, request, client_address) -> None:
        """What the base class does is print a traceback on stderr, outside the JSON log. A client that went away
        while it was being answered is no error of the service's; anything else is logged, as one line."""
        error = sys.exc_info()[1]
        if isinstance(error, (ConnectionError, socket.timeout)):
            log(logging.DEBUG, "the client went away", client=client_address[0], error=f"{type(error).__name__}: {error}")
        else:
            log(logging.ERROR, "a request failed unexpectedly", client=client_address[0],
                error=f"{type(error).__name__}: {error}")

    def _turn_away(self, request, client_address) -> None:
        # Said once in a while, not for every connection: a flood of them must not become a flood of log lines.
        # Logged before the answer goes out, like the requests' lines, so it exists by the time the client has it.
        if time.monotonic() - self._last_warning >= 10:
            self._last_warning = time.monotonic()
            log(logging.WARNING, "connections are being refused: the service is already serving as many as it takes at once",
                client=client_address[0], limit=self.max_connections)
        body = json.dumps({"code": "busy", "message": "too many connections"}).encode("utf-8")
        head = (f"HTTP/1.0 503 Service Unavailable\r\nContent-Type: application/json; charset=utf-8\r\n"
                f"Content-Length: {len(body)}\r\nRetry-After: {BUSY_RETRY_AFTER_S}\r\n\r\n").encode("ascii")
        try:
            request.settimeout(1.0)  # the answer is a few hundred bytes; a client that cannot take them is let go
            request.sendall(head + body)
        except OSError:
            pass
        self.shutdown_request(request)


def make_server(service: Service, host: str, port: int, max_connections: int = MAX_CONNECTIONS) -> Server:
    return Server((host, port), make_handler(service), max_connections)


def main() -> int:
    try:
        config = configuration.load(os.environ)
    except configuration.ConfigError as error:
        print(f"stt: configuration:\n{error}", file=sys.stderr)
        return 2
    configure_logging(config.log_level)
    if config.insecure:
        log(logging.WARNING, "running without a token (STT_INSECURE): anyone who reaches the port may use the model")

    service = Service(config, lambda: ParakeetEngine(config.threads))
    server = make_server(service, config.host, config.port)
    loader = threading.Thread(target=service.load, name="loader")
    loader.start()

    def stop(*_args) -> None:
        threading.Thread(target=server.shutdown, name="stopper").start()

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)

    def watch_loader() -> None:
        loader.join()
        if service.load_error is not None:
            server.shutdown()  # a service without a model is no use: exit and let Docker start it again

    threading.Thread(target=watch_loader, name="watcher", daemon=True).start()
    log(logging.INFO, "listening", host=config.host, port=config.port, max_audio_seconds=config.max_audio_seconds)
    server.serve_forever()
    server.server_close()
    return 1 if service.load_error is not None else 0


if __name__ == "__main__":
    sys.exit(main())
