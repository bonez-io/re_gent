.PHONY: help build test test-race test-cover lint fmt clean install server server-down server-logs

# Default target
help:
	@echo "Regent Development Commands"
	@echo ""
	@echo "  make build      - Build rgt binary"
	@echo "  make test       - Run all tests"
	@echo "  make test-race  - Run tests with race detector"
	@echo "  make test-cover - Run tests with coverage report"
	@echo "  make lint       - Run golangci-lint"
	@echo "  make fmt        - Format code with gofmt"
	@echo "  make clean      - Remove build artifacts"
	@echo "  make install    - Install rgt to GOPATH/bin"
	@echo ""
	@echo "  Self-hosted server (Docker):"
	@echo "  make server      - Build & start the server (docker compose up -d)"
	@echo "  make server-down - Stop the server"
	@echo "  make server-logs - Follow server logs"
	@echo ""

# Version stamping (mirrors .goreleaser.yaml ldflags). Falls back gracefully
# outside a git checkout.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X github.com/regent-vcs/regent/internal/cli.Version=$(VERSION) \
           -X github.com/regent-vcs/regent/internal/cli.Commit=$(COMMIT) \
           -X github.com/regent-vcs/regent/internal/cli.Date=$(DATE)

build:
	go build -ldflags "$(LDFLAGS)" -o rgt ./cmd/rgt

test:
	go test ./...

test-race:
	go test -race ./...

test-cover:
	go test -cover -coverprofile=coverage.txt ./...
	go tool cover -html=coverage.txt -o coverage.html
	@echo "Coverage report: coverage.html"

lint:
	golangci-lint run

fmt:
	gofmt -w .

clean:
	rm -f rgt
	rm -f coverage.txt coverage.html
	rm -rf dist/

install:
	go install ./cmd/rgt

# --- Self-hosted server (Docker) -------------------------------------------
# Start the object/ref server in a container. Set REGENT_SERVER_TOKEN to require
# bearer-token auth (recommended for anything network-reachable):
#   REGENT_SERVER_TOKEN=$(openssl rand -hex 32) make server
server:
	docker compose up -d --build
	@echo ""
	@echo "re_gent server is up on http://localhost:$${REGENT_PORT:-7654} (health: /healthz)."
	@echo "Connect a repo to it:"
	@echo "  rgt connect http://localhost:$${REGENT_PORT:-7654}"
	@echo "If REGENT_SERVER_TOKEN is set, run 'rgt login http://localhost:$${REGENT_PORT:-7654}' first."

server-down:
	docker compose down

server-logs:
	docker compose logs -f
