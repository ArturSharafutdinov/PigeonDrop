# syntax=docker/dockerfile:1
# The PigeonDrop server as one static binary in a minimal image (no shell, runs as a non-root user).

# Tags say what the images are; the digests are what is pulled (Dependabot moves both). The Go version matches
# `toolchain` in go.mod, so the build never has to download another one.
FROM golang:1.26.10-alpine@sha256:87c65a688975c0b1d4585d204874c45ca78354670e3a5f11368c5a492303d696 AS build
WORKDIR /src
# Dependencies first, so this layer is reused until go.mod or go.sum change.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/pigeondrop ./cmd/pigeondrop
RUN mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/pigeondrop /pigeondrop
# STORAGE_DIR must be a persistent volume mounted at /data (recordings go to /data/messages); it is
# created here and owned by the unprivileged user so that a fresh Docker volume is writable.
COPY --from=build --chown=65532:65532 /data /data
ENV PORT=3000 \
    STORAGE_DIR=/data
USER 65532:65532
EXPOSE 3000
# The image has no curl or wget, so the binary checks itself.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/pigeondrop", "healthcheck"]
ENTRYPOINT ["/pigeondrop"]
CMD ["serve"]
