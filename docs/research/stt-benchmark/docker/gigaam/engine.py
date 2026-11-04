"""GigaAM v3 (Russian, with built-in punctuation) through onnx-asr, int8 weights, CPU only.

One GigaAM call takes up to 25 s of audio, while recordings from the watch run up to 60 s, so the recording is
split at pauses (Silero VAD) and the pieces are joined."""
import onnxruntime as ort
import onnx_asr

from runner import run, visible_cpus


class Engine:
    name = "GigaAM v3 e2e RNNT int8 + Silero VAD (onnx-asr)"

    def __init__(self):
        opts = ort.SessionOptions()
        opts.intra_op_num_threads = visible_cpus()
        model = onnx_asr.load_model("gigaam-v3-e2e-rnnt", quantization="int8", sess_options=opts)
        vad = onnx_asr.load_vad("silero", sess_options=opts)
        self.model = model.with_vad(vad)

    def transcribe(self, path, lang):
        segments = self.model.recognize(path)
        # The model is Russian-only and does not detect the language, so the answer has no detected language.
        return {"text": " ".join(s.text.strip() for s in segments), "language": None}


if __name__ == "__main__":
    run(Engine)
