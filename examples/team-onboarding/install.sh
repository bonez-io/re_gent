#!/bin/sh
# re_gent one-line installer — the "one paste" onboarding path for plain laptops.
#
#   curl -fsSL https://YOUR-TEAM-SERVER/install | sh
#
# What it does:
#   1. Installs the `rgt` binary (go install; see TODO for a binary fallback).
#   2. Verifies `rgt` is on PATH.
#   3. Prints the one remaining manual step: export the team token.
#
# It does NOT write any config: the repo's committed .regent/config.toml already
# wires capture to the team server. It does NOT touch the token (a secret).
#
# Idempotent and safe to re-run.
set -eu

RGT_MODULE="github.com/regent-vcs/regent/cmd/rgt@latest"

info() { printf '  %s\n' "$*"; }
warn() { printf '  ! %s\n' "$*" >&2; }

printf '\n== re_gent installer ==\n\n'

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
    # TODO(team): no public prebuilt release exists yet. When one does, replace
    # this branch with a download, e.g.:
    #   os=$(uname -s | tr '[:upper:]' '[:lower:]'); arch=$(uname -m)
    #   url="https://YOUR-TEAM-SERVER/dist/rgt-${os}-${arch}"
    #   curl -fsSL "$url" -o "$HOME/.local/bin/rgt" && chmod +x "$HOME/.local/bin/rgt"
    # Alternatively copy the binary your team built from the shared dev box.
    warn "Go is not installed and no prebuilt binary source is configured yet."
    warn "Install Go (https://go.dev/dl/) and re-run, or ask your team for a"
    warn "prebuilt rgt binary and place it on your PATH. See the TODO in this script."
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
rgt --version 2>/dev/null || true

# ---------------------------------------------------------------------------
# 3. Final manual step: the shared team token (a secret, never committed).
# ---------------------------------------------------------------------------
printf '\n== One step left ==\n\n'
if [ -n "${REGENT_TOKEN:-}" ]; then
  info "REGENT_TOKEN is already set — you're done. Run an agent turn in the repo."
else
  info "Export the shared team token, then start working in the repo:"
  info ""
  info "  export REGENT_TOKEN=<the-team-token>"
  info ""
  info "Add that line to your shell profile (~/.zshrc, ~/.bashrc) to persist it."
fi
info ""
info "Server wiring is already handled by the repo's committed .regent/config.toml"
info "(url + repo_id). Nothing else to configure."
printf '\n'
