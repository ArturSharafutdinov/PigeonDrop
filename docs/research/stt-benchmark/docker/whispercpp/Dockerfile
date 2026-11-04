# Whisper large-v3-turbo q5_0, whisper.cpp. Build from the docs/research/stt-benchmark/docker directory:
#   docker build -f whispercpp/Dockerfile -t stt-whispercpp .
FROM debian:trixie-slim AS build
RUN apt-get update && apt-get install -y --no-install-recommends build-essential cmake git curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*
ARG WHISPER_CPP_VERSION=v1.9.4
RUN git clone --depth 1 --branch ${WHISPER_CPP_VERSION} https://github.com/ggml-org/whisper.cpp /src
WORKDIR /src
# The CPU is detected at build time (GGML_NATIVE): build the image on the machine where it will run.
# The compiler from Debian 13 (gcc 14) matters: gcc 12 from Debian 12 did not recognise the Apple M4 and built slow kernels
# (the encoder was 4 times slower; +MATMUL_INT8 only appears with the newer compiler).
RUN cmake -B build -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF \
    && cmake --build build -j4 --target whisper-cli
# Retries and resuming: the 574 MB download from Hugging Face sometimes breaks off in the middle.
RUN mkdir /models \
    && curl -fL --retry 6 --retry-all-errors --retry-delay 3 -C - -o /models/ggml-large-v3-turbo-q5_0.bin https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo-q5_0.bin \
    && curl -fL --retry 6 --retry-all-errors --retry-delay 3 -C - -o /models/ggml-silero-v5.1.2.bin https://huggingface.co/ggml-org/whisper-vad/resolve/main/ggml-silero-v5.1.2.bin

FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends python3 libgomp1 libstdc++6 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /src/build/bin/whisper-cli /usr/local/bin/whisper-cli
COPY --from=build /models /models
COPY common/runner.py /app/runner.py
COPY whispercpp/engine.py /app/engine.py
WORKDIR /app
ENTRYPOINT ["python3", "/app/engine.py"]
