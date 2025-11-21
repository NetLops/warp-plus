# syntax=docker/dockerfile:1.6

FROM --platform=$BUILDPLATFORM golang:1.24 AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 \
    GOOS=${TARGETOS:-linux} \
    GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" -o /out/warp-plus ./cmd/warp-plus

FROM debian:bookworm-slim
LABEL org.opencontainers.image.title="warp-plus"
LABEL org.opencontainers.image.source="https://github.com/bepass-org/warp-plus"

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates libcap2-bin && \
    rm -rf /var/lib/apt/lists/*

ENV HOME=/var/lib/warp-plus
ENV XDG_CACHE_HOME=/var/lib/warp-plus
ENV WARP_PLUS_CACHE_DIR=/var/lib/warp-plus

WORKDIR /var/lib/warp-plus

RUN useradd --system --home-dir /var/lib/warp-plus --shell /usr/sbin/nologin warp && \
    chown -R warp:warp /var/lib/warp-plus

COPY --from=builder /out/warp-plus /usr/local/bin/warp-plus
RUN setcap 'cap_net_admin,cap_net_raw=eip' /usr/local/bin/warp-plus

VOLUME ["/var/lib/warp-plus"]
EXPOSE 8086/tcp

ENTRYPOINT ["/usr/local/bin/warp-plus"]
CMD ["--bind=0.0.0.0:8086", "--cache-dir=/var/lib/warp-plus"]
