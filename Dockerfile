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

# --- runtime stage ---------------------------------------------------------
FROM alpine:3
# wget powers the HEALTHCHECK; ca-certificates for any outbound TLS.
RUN apk add --no-cache wget ca-certificates \
 && adduser -D -H -u 10001 regent \
 && mkdir -p /data \
 && chown regent /data

COPY --from=build /out/rgt /usr/local/bin/rgt

# Served repos live here; a volume keeps them across container churn.
VOLUME /data
EXPOSE 7654
USER regent

# Set REGENT_SERVER_TOKEN at runtime to require bearer-token auth (see
# `rgt serve --help`). Left unset, the server runs open (local-dev default).
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:7654/healthz || exit 1

ENTRYPOINT ["rgt"]
CMD ["serve", "--addr", "0.0.0.0:7654", "--data", "/data"]
