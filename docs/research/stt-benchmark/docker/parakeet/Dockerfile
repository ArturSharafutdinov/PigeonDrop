# Parakeet TDT 0.6B v3, int8, onnx-asr. Build from the docs/research/stt-benchmark/docker directory:
#   docker build -f parakeet/Dockerfile -t stt-parakeet .
# The model is downloaded at build time and lives in the image, so it can run with --network none.
FROM python:3.12-slim
ENV PYTHONUNBUFFERED=1 PIP_NO_CACHE_DIR=1 HF_HOME=/models
RUN pip install "onnx-asr[cpu,hub]==0.12.0"
RUN python -c "import onnx_asr; onnx_asr.load_model('nemo-parakeet-tdt-0.6b-v3', quantization='int8')"
ENV HF_HUB_OFFLINE=1
COPY common/runner.py /app/runner.py
COPY parakeet/engine.py /app/engine.py
WORKDIR /app
ENTRYPOINT ["python", "/app/engine.py"]
