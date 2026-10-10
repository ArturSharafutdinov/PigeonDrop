"""Reads the WAV a client sends: 16 kHz, 16-bit, mono PCM, the form the Go server makes from a recording. The header
describes the audio, and anything that does not match is refused, because a model fed another rate or width would
not fail, it would only return nonsense."""
from __future__ import annotations

import dataclasses
import io
import wave

SAMPLE_RATE = 16000
SAMPLE_WIDTH_BYTES = 2
CHANNELS = 1


class BadAudio(Exception):
    """The body is not the WAV the service takes. The message is safe to show to the client."""


class TooLong(Exception):
    """The audio is longer than the service takes."""


@dataclasses.dataclass(frozen=True)
class Audio:
    pcm: bytes  # little-endian signed 16-bit samples
    seconds: float

    @property
    def milliseconds(self) -> int:
        return int(round(self.seconds * 1000))


def read_wav(data: bytes, max_seconds: float) -> Audio:
    try:
        reader = wave.open(io.BytesIO(data), "rb")
    except (wave.Error, EOFError):
        raise BadAudio("the body is not a PCM WAV file") from None
    with reader:
        if reader.getcomptype() != "NONE":
            raise BadAudio("the WAV is compressed; only PCM is accepted")
        if reader.getnchannels() != CHANNELS:
            raise BadAudio(f"the WAV must be mono, it has {reader.getnchannels()} channels")
        if reader.getsampwidth() != SAMPLE_WIDTH_BYTES:
            raise BadAudio(f"the WAV must be 16-bit, it has {reader.getsampwidth() * 8}-bit samples")
        if reader.getframerate() != SAMPLE_RATE:
            raise BadAudio(f"the WAV must be {SAMPLE_RATE} Hz, it is {reader.getframerate()} Hz")
        frames = reader.getnframes()
        if frames == 0:
            raise BadAudio("the WAV holds no audio")
        seconds = frames / SAMPLE_RATE
        if seconds > max_seconds:
            raise TooLong(f"the audio is {seconds:.2f} s, the service takes at most {max_seconds:g} s")
        pcm = reader.readframes(frames)
    if len(pcm) != frames * SAMPLE_WIDTH_BYTES:
        raise BadAudio("the WAV is cut short: its header promises more audio than the body holds")
    return Audio(pcm=pcm, seconds=seconds)
