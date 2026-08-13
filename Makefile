.PHONY: help build test test-race test-cover lint fmt clean install dist install-dist

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
	@echo "  make install      - Install rgt to GOPATH/bin (plain go install)"
	@echo "  make dist         - Build the release artifact for this OS/arch via goreleaser (./dist)"
	@echo "  make install-dist - Build dist and install that binary to GOPATH/bin"
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

# GOBIN mirrors what `go install` targets, so `install` and `install-dist`
# land the binary in the same place.
GOBIN ?= $(shell go env GOPATH)/bin

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/rgt
	@target="$(GOBIN)/rgt"; \
	shadow_failed=0; \
	found_target=0; old_ifs="$$IFS"; IFS=':'; \
	for dir in $$PATH; do \
		if [ "$$dir" = "$(GOBIN)" ]; then found_target=1; continue; fi; \
		if [ "$$found_target" = 1 ]; then continue; fi; \
		candidate="$$dir/rgt"; \
		if [ -e "$$candidate" ] || [ -L "$$candidate" ]; then \
			IFS="$$old_ifs"; \
			echo "found rgt earlier in PATH, shadowing the build just installed: $$candidate"; \
			if rm -f "$$candidate" 2>/dev/null && install -m 0755 "$$target" "$$candidate" 2>/dev/null; then \
				echo "  -> replaced with the freshly built binary"; \
			else \
				echo "  -> could not overwrite (no permission); remove or update it yourself, e.g. 'brew uninstall regent'"; \
				shadow_failed=1; \
			fi; \
			IFS=':'; \
		fi; \
	done; \
	IFS="$$old_ifs"; \
	resolved=$$(command -v rgt 2>/dev/null); \
	if [ -z "$$resolved" ]; then \
		echo "warning: $$target was built but no 'rgt' is on your PATH; add $(GOBIN) to PATH"; \
	elif [ "$$shadow_failed" = 1 ]; then \
		echo "warning: rgt still resolves to $$resolved, not $$target"; \
	else \
		echo "Installed -> rgt is on PATH at $$resolved and up to date"; \
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
