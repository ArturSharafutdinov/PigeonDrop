# Whisper large-v3-turbo, int8, faster-whisper. Сборка из каталога bench/stt/docker:
#   docker build -f faster-whisper/Dockerfile -t stt-faster-whisper .
FROM python:3.12-slim
ENV PYTHONUNBUFFERED=1 PIP_NO_CACHE_DIR=1 HF_HOME=/models
# av закреплён: из более новых версий убран параметр metadata_errors, который вызывает faster-whisper 1.2.1.
RUN pip install "faster-whisper==1.2.1" "av==15.1.0"
# Только скачивание (без загрузки в память: при сборке памяти может не хватить).
RUN python -c "from faster_whisper.utils import download_model; print(download_model('large-v3-turbo', cache_dir='/models'))"
ENV HF_HUB_OFFLINE=1
COPY common/runner.py /app/runner.py
COPY faster-whisper/engine.py /app/engine.py
WORKDIR /app
ENTRYPOINT ["python", "/app/engine.py"]
