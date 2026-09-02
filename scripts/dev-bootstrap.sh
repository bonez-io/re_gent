#!/usr/bin/env bash
#
# dev-bootstrap.sh — claim the local dev server's first-owner setup and sign
# the CLI in, in one step.
#
# Written to run under either bash or zsh:
#
#   ./scripts/dev-bootstrap.sh          # bash, via the shebang
#   bash scripts/dev-bootstrap.sh
#   zsh scripts/dev-bootstrap.sh
#
# fish has no `set -e`/`[ ]` compatible mode of its own for this script; fish
# users should invoke it explicitly with bash or zsh as shown above.
#
# Idempotent: run it as many times as you like. The first run against a fresh
# server claims the bootstrap credential and creates the first owner; every
# run after that finds the server already bootstrapped and exits 0 without
# doing anything.
#
# The one-time bootstrap credential is never printed or logged — this script
# reads it straight from the data directory's bootstrap-token file (mode
# 0600, written by regent-server on first start) and uses it only in-memory.
# The resulting personal access token *is* printed once, the same way the
# production bootstrap flow shows it once in the browser; there is no way to
# recover it later short of re-bootstrapping.

set -eu

SERVER_URL=${REGENT_SERVER_URL:-http://127.0.0.1:${REGENT_PORT:-7655}}
DATA_DIR=${REGENT_DATA:-.local/data}
BOOTSTRAP_FILE="$DATA_DIR/bootstrap-token"
RGT_BIN=${RGT:-}
if [ -z "$RGT_BIN" ]; then
  if [ -x ./bin/rgt ]; then
    RGT_BIN=./bin/rgt
  else
    RGT_BIN=rgt
  fi
fi

USERNAME=${REGENT_DEV_USERNAME:-${USER:-devowner}}
DISPLAY_NAME=${REGENT_DEV_DISPLAY_NAME:-$USERNAME}

log() { printf '%s\n' "$*" >&2; }

fail() {
  log "dev-bootstrap: $*"
  exit 1
}

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v "$RGT_BIN" >/dev/null 2>&1 || fail "rgt binary not found (looked for \$RGT, ./bin/rgt, and rgt on PATH); run 'make serve' first or set \$RGT"

log "dev-bootstrap: waiting for $SERVER_URL/healthz ..."
attempt=0
healthy=0
while [ "$attempt" -lt 60 ]; do
  if curl -fsS -o /dev/null "$SERVER_URL/healthz" 2>/dev/null; then
    healthy=1
    break
  fi
  attempt=$((attempt + 1))
  sleep 0.5
done
if [ "$healthy" -ne 1 ]; then
  fail "server at $SERVER_URL never answered /healthz; is 'make serve' running?"
fi
log "dev-bootstrap: server is up"

capabilities=$(curl -fsS "$SERVER_URL/api/v1/capabilities") || fail "GET $SERVER_URL/api/v1/capabilities failed"
case "$capabilities" in
  *'"bootstrap_required":true'*)
    bootstrap_required=1
    ;;
  *'"bootstrap_required":false'*)
    bootstrap_required=0
    ;;
  *)
    fail "could not read bootstrap_required from capabilities response: $capabilities"
    ;;
esac

if [ "$bootstrap_required" -eq 0 ]; then
  log "dev-bootstrap: server is already bootstrapped; nothing to do."
  exit 0
fi

[ -f "$BOOTSTRAP_FILE" ] || fail "server reports bootstrap_required but $BOOTSTRAP_FILE does not exist (wrong \$REGENT_DATA, or a different server instance is running)"
[ -r "$BOOTSTRAP_FILE" ] || fail "$BOOTSTRAP_FILE is not readable by this user"

bootstrap_token=$(cat "$BOOTSTRAP_FILE")
[ -n "$bootstrap_token" ] || fail "$BOOTSTRAP_FILE is empty"

log "dev-bootstrap: claiming bootstrap credential and creating the first owner ($USERNAME) ..."
response=$(curl -fsS -X POST "$SERVER_URL/api/v1/auth/bootstrap" \
  -H "Authorization: Bootstrap $bootstrap_token" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"$USERNAME\",\"display_name\":\"$DISPLAY_NAME\"}") \
  || fail "POST $SERVER_URL/api/v1/auth/bootstrap failed (has it already been claimed by someone else? re-run this script)"
bootstrap_token=""

# Pull the top-level "token" field out of the compact JSON response
# ({"viewer":{...},"token":"rgt_pat_...","csrf_token":"..."}). No jq
# dependency: everything else this script needs is POSIX curl/sed/grep.
pat=$(printf '%s' "$response" | grep -o '"token":"[^"]*"' | head -1 | sed 's/^"token":"//; s/"$//')
[ -n "$pat" ] || fail "bootstrap response did not include a token: $response"

log "dev-bootstrap: signing the CLI in with the new owner's token ..."
printf '%s\n' "$pat" | "$RGT_BIN" auth login "$SERVER_URL" --token-stdin >&2

log ""
log "First owner created: $USERNAME ($DISPLAY_NAME)"
log "Personal access token (shown once, save it if you need it elsewhere):"
printf '%s\n' "$pat"
pat=""
log ""
log "The CLI is already signed in (credential stored in ~/.regent/config.toml)."
log "Connect a project with: rgt connect $SERVER_URL"
