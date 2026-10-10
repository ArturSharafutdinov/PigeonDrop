"""Test helpers: WAV files of any shape and a model that does what the test tells it to."""
import io
import threading
import wave

from app.engine import Engine, Transcript


def wav(seconds=1.0, rate=16000, channels=1, width=2, frames=None, header_frames=None):
    """A silent PCM WAV. `frames` overrides the length in samples; `header_frames` makes the header promise a
    different number of frames than the body holds (a cut-off file)."""
    count = frames if frames is not None else int(seconds * rate)
    buffer = io.BytesIO()
    with wave.open(buffer, "wb") as w:
        w.setnchannels(channels)
        w.setsampwidth(width)
        w.setframerate(rate)
        w.writeframes(b"\x00" * (count * channels * width))
    data = buffer.getvalue()
    if header_frames is not None:
        # data chunk size is the 4 bytes at offset 40; the RIFF size at offset 4 is left alone, readers use the former
        data = data[:40] + (header_frames * channels * width).to_bytes(4, "little") + data[44:]
    return data


class FakeEngine(Engine):
    """Returns `text`. `gate` holds a call until the test opens it, `entered` tells the test that a call is inside,
    `fail` is raised instead of answering."""

    name = "fake-1"

    def __init__(self, text="привет, мир", language=None, fail=None, gate=None, entered=None):
        self.text, self.language, self.fail, self.gate, self.entered = text, language, fail, gate, entered
        self.received = []
        self._lock = threading.Lock()

    def transcribe(self, pcm16):
        with self._lock:
            self.received.append(pcm16)
        if self.entered is not None:
            self.entered.set()
        if self.gate is not None:
            assert self.gate.wait(10), "the test never opened the gate"
        if self.fail is not None:
            raise self.fail
        return Transcript(text=self.text, language=self.language)
