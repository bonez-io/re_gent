.PHONY: help build release-binaries test test-race test-cover lint fmt clean install dist install-dist server server-down server-logs ui ui-install ui-check dev

# Default target
help:
	@echo "Regent Development Commands"
	@echo ""
	@echo "  make build        - Build rgt binary"
	@echo "  make test         - Run all tests"
	@echo "  make test-race    - Run tests with race detector"
	@echo "  make test-cover   - Run tests with coverage report"
	@echo "  make lint         - Run golangci-lint"
	@echo "  make fmt          - Format code with gofmt"
	@echo "  make clean        - Remove build artifacts"
	@echo "  make install      - Install this local build to Go's bin directory"
	@echo "  make dist         - Build the release artifact for this OS/arch via goreleaser (./dist)"
	@echo "  make install-dist - Build dist and install that binary to GOPATH/bin"
	@echo "  make dev          - Start the local server, then the UI dev server"
	@echo "  make ui           - Start the UI dev server (expects the server on :7654)"
	@echo "  make ui-check     - Build and browser-test the UI"
	@echo ""
	@echo "  Local development server (Docker):"
	@echo "  make server      - Build & start the server (docker compose up -d)"
	@echo "                     Local development only; binds to 127.0.0.1."
	@echo "  make server-down - Stop the server"
	@echo "  make server-logs - Follow server logs"
	@echo ""

# Version stamping (mirrors .goreleaser.yaml ldflags). Falls back gracefully
# outside a git checkout.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X github.com/bonez-io/re_gent/internal/cli.Version=$(VERSION) \
           -X github.com/bonez-io/re_gent/internal/cli.Commit=$(COMMIT) \
           -X github.com/bonez-io/re_gent/internal/cli.Date=$(DATE)

build:
	go build -ldflags "$(LDFLAGS)" -o rgt ./cmd/rgt

# Cross-compile per-OS/arch binaries into dist/binaries so a non-Docker host can
# point `rgt serve --binaries-dir dist/binaries` (or REGENT_BINARIES_DIR) at
# them and /install can hand every teammate a runnable binary.
RELEASE_TARGETS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64
release-binaries:
	@mkdir -p dist/binaries
	@for t in $(RELEASE_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=""; [ "$$os" = windows ] && ext=".exe"; \
		echo "  building rgt_$${os}_$${arch}$${ext}"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" \
			-o dist/binaries/rgt_$${os}_$${arch}$${ext} ./cmd/rgt || exit 1; \
	done
	@echo "per-OS binaries in dist/binaries/"

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

# Respect an explicitly configured Go bin directory and otherwise use GOPATH/bin.
# Passing GOBIN to `go install` keeps the build target and verification target
# identical. The install never rewrites some other package manager's binary.
GOBIN ?= $(shell bin=$$(go env GOBIN); if [ -n "$$bin" ]; then echo "$$bin"; else echo "$$(go env GOPATH)/bin"; fi)

install:
	@mkdir -p "$(GOBIN)"
	GOBIN="$(GOBIN)" go install -ldflags "$(LDFLAGS)" ./cmd/rgt
	@target="$(GOBIN)/rgt"; \
	"$$target" version; \
	resolved=$$(command -v rgt 2>/dev/null || true); \
	if [ "$$resolved" = "$$target" ]; then \
		echo "Installed local rgt -> $$target"; \
	elif [ -z "$$resolved" ]; then \
		echo "Installed local rgt -> $$target"; \
		echo "Add $(GOBIN) to PATH to run it as 'rgt'."; \
	else \
		echo "Installed local rgt -> $$target"; \
		echo "Note: 'rgt' currently resolves to $$resolved; put $(GOBIN) earlier in PATH to use this build."; \
	fi

# Builds the release artifact for the host OS/arch only (--single-target),
# using the same ldflags/hooks as an actual release, without cutting a tag.
dist:
	goreleaser build --snapshot --clean --single-target

install-dist: dist
	@bin=$$(find dist -type f -name rgt | head -1); \
	if [ -z "$$bin" ]; then echo "error: no rgt binary found under dist/"; exit 1; fi; \
	mkdir -p "$(GOBIN)"; \
	install -m 0755 "$$bin" "$(GOBIN)/rgt"; \
	echo "Installed $$bin -> $(GOBIN)/rgt"

# --- Local development server (Docker) -------------------------------------
# Start the object/ref server in a container for local development. The server
# is currently open and Compose binds it to loopback only. Remote deployment,
# authentication, TLS, and Terraform are intentionally deferred.
server:
	docker compose up -d --build
	@echo ""
	@echo "re_gent server is up on http://localhost:$${REGENT_PORT:-7654} (health: /healthz)."
	@echo "Local development server (open, loopback-only)."
	@echo "Connect a repo to it:"
	@echo "  rgt connect http://localhost:$${REGENT_PORT:-7654}"

server-down:
	docker compose down

server-logs:
	docker compose logs -f

# --- UI development -------------------------------------------------------
ui-install:
	cd web && corepack pnpm install --frozen-lockfile

ui:
	cd web && corepack pnpm dev

ui-check:
	cd web && corepack pnpm run check

# The server remains in Docker while Vite stays in the foreground. Ctrl-C
# stops Vite; `make server-down` stops the persistent local server.
dev: server ui-install
	cd web && corepack pnpm dev
