"""GigaAM v3 (русский, со встроенной пунктуацией) через onnx-asr, веса int8, только процессор.

Один вызов GigaAM принимает до 25 с звука, а записи с часов бывают до 60 с, поэтому запись режется по паузам
(Silero VAD) и куски склеиваются."""
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
        # Модель русская и языка не определяет, поэтому «определённого языка» в ответе нет.
        return {"text": " ".join(s.text.strip() for s in segments), "language": None}


if __name__ == "__main__":
    run(Engine)
