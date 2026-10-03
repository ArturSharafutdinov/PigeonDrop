# syntax=docker/dockerfile:1
# The PigeonDrop server as one static binary in a minimal image (no shell, runs as a non-root user).

FROM golang:1.26-alpine AS build
WORKDIR /src
# Dependencies first, so this layer is reused until go.mod or go.sum change.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/pigeondrop ./cmd/pigeondrop
RUN mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/pigeondrop /pigeondrop
# STORAGE_DIR must be a persistent volume mounted at /data; it is created here and owned by the
# unprivileged user so that a fresh Docker volume is writable.
COPY --from=build --chown=65532:65532 /data /data
ENV PORT=3000 \
    STORAGE_DIR=/data
USER 65532:65532
EXPOSE 3000
# The image has no curl or wget, so the binary checks itself.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/pigeondrop", "healthcheck"]
ENTRYPOINT ["/pigeondrop"]
CMD ["serve"]
