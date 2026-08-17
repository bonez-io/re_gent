# syntax=docker/dockerfile:1

# --- build stage -----------------------------------------------------------
FROM golang:1.23-alpine AS build
WORKDIR /src

# Download modules first so they cache independently of source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Static, CGO-free binary (matches .goreleaser.yaml: CGO_ENABLED=0).
RUN CGO_ENABLED=0 go build -trimpath -o /out/rgt ./cmd/rgt

# Cross-compile per-OS/arch binaries so /install can hand every teammate a
# runnable rgt, not only those matching the server's platform. Served from
# REGENT_BINARIES_DIR below.
RUN set -eu; mkdir -p /out/binaries; \
    for t in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64; do \
      os="${t%/*}"; arch="${t#*/}"; ext=""; [ "$os" = windows ] && ext=".exe"; \
      GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 go build -trimpath \
        -o "/out/binaries/rgt_${os}_${arch}${ext}" ./cmd/rgt; \
    done

# --- runtime stage ---------------------------------------------------------
FROM alpine:3
# wget powers the HEALTHCHECK; ca-certificates for any outbound TLS.
RUN apk add --no-cache wget ca-certificates \
 && adduser -D -H -u 10001 regent \
 && mkdir -p /data \
 && chown regent /data

COPY --from=build /out/rgt /usr/local/bin/rgt
# Prebuilt per-OS/arch binaries served by /install (see REGENT_BINARIES_DIR).
COPY --from=build /out/binaries /binaries
ENV REGENT_BINARIES_DIR=/binaries

# Served repos live here; a volume keeps them across container churn.
VOLUME /data
EXPOSE 7654
USER regent

# The current server is open. docker-compose.yml binds it to loopback for local
# development; remote authentication/TLS are separate deployment work.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:7654/healthz || exit 1

ENTRYPOINT ["rgt"]
CMD ["serve", "--addr", "0.0.0.0:7654", "--data", "/data"]
