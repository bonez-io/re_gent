#!/bin/sh
# re_gent one-line installer — the "one paste" onboarding path for plain laptops.
#
# RECOMMENDED: your `rgt serve` instance hosts this script and a prebuilt binary
# for you, so teammates need NO Go toolchain. Share this exact line (the server
# personalizes the script with its own address):
#
#   curl -fsSL http://YOUR-TEAM-SERVER/install | sh
#
# That server-hosted script (see internal/server/install.go, GET /install):
#   1. Downloads the `rgt` binary from <server>/bin/rgt into ~/.local/bin (or
#      /usr/local/bin), chmod +x, and verifies it runs — zero dependencies when
#      the teammate is on the same OS/arch as the server (the common case).
#   2. Falls back to `go install` only if that binary can't exec here and Go is
#      present; otherwise prints a clear manual instruction.
#   3. For an open server (the default, on a private network) it just confirms
#      you're wired up; for a public/token server it prints the export step.
#
# This standalone copy is the SOURCE-ONLY fallback: use it when you are not
# fetching from a live server (no prebuilt binary available). It installs via
# `go install`, so it needs Go. Prefer the server-hosted `/install` above.
#
# It writes no config: the repo's committed .regent/config.toml already wires
# capture to the team server. The default server is OPEN (no auth) on a private
# network, so no token is needed. A token is OPTIONAL and only for a public
# server: the CLIENT env var is REGENT_TOKEN (the server side uses
# REGENT_SERVER_TOKEN).
#
# Idempotent and safe to re-run.
set -eu

RGT_MODULE="github.com/regent-vcs/regent/cmd/rgt@latest"

info() { printf '  %s\n' "$*"; }
warn() { printf '  ! %s\n' "$*" >&2; }

printf '\n== re_gent installer (source fallback) ==\n\n'

# ---------------------------------------------------------------------------
# 1. Install rgt (skip if already present so re-runs are cheap and idempotent).
# ---------------------------------------------------------------------------
if command -v rgt >/dev/null 2>&1; then
  info "rgt already installed: $(command -v rgt)"
else
  if command -v go >/dev/null 2>&1; then
    info "Installing rgt from source via 'go install'..."
    go install "$RGT_MODULE"
  else
    warn "Go is not installed, and this standalone script builds from source."
    warn "Use the server-hosted installer instead — it ships a prebuilt binary:"
    warn "  curl -fsSL http://YOUR-TEAM-SERVER/install | sh"
    warn "Or install Go (https://go.dev/dl/) and re-run this script."
    exit 1
  fi
fi

# ---------------------------------------------------------------------------
# 2. Make sure the freshly-installed binary is reachable, then verify.
#    'go install' drops binaries in $GOBIN, else $GOPATH/bin, else ~/go/bin.
# ---------------------------------------------------------------------------
if ! command -v rgt >/dev/null 2>&1; then
  GOBIN_DIR=$(go env GOBIN 2>/dev/null || true)
  if [ -z "$GOBIN_DIR" ]; then
    GOBIN_DIR="$(go env GOPATH 2>/dev/null || echo "$HOME/go")/bin"
  fi
  case ":${PATH}:" in
    *":${GOBIN_DIR}:"*) : ;;
    *) PATH="${GOBIN_DIR}:${PATH}"; export PATH ;;
  esac
fi

if ! command -v rgt >/dev/null 2>&1; then
  GOBIN_DIR=$(go env GOBIN 2>/dev/null || true)
  [ -n "$GOBIN_DIR" ] || GOBIN_DIR="$(go env GOPATH 2>/dev/null || echo "$HOME/go")/bin"
  warn "rgt was installed but is not on your PATH."
  warn "Add this to your shell profile and open a new shell:"
  warn "  export PATH=\"${GOBIN_DIR}:\$PATH\""
  exit 1
fi

info "rgt is ready: $(command -v rgt)"
rgt version 2>/dev/null || true

# ---------------------------------------------------------------------------
# 3. You're wired up. Server wiring is already handled by the repo's committed
#    .regent/config.toml (url + repo_id) — nothing else to configure.
#
#    The default server is OPEN (no auth) on a private network, so there is no
#    token to set. A token is OPTIONAL and only for a public/token-protected
#    server (a secret, never committed).
# ---------------------------------------------------------------------------
printf '\n== You are done ==\n\n'
info "Server wiring is already handled by the repo's committed .regent/config.toml"
info "(url + repo_id). Start an agent turn in the repo."
info ""
info "Optional — only if your team's server is PUBLIC and requires a token:"
info "  export REGENT_TOKEN=<the-team-token>   # add to ~/.zshrc / ~/.bashrc to persist"
printf '\n'
