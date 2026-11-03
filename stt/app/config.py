"""Settings of the service, read from environment variables the way the Go server's are (Coolify and Docker pass
them in). Every problem is reported at once, not only the first."""
from __future__ import annotations

import dataclasses
from typing import Mapping, Optional

LOG_LEVELS = ("debug", "info", "warning", "error")

# The shortest token the service starts with (`openssl rand -hex 32` makes 64 characters). The token is the only
# thing between the model and whoever can reach the port, so a one-letter one is a mistake, not a choice.
MIN_TOKEN_LENGTH = 16


class ConfigError(Exception):
    """One or more settings are wrong; the message lists them all."""


@dataclasses.dataclass(frozen=True)
class Config:
    # Shared secret the Go server sends as "Authorization: Bearer ...".
    token: str
    # Allows an empty token. Only for trying the service by hand; never in production.
    insecure: bool
    host: str
    port: int
    # Longest recording the service takes (the watch records at most 60 s). Memory was measured only up to 60 s,
    # so do not raise this without measuring again.
    max_audio_seconds: float
    # Threads of the model's runtime; None means as many as the container may really use (see cpus.available_cpus).
    threads: Optional[int]
    log_level: str

    @property
    def max_body_bytes(self) -> int:
        """Largest request body: the longest recording as 16 kHz 16-bit mono (32 000 bytes a second) plus a WAV
        header with room for extra chunks."""
        return int(self.max_audio_seconds * 32000) + 4096


def _parse_bool(value: str) -> Optional[bool]:
    text = value.strip().lower()
    if text in ("1", "true", "yes", "on"):
        return True
    if text in ("0", "false", "no", "off", ""):
        return False
    return None


def load(environ: Mapping[str, str]) -> Config:
    problems = []

    token = environ.get("STT_TOKEN", "").strip()
    insecure = _parse_bool(environ.get("STT_INSECURE", ""))
    if insecure is None:
        problems.append(f"STT_INSECURE must be true or false, got {environ.get('STT_INSECURE')!r}")
        insecure = False
    if not token and not insecure:
        problems.append("STT_TOKEN is required (STT_INSECURE=true allows running without one, for trying by hand only)")
    elif token and len(token) < MIN_TOKEN_LENGTH and not insecure:  # insecure mode never looks at the token
        problems.append(f"STT_TOKEN must be at least {MIN_TOKEN_LENGTH} characters, got {len(token)} "
                        "(make one with `openssl rand -hex 32`)")

    port = 8000
    raw = environ.get("PORT", "").strip()
    if raw:
        if raw.isdigit() and 1 <= int(raw) <= 65535:
            port = int(raw)
        else:
            problems.append(f"PORT must be a number from 1 to 65535, got {raw!r}")

    max_seconds = 65.0
    raw = environ.get("MAX_AUDIO_SECONDS", "").strip()
    if raw:
        try:
            max_seconds = float(raw)
        except ValueError:
            max_seconds = 0.0
        if not 1.0 <= max_seconds <= 600.0:
            problems.append(f"MAX_AUDIO_SECONDS must be a number of seconds from 1 to 600, got {raw!r}")
            max_seconds = 65.0

    threads: Optional[int] = None
    raw = environ.get("STT_THREADS", "").strip()
    if raw:
        if raw.isdigit() and 1 <= int(raw) <= 64:
            threads = int(raw)
        else:
            problems.append(f"STT_THREADS must be a number from 1 to 64, got {raw!r}")

    level = environ.get("LOG_LEVEL", "").strip().lower() or "info"
    if level not in LOG_LEVELS:
        problems.append(f"LOG_LEVEL must be one of {', '.join(LOG_LEVELS)}, got {level!r}")
        level = "info"

    if problems:
        raise ConfigError("\n".join(problems))
    return Config(
        token=token, insecure=insecure, host=environ.get("HOST", "").strip() or "0.0.0.0", port=port,
        max_audio_seconds=max_seconds, threads=threads, log_level=level,
    )
