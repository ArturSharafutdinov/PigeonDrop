"""Whisper large-v3-turbo (ggml, q5_0 quantisation) through whisper.cpp, CPU only.

whisper.cpp is a separate program that loads the model on every call; that time is taken from its log and
reported separately (`call_load_s`), so `infer_s` is recognition only."""
import re
import subprocess

from runner import run, visible_cpus

MODEL = "/models/ggml-large-v3-turbo-q5_0.bin"
VAD_MODEL = "/models/ggml-silero-v5.1.2.bin"


class Engine:
    name = "Whisper large-v3-turbo q5_0 (whisper.cpp)"

    def __init__(self):
        self.threads = visible_cpus()

    def transcribe(self, path, lang):
        cmd = ["whisper-cli", "-m", MODEL, "-f", path, "-t", str(self.threads), "-l", lang or "auto",
               "-nt", "-mc", "0", "--vad", "--vad-model", VAD_MODEL]
        done = subprocess.run(cmd, capture_output=True, text=True, check=True)
        load = re.search(r"load time\s*=\s*([\d.]+)\s*ms", done.stderr)
        detected = re.search(r"auto-detected language:\s*(\w+)", done.stderr)
        return {
            "text": " ".join(done.stdout.split()),
            "language": detected.group(1) if detected else lang,
            "load_s": float(load.group(1)) / 1000 if load else None,
        }


if __name__ == "__main__":
    run(Engine)
