"""The model behind the service. The server knows only the `Engine` shape, so another model is another class here
(or another image), and nothing else changes."""
from __future__ import annotations

import dataclasses
from typing import Optional

from .cpus import available_cpus


@dataclasses.dataclass(frozen=True)
class Transcript:
    text: str
    # What the model says it heard, if it says; Parakeet does not report it.
    language: Optional[str] = None


class Engine:
    """What the server needs from a model."""

    # Names the model in every answer, so that a text can be traced to what made it.
    name = "unnamed"

    def transcribe(self, pcm16: bytes) -> Transcript:
        """pcm16 is 16 kHz mono 16-bit little-endian PCM. Called by one thread at a time."""
        raise NotImplementedError


class ParakeetEngine(Engine):
    """NVIDIA Parakeet TDT 0.6B v3 (25 European languages, language found by the model itself), int8, on the CPU,
    through onnx-asr. The model is in the image (HF_HUB_OFFLINE=1): nothing is downloaded when it starts."""

    name = "parakeet-tdt-0.6b-v3-int8"

    def __init__(self, threads: Optional[int] = None):
        # Imported here so that the server and its tests run without the model's libraries installed.
        import numpy
        import onnx_asr
        import onnxruntime

        self._numpy = numpy
        options = onnxruntime.SessionOptions()
        options.intra_op_num_threads = threads or available_cpus()
        self.threads = options.intra_op_num_threads
        self._model = onnx_asr.load_model("nemo-parakeet-tdt-0.6b-v3", quantization="int8", sess_options=options)

    def transcribe(self, pcm16: bytes) -> Transcript:
        np = self._numpy
        samples = np.frombuffer(pcm16, dtype="<i2").astype(np.float32) / 32768.0
        return Transcript(text=str(self._model.recognize(samples, sample_rate=16000)).strip())
