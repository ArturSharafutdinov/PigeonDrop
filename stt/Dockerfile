# The speech recognition service: Parakeet TDT 0.6B v3 (int8) behind a small HTTP API. Build from the repository root:
#   docker build -f stt/Dockerfile -t pigeondrop-stt stt
# The model is downloaded while the image is built and lies inside it (about 640 MB), so the container needs no
# network and starts without downloading anything. Run it with a memory limit of 3 GB and without swap
# (--memory=3g --memory-swap=3g): memory was measured up to a 60 second recording (STT_COMPARISON.md, 7.4).

# The tag says which Python this is; the digest is what is pulled (Dependabot moves both).
FROM python:3.12-slim@sha256:b18b261e1b4974b4f321451ad5251ded85abb22c2b8b262da527dda1d031ab30
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1 PIP_NO_CACHE_DIR=1 HF_HOME=/models
# onnx-asr 0.12.0 is the version of the benchmark in bench/stt, where the numbers in STT_COMPARISON.md come from;
# requirements.txt pins it together with everything it pulls in.
COPY requirements.txt /tmp/requirements.txt
RUN pip install -r /tmp/requirements.txt
# The model is fetched into HF_HOME once, from the Hugging Face repository that onnx-asr names, and its revision must be
# the one that was measured: when this step stops with a mismatch the upstream model has changed, so run
# stt/smoke-synthetic.sh (and the comparison in docs, if the text changed) before moving MODEL_REVISION. The cache
# then belongs to the unprivileged user below, which only reads it.
ARG MODEL_REVISION=8f23f0c03c8761650bdb5b40aaf3e40d2c15f1ce
RUN python -c "import onnx_asr; onnx_asr.load_model('nemo-parakeet-tdt-0.6b-v3', quantization='int8')" \
    && test "$(cat /models/hub/models--istupakov--parakeet-tdt-0.6b-v3-onnx/refs/main)" = "$MODEL_REVISION" \
    && chown -R 65532:65532 /models
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
