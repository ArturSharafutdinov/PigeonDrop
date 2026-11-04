"""NVIDIA Parakeet TDT 0.6B v3 (25 European languages) through onnx-asr, int8 weights, CPU only."""
import onnxruntime as ort
import onnx_asr

from runner import run, visible_cpus


class Engine:
    name = "Parakeet TDT 0.6B v3 int8 (onnx-asr)"

    def __init__(self):
        opts = ort.SessionOptions()
        opts.intra_op_num_threads = visible_cpus()
        self.model = onnx_asr.load_model("nemo-parakeet-tdt-0.6b-v3", quantization="int8", sess_options=opts)

    def transcribe(self, path, lang):
        # The model detects the language itself and needs no hint.
        return {"text": self.model.recognize(path), "language": None}


if __name__ == "__main__":
    run(Engine)
