# The speech recognition service: Parakeet TDT 0.6B v3 (int8) behind a small HTTP API. Build from the repository root:
#   docker build -f stt/Dockerfile -t pigeondrop-stt stt
# The model is downloaded while the image is built and lies inside it (about 640 MB), so the container needs no
# network and starts without downloading anything. Run it with a memory limit of 3 GB and without swap
# (--memory=3g --memory-swap=3g): memory was measured up to a 60 second recording (STT_COMPARISON.md, 7.4).

FROM python:3.12-slim
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1 PIP_NO_CACHE_DIR=1 HF_HOME=/models
# The same versions as the benchmark in bench/stt, which is where the numbers in STT_COMPARISON.md come from.
RUN pip install "onnx-asr[cpu,hub]==0.12.0"
# The model is fetched into HF_HOME once; the permissions let the unprivileged user below read (and, if the
# library wants to, touch) the cache.
RUN python -c "import onnx_asr; onnx_asr.load_model('nemo-parakeet-tdt-0.6b-v3', quantization='int8')" \
    && chmod -R a+rwX /models
ENV HF_HUB_OFFLINE=1

COPY app /app/app
WORKDIR /app
# A fixed non-root user, like the server's image.
USER 65532:65532
ENV PORT=8000
EXPOSE 8000
# 200 only once the model is loaded; loading takes a few seconds, a minute at most on a slow disk.
HEALTHCHECK --interval=30s --timeout=5s --start-period=90s --retries=3 \
    CMD ["python", "-c", "import os, sys, urllib.request; sys.exit(0 if urllib.request.urlopen('http://127.0.0.1:' + os.environ.get('PORT', '8000') + '/healthz', timeout=3).status == 200 else 1)"]
ENTRYPOINT ["python", "-m", "app.server"]
