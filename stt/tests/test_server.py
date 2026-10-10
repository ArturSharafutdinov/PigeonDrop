"""The service's protocol, end to end: a real server on a free port, a fake model, plain HTTP clients."""
import http.client
import io
import json
import logging
import os
import socket
import threading
import unittest
from unittest import mock

from app import config as configuration
from app import server

from .helpers import FakeEngine, wav

TOKEN = "s3cret-token"


class ServerTest(unittest.TestCase):
    def start(self, engine=None, loaded=True, **env):
        environment = {"STT_TOKEN": TOKEN}
        environment.update(env)
        self.engine = engine or FakeEngine()
        self.service = server.Service(configuration.load(environment), lambda: self.engine)
        if loaded:
            self.service.load()
        httpd = server.make_server(self.service, "127.0.0.1", 0)
        thread = threading.Thread(target=httpd.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(self.stop, httpd, thread)
        self.port = httpd.server_address[1]

    @staticmethod
    def stop(httpd, thread):
        httpd.shutdown()
        httpd.server_close()
        thread.join(5)

    def request(self, method, path, body=None, headers=None):
        connection = http.client.HTTPConnection("127.0.0.1", self.port, timeout=10)
        try:
            connection.request(method, path, body=body, headers=headers or {})
            response = connection.getresponse()
            data = response.read()
            return response.status, response, json.loads(data) if data else None
        finally:
            connection.close()

    def transcribe(self, body=None, token=TOKEN, content_type="audio/wav", headers=None):
        sent = {"Content-Type": content_type}
        if token is not None:
            sent["Authorization"] = "Bearer " + token
        sent.update(headers or {})
        return self.request("POST", "/v1/transcribe", wav(1.0) if body is None else body, sent)

    def raw(self, data, shutdown_write=False):
        """Sends bytes as they are and returns (status, body) of whatever comes back."""
        with socket.create_connection(("127.0.0.1", self.port), timeout=10) as s:
            s.sendall(data)
            if shutdown_write:
                s.shutdown(socket.SHUT_WR)
            chunks = []
            while True:
                piece = s.recv(65536)
                if not piece:
                    break
                chunks.append(piece)
        answer = b"".join(chunks)
        head, _, body = answer.partition(b"\r\n\r\n")
        return int(head.split(b" ", 2)[1]), json.loads(body) if body else None


class HealthTest(ServerTest):
    def test_ready_after_the_model_is_loaded(self):
        self.start()
        status, _, body = self.request("GET", "/healthz")
        self.assertEqual((status, body), (200, {"ok": True, "engine": "fake-1"}))

    def test_loading_until_the_model_is_there(self):
        self.start(loaded=False)
        status, _, body = self.request("GET", "/healthz")
        self.assertEqual((status, body), (503, {"ok": False, "code": "loading"}))
        self.service.load()
        self.assertEqual(self.request("GET", "/healthz")[0], 200)

    def test_a_model_that_cannot_load_never_becomes_ready(self):
        def broken():
            raise RuntimeError("no model file")

        service = server.Service(configuration.load({"STT_TOKEN": TOKEN}), broken)
        with self.assertLogs("stt", level="ERROR") as logs:
            service.load()
        self.assertFalse(service.ready.is_set())
        self.assertIsInstance(service.load_error, RuntimeError)
        self.assertIn("no model file", logs.records[0].fields["error"])

    def test_unknown_routes_and_methods(self):
        self.start()
        self.assertEqual(self.request("GET", "/")[0], 404)
        self.assertEqual(self.request("POST", "/v1/other", b"x", {"Content-Length": "1"})[0], 404)
        status, response, body = self.request("PUT", "/v1/transcribe", b"x")
        self.assertEqual((status, body["code"], response.getheader("Allow")), (405, "method_not_allowed", "GET, POST"))


class TranscribeTest(ServerTest):
    def test_returns_the_text_and_what_made_it(self):
        self.start(FakeEngine(text="позвони Ивану", language="ru"))
        status, response, body = self.transcribe(wav(2.0))
        self.assertEqual(status, 200)
        self.assertEqual(body["text"], "позвони Ивану")
        self.assertEqual(body["language"], "ru")
        self.assertEqual(body["engine"], "fake-1")
        self.assertEqual(body["audio_ms"], 2000)
        self.assertIsInstance(body["stt_ms"], int)
        self.assertTrue(response.getheader("Content-Type").startswith("application/json"))

    def test_the_model_gets_the_samples_and_nothing_else(self):
        self.start()
        self.transcribe(wav(2.0))
        self.assertEqual(len(self.engine.received), 1)
        self.assertEqual(len(self.engine.received[0]), 2 * 16000 * 2)  # no WAV header

    def test_silence_is_a_normal_answer_with_empty_text(self):
        self.start(FakeEngine(text=""))
        status, _, body = self.transcribe()
        self.assertEqual((status, body["text"]), (200, ""))

    def test_language_is_null_when_the_model_does_not_say(self):
        self.start()
        self.assertIsNone(self.transcribe()[2]["language"])

    def test_content_type_may_carry_parameters_and_have_other_spellings(self):
        self.start()
        for content_type in ("audio/wav", "audio/x-wav", "audio/wav; charset=binary", "Audio/WAV"):
            with self.subTest(content_type):
                self.assertEqual(self.transcribe(content_type=content_type)[0], 200)

    def test_another_content_type_is_refused(self):
        self.start()
        status, _, body = self.transcribe(content_type="audio/ogg")
        self.assertEqual((status, body["code"]), (415, "unsupported_media_type"))
        self.assertEqual(self.engine.received, [])

    def test_request_id_is_echoed_or_made(self):
        self.start()
        _, response, _ = self.transcribe(headers={"X-Request-Id": "job-42.a"})
        self.assertEqual(response.getheader("X-Request-Id"), "job-42.a")
        _, response, _ = self.transcribe(headers={"X-Request-Id": "bad id with spaces"})
        made = response.getheader("X-Request-Id")
        self.assertRegex(made, r"^[0-9a-f]{32}$")
        _, response, _ = self.request("GET", "/healthz")
        self.assertTrue(response.getheader("X-Request-Id"))


class AuthTest(ServerTest):
    def test_the_right_token_passes_and_others_do_not(self):
        self.start()
        self.assertEqual(self.transcribe()[0], 200)
        for label, token in (("none", None), ("wrong", "nope"), ("same length", "x" * len(TOKEN)), ("empty", ""),
                             ("prefix", TOKEN[:-1]), ("longer", TOKEN + "x")):
            with self.subTest(label):
                status, _, body = self.transcribe(token=token)
                self.assertEqual((status, body["code"]), (401, "unauthorized"))
        self.assertEqual(len(self.engine.received), 1)  # only the first request reached the model

    def test_the_scheme_must_be_bearer(self):
        self.start()
        status, _, _ = self.transcribe(token=None, headers={"Authorization": TOKEN})
        self.assertEqual(status, 401)
        status, _, _ = self.transcribe(token=None, headers={"Authorization": "Basic " + TOKEN})
        self.assertEqual(status, 401)

    def test_a_refused_request_with_a_big_body_still_gets_its_answer(self):
        # The server reads the body before it refuses, so the client sees 401 and not a reset connection.
        self.start()
        status, _, body = self.transcribe(wav(30.0), token="wrong")
        self.assertEqual((status, body["code"]), (401, "unauthorized"))

    def test_without_a_token_only_insecure_mode_starts(self):
        with self.assertRaises(configuration.ConfigError):
            configuration.load({})
        self.start(STT_TOKEN="", STT_INSECURE="true")
        self.assertEqual(self.transcribe(token=None)[0], 200)

    def test_a_token_is_never_echoed_or_logged(self):
        self.start()
        with self.assertLogs("stt", level="DEBUG") as logs:
            status, response, body = self.transcribe(token="wrong-" + TOKEN)
        self.assertNotIn(TOKEN, json.dumps(body) + str(response.headers) + "".join(logs.output))


class BadInputTest(ServerTest):
    def test_audio_that_is_not_the_expected_wav(self):
        self.start()
        cases = {
            "not a wav": b"this is not audio",
            "empty body": b"",
            "stereo": wav(1.0, channels=2),
            "8 kHz": wav(1.0, rate=8000),
            "44.1 kHz": wav(1.0, rate=44100),
            "8-bit": wav(1.0, width=1),
            "24-bit": wav(1.0, width=3),
            "no audio": wav(frames=0),
            "cut short": wav(1.0, header_frames=32000),
        }
        for label, body in cases.items():
            with self.subTest(label):
                status, _, answer = self.transcribe(body)
                self.assertEqual((status, answer["code"]), (400, "bad_audio"))
                self.assertTrue(answer["message"])
        self.assertEqual(self.engine.received, [])

    def test_audio_longer_than_the_limit(self):
        self.start(MAX_AUDIO_SECONDS="2")
        self.assertEqual(self.transcribe(wav(2.0))[0], 200)  # exactly the limit is fine
        status, _, body = self.transcribe(wav(2.05))  # fits in the body size, not in the duration
        self.assertEqual((status, body["code"]), (413, "too_long"))
        self.assertIn("2.05 s", body["message"])
        self.assertEqual(len(self.engine.received), 1)

    def test_a_body_over_the_size_limit_is_refused_from_its_declared_length(self):
        self.start(MAX_AUDIO_SECONDS="2")
        request = (b"POST /v1/transcribe HTTP/1.1\r\nHost: x\r\nContent-Type: audio/wav\r\n"
                   b"Authorization: Bearer " + TOKEN.encode() + b"\r\nContent-Length: 99999999\r\n\r\n")
        status, body = self.raw(request)
        self.assertEqual((status, body["code"]), (413, "too_long"))

    def test_content_length_is_required_and_must_be_a_number(self):
        self.start()
        for label, header in (("missing", b""), ("not a number", b"Content-Length: many\r\n"),
                              ("negative", b"Content-Length: -5\r\n")):
            with self.subTest(label):
                request = (b"POST /v1/transcribe HTTP/1.1\r\nHost: x\r\nContent-Type: audio/wav\r\n" + header + b"\r\n")
                status, body = self.raw(request)
                self.assertEqual((status, body["code"]), (411, "length_required"))

    def test_a_body_shorter_than_declared(self):
        self.start()
        request = (b"POST /v1/transcribe HTTP/1.1\r\nHost: x\r\nContent-Type: audio/wav\r\n"
                   b"Content-Length: 5000\r\n\r\n" + b"x" * 100)
        status, body = self.raw(request, shutdown_write=True)
        self.assertEqual((status, body["code"]), (400, "bad_audio"))

    def test_nothing_is_recognized_while_the_model_is_loading(self):
        self.start(loaded=False)
        status, response, body = self.transcribe()
        self.assertEqual((status, body["code"], response.getheader("Retry-After")), (503, "loading", "10"))
        self.assertEqual(self.engine.received, [])


class OneAtATimeTest(ServerTest):
    def test_a_second_recording_meanwhile_gets_busy_and_the_first_is_not_hurt(self):
        gate, entered = threading.Event(), threading.Event()
        self.start(FakeEngine(gate=gate, entered=entered))
        first = {}

        def run():
            first["answer"] = self.transcribe()

        worker = threading.Thread(target=run)
        worker.start()
        self.assertTrue(entered.wait(10))

        status, response, body = self.transcribe()
        self.assertEqual((status, body["code"], response.getheader("Retry-After")), (429, "busy", "5"))

        gate.set()
        worker.join(10)
        self.assertEqual(first["answer"][0], 200)
        self.assertEqual(len(self.engine.received), 1)  # the refused one never reached the model
        self.assertEqual(self.transcribe()[0], 200)  # and the lock is free again

    def test_health_answers_while_the_model_is_busy(self):
        gate, entered = threading.Event(), threading.Event()
        self.start(FakeEngine(gate=gate, entered=entered))
        worker = threading.Thread(target=self.transcribe)
        worker.start()
        self.assertTrue(entered.wait(10))
        self.assertEqual(self.request("GET", "/healthz")[0], 200)
        gate.set()
        worker.join(10)


class ModelFailureTest(ServerTest):
    def test_a_failing_model_is_a_500_for_that_request_only(self):
        self.start(FakeEngine(fail=RuntimeError("out of memory in the runtime")))
        with self.assertLogs("stt", level="ERROR") as logs:
            status, _, body = self.transcribe()
        self.assertEqual((status, body["code"]), (500, "internal"))
        self.assertNotIn("out of memory", json.dumps(body))  # the detail stays in the log
        self.assertTrue(any("out of memory" in getattr(r, "fields", {}).get("error", "") for r in logs.records))
        # The service lives on and the lock was given back.
        self.engine.fail = None
        self.assertEqual(self.transcribe()[0], 200)


class LoggingTest(ServerTest):
    def test_the_log_has_one_json_line_per_request_and_never_the_text_or_audio(self):
        secret = "номер карты 4242 4242 4242 4242"
        self.start(FakeEngine(text=secret))
        stream = io.StringIO()
        handler = logging.StreamHandler(stream)
        handler.setFormatter(server.JsonFormatter())
        logger = logging.getLogger("stt")
        logger.addHandler(handler)
        logger.setLevel(logging.DEBUG)
        self.addCleanup(logger.removeHandler, handler)

        self.transcribe(wav(1.5), headers={"X-Request-Id": "r-1"})
        self.transcribe(token="wrong", headers={"X-Request-Id": "r-2"})

        lines = [json.loads(line) for line in stream.getvalue().splitlines()]
        requests = [line for line in lines if line["msg"] == "request"]
        self.assertEqual([(r["request_id"], r["status"]) for r in requests], [("r-1", 200), ("r-2", 401)])
        self.assertEqual((requests[0]["audio_ms"], requests[0]["chars"]), (1500, len(secret)))
        self.assertNotIn("4242", stream.getvalue())
        self.assertNotIn(TOKEN, stream.getvalue())


class MainTest(unittest.TestCase):
    def test_a_bad_configuration_stops_the_start_and_lists_every_problem(self):
        environment = {"PORT": "99999", "MAX_AUDIO_SECONDS": "soon"}
        stderr = io.StringIO()
        with mock.patch.dict(os.environ, environment, clear=True), mock.patch("sys.stderr", stderr):
            self.assertEqual(server.main(), 2)
        message = stderr.getvalue()
        for expected in ("STT_TOKEN", "PORT", "MAX_AUDIO_SECONDS"):
            self.assertIn(expected, message)


if __name__ == "__main__":
    unittest.main()
