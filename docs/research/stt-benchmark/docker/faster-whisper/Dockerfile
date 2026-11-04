# Whisper large-v3-turbo, int8, faster-whisper. Build from the docs/research/stt-benchmark/docker directory:
#   docker build -f faster-whisper/Dockerfile -t stt-faster-whisper .
FROM python:3.12-slim
ENV PYTHONUNBUFFERED=1 PIP_NO_CACHE_DIR=1 HF_HOME=/models
# av is pinned: newer versions dropped the metadata_errors parameter that faster-whisper 1.2.1 passes.
RUN pip install "faster-whisper==1.2.1" "av==15.1.0"
# Download only (do not load into memory: the build may not have enough of it).
RUN python -c "from faster_whisper.utils import download_model; print(download_model('large-v3-turbo', cache_dir='/models'))"
ENV HF_HUB_OFFLINE=1
COPY common/runner.py /app/runner.py
COPY faster-whisper/engine.py /app/engine.py
WORKDIR /app
ENTRYPOINT ["python", "/app/engine.py"]
