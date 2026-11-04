"""Whisper large-v3-turbo through faster-whisper (CTranslate2), int8 weights, CPU only.

Against invented text on silence and noise: the VAD is on (`vad_filter`) and `condition_on_previous_text` is off."""
from faster_whisper import WhisperModel

from runner import run, visible_cpus


class Engine:
    name = "Whisper large-v3-turbo int8 (faster-whisper)"

    def __init__(self):
        self.model = WhisperModel("large-v3-turbo", device="cpu", compute_type="int8",
                                  cpu_threads=visible_cpus(), download_root="/models", local_files_only=True)

    def transcribe(self, path, lang):
        segments, info = self.model.transcribe(path, language=lang, beam_size=5, vad_filter=True,
                                               condition_on_previous_text=False)
        return {"text": " ".join(s.text.strip() for s in segments), "language": info.language}


if __name__ == "__main__":
    run(Engine)
