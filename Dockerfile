# ---- build stage ----
FROM golang:1.26-bookworm AS build
WORKDIR /src/artifacts/api-server
COPY artifacts/api-server/ ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/fingerprint-server ./cmd/fingerprint-server

# ---- runtime stage (needs ffmpeg + ffprobe on PATH) ----
FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ffmpeg ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 app
COPY --from=build /out/fingerprint-server /usr/local/bin/fingerprint-server
USER app
ENV PORT=10000
EXPOSE 10000
CMD ["fingerprint-server"]
