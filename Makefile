.PHONY: help build release-binaries test test-race test-cover lint fmt clean install dist install-dist server server-down server-logs ui ui-install ui-check dev serve serve-open smoke bin/rgt bin/regent-server

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
	@echo "  make dev          - Native server (background) + UI dev server. No Docker needed."
	@echo "  make ui           - Start the UI dev server (expects the server on :7655)"
	@echo "  make ui-check     - Build and browser-test the UI"
	@echo ""
	@echo "  Local development server (native, no Docker):"
	@echo "  make serve        - Build & run regent-server in self-hosted mode on 127.0.0.1:7655"
	@echo "  make serve-open   - Same, but the legacy fully-open (no-auth) mode"
	@echo "  make smoke        - End-to-end check of the local dev loop (scripts/dev-smoke.sh)"
	@echo ""
	@echo "  Local development server (Docker, optional):"
	@echo "  make server      - Build & start the server + web UI (docker compose up -d)"
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

# --- Local development server (Docker, optional) ----------------------------
# Start the server + web UI in containers. Not required for local dev: `make
# serve` / `make dev` below run the same server natively, with no Docker
# dependency. Remote deployment, TLS, and Terraform remain out of scope here;
# see docker-compose.production.yml and docs/self-hosted.md for those.
server:
	docker compose up -d --build
	@echo ""
	@echo "re_gent server is up on http://localhost:$${REGENT_PORT:-7654} (health: /healthz)."
	@echo "re_gent web UI is up on http://localhost:8080."
	@echo "Self-hosted auth is on by default. Read the one-time bootstrap token for the first owner:"
	@echo "  docker compose exec server cat /data/bootstrap-token"
	@echo "For the legacy fully-open (no-auth) loopback mode instead:"
	@echo "  docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d --build"

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

# --- Local development server (native, no Docker) ---------------------------
# Same regent-server binary Docker runs, built and run directly on this
# machine. REGENT_PORT and REGENT_DATA are overridable; defaults avoid the
# port this machine already reserves for another project's server (7654).
REGENT_PORT ?= 7655
REGENT_LOCAL_DIR ?= .local
REGENT_DATA ?= $(REGENT_LOCAL_DIR)/data

bin/rgt:
	@mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o bin/rgt ./cmd/rgt

bin/regent-server:
	@mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o bin/regent-server ./cmd/regent-server

# Persistent self-hosted auth (same composition as production), foreground.
# First start writes a one-time bootstrap credential; `scripts/dev-bootstrap.sh`
# claims it and signs the CLI in automatically.
serve: bin/rgt bin/regent-server
	@mkdir -p $(REGENT_DATA)
	@echo "Starting regent-server on 127.0.0.1:$(REGENT_PORT) (auth: self-hosted, data: $(REGENT_DATA))"
	@echo "First start only: bootstrap token written to $(REGENT_DATA)/bootstrap-token"
	@echo "Run ./scripts/dev-bootstrap.sh in another terminal to claim it and sign in."
	./bin/regent-server --addr 127.0.0.1:$(REGENT_PORT) --data $(REGENT_DATA) --auth-mode self-hosted

# Legacy fully-open (no application auth) mode, foreground, loopback only.
serve-open: bin/rgt bin/regent-server
	@mkdir -p $(REGENT_DATA)
	@echo "Starting regent-server on 127.0.0.1:$(REGENT_PORT) (auth: open, loopback-only, data: $(REGENT_DATA))"
	./bin/regent-server --addr 127.0.0.1:$(REGENT_PORT) --data $(REGENT_DATA) --insecure-no-auth

# Runs the native server in the background and Vite in the foreground, in one
# terminal, with no Docker dependency. Ctrl-C stops Vite and the trap below
# stops the background server with it.
#
# Two-terminal alternative, if you would rather see the server's own output:
#   make serve            # terminal 1
#   make ui               # terminal 2 (after ./scripts/dev-bootstrap.sh)
dev: bin/rgt bin/regent-server ui-install
	@mkdir -p $(REGENT_DATA)
	@( \
		./bin/regent-server --addr 127.0.0.1:$(REGENT_PORT) --data $(REGENT_DATA) --auth-mode self-hosted \
			> $(REGENT_LOCAL_DIR)/server.log 2>&1 & \
		server_pid=$$!; \
		trap 'echo; echo "Stopping regent-server (pid $$server_pid)"; kill $$server_pid 2>/dev/null || true' EXIT INT TERM; \
		echo "regent-server pid $$server_pid on 127.0.0.1:$(REGENT_PORT); logs: $(REGENT_LOCAL_DIR)/server.log"; \
		echo "Run ./scripts/dev-bootstrap.sh once the server answers /healthz."; \
		cd web && VITE_REGENT_SERVER_URL=http://127.0.0.1:$(REGENT_PORT) corepack pnpm dev; \
	)

# End-to-end check of the whole native dev loop: server, bootstrap, CLI login,
# connect, a captured turn, sync, and the server-side read. Uses its own free
# port and temp directories; never touches ./bin, ./.local, or Docker.
smoke:
	./scripts/dev-smoke.sh
