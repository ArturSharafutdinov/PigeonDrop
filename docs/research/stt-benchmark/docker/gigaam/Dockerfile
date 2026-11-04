# GigaAM v3 (Russian), int8, onnx-asr + Silero VAD. Build from the docs/research/stt-benchmark/docker directory:
#   docker build -f gigaam/Dockerfile -t stt-gigaam .
FROM python:3.12-slim
ENV PYTHONUNBUFFERED=1 PIP_NO_CACHE_DIR=1 HF_HOME=/models
RUN pip install "onnx-asr[cpu,hub]==0.12.0"
RUN python -c "import onnx_asr; onnx_asr.load_model('gigaam-v3-e2e-rnnt', quantization='int8'); onnx_asr.load_vad('silero')"
ENV HF_HUB_OFFLINE=1
COPY common/runner.py /app/runner.py
COPY gigaam/engine.py /app/engine.py
WORKDIR /app
ENTRYPOINT ["python", "/app/engine.py"]
