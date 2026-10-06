# Whisper large-v3-turbo q5_0, whisper.cpp. Сборка из каталога bench/stt/docker:
#   docker build -f whispercpp/Dockerfile -t stt-whispercpp .
FROM debian:trixie-slim AS build
RUN apt-get update && apt-get install -y --no-install-recommends build-essential cmake git curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*
ARG WHISPER_CPP_VERSION=v1.9.4
RUN git clone --depth 1 --branch ${WHISPER_CPP_VERSION} https://github.com/ggml-org/whisper.cpp /src
WORKDIR /src
# Процессор определяется при сборке (GGML_NATIVE): образ нужно собирать на той машине, где он будет работать.
# Компилятор из Debian 13 (gcc 14) важен: gcc 12 из Debian 12 не распознал Apple M4 и собрал медленные ядра
# (кодировщик в 4 раза медленнее, +MATMUL_INT8 появляется только с новым компилятором).
RUN cmake -B build -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF \
    && cmake --build build -j4 --target whisper-cli
# Повторы и докачка: 574 МБ с Hugging Face иногда обрываются посреди загрузки.
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
