#!/usr/bin/env bash
#
# dev-bootstrap.sh — drive the local dev server through the onboarding wizard
# (docs/rfcs/0005-self-hosted-team-onboarding.md) and sign the CLI in, in one
# step.
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
# Flow, against the API contract locked in RFC 0005 Appendix A:
#
#   1. Resolve the admin password: reuse $LOCAL_DIR/admin-password.txt if a
#      previous run already saved one, else $REGENT_ADMIN_PASSWORD if set,
#      else generate one. Saved with mode 0600.
#   2. Wait for $SERVER_URL/healthz.
#   3. GET /api/v1/capabilities and read its "onboarding" field.
#   4. POST /api/v1/auth/login as admin with that password. This only
#      succeeds if the server was actually started with the same password
#      (REGENT_ADMIN_PASSWORD in its own environment, or a value it read from
#      $LOCAL_DIR/admin-password.txt the way `make serve`/`make dev` do —
#      see the Makefile). A fresh server that generated its own random
#      password independently will refuse this login; the error message
#      below explains the fix.
#   5. When onboarding is still "admin_password", POST
#      /api/v1/onboarding/admin to complete the wizard's first screen (org
#      "Local Dev", invited-only, default role reader) and replace the
#      password; save the new one, overwriting the file from step 1. This
#      call is one-time and atomic, so a server already past this state
#      skips straight to step 6 with the password already on disk.
#   6. Create a personal access token through the existing PAT route
#      (POST /api/v1/auth/tokens) and save it to $LOCAL_DIR/owner-pat.txt,
#      mode 0600.
#   7. `rgt auth login --token-stdin` with that token.
#
# Idempotent: a server whose onboarding is already "done" and a saved PAT on
# disk means a previous run already finished; this script exits 0 without
# doing anything. A server that only got partway (e.g. onboarding done but no
# saved PAT yet) resumes from wherever it left off.
#
# Session mutations need the CSRF header every unsafe request under a browser
# session requires (X-Regent-CSRF, matching csrfHeaderName in
# selfhosted/server.go) alongside the __Host- session cookie a curl cookie
# jar carries automatically between calls.
#
# Needs curl and jq (both used to build/parse JSON without shelling out to
# anything fancier).

set -eu

SERVER_URL=${REGENT_SERVER_URL:-http://127.0.0.1:${REGENT_PORT:-7655}}
LOCAL_DIR=${REGENT_LOCAL_DIR:-.local}
ADMIN_PASSWORD_FILE="$LOCAL_DIR/admin-password.txt"
PAT_FILE="$LOCAL_DIR/owner-pat.txt"

ORG_NAME=${REGENT_DEV_ORG_NAME:-"Local Dev"}
ORG_SLUG=${REGENT_DEV_ORG_SLUG:-local-dev}
ADMIN_USERNAME=admin
ADMIN_DISPLAY_NAME=${REGENT_DEV_DISPLAY_NAME:-Admin}

RGT_BIN=${RGT:-}
if [ -z "$RGT_BIN" ]; then
  if [ -x ./bin/rgt ]; then
    RGT_BIN=./bin/rgt
  else
    RGT_BIN=rgt
  fi
fi

log() { printf '%s\n' "$*" >&2; }

fail() {
  log "dev-bootstrap: $*"
  exit 1
}

gen_password() {
  # 20 random alphanumeric characters, matching the length of the server's
  # own random initial password (RFC 0005) and clearing the wizard's
  # 12-character minimum with room to spare. head -c bounds /dev/urandom
  # itself (rather than piping tr's output through a second `head -c`) so tr
  # always hits a natural EOF instead of a SIGPIPE from a downstream head
  # closing the pipe early.
  head -c 512 /dev/urandom | LC_ALL=C tr -dc 'A-Za-z0-9' | head -c 20
}

save_secret() {
  # save_secret FILE CONTENT — writes CONTENT to FILE with mode 0600, without
  # ever creating FILE world- or group-readable in between.
  file=$1
  content=$2
  ( umask 077 && printf '%s' "$content" > "$file" )
  chmod 0600 "$file"
}

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"
command -v "$RGT_BIN" >/dev/null 2>&1 || fail "rgt binary not found (looked for \$RGT, ./bin/rgt, and rgt on PATH); run 'make serve' first or set \$RGT"

mkdir -p "$LOCAL_DIR"
COOKIE_JAR=$(mktemp "${TMPDIR:-/tmp}/regent-dev-bootstrap-cookies.XXXXXX")
trap 'rm -f "$COOKIE_JAR"' EXIT

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
  fail "server at $SERVER_URL never answered /healthz; is 'make serve' (or 'make dev') running?"
fi
log "dev-bootstrap: server is up"

capabilities=$(curl -fsS "$SERVER_URL/api/v1/capabilities") || fail "GET $SERVER_URL/api/v1/capabilities failed"
onboarding=$(printf '%s' "$capabilities" | jq -r '.onboarding // empty')
if [ -z "$onboarding" ]; then
  fail "capabilities has no \"onboarding\" field ($capabilities); this server predates the onboarding wizard (RFC 0005 stream S1) -- rebuild regent-server against a version that implements it"
fi

if [ "$onboarding" = "done" ] && [ -f "$PAT_FILE" ]; then
  log "dev-bootstrap: onboarding is done and $PAT_FILE already exists; nothing to do."
  cat "$PAT_FILE"
  echo ""
  exit 0
fi

# Resolve the admin password to sign in with. Once onboarding has moved past
# admin_password, POST /api/v1/onboarding/admin is one-time and must not be
# retried, so the only password that still works is whatever a previous run
# already saved.
if [ -f "$ADMIN_PASSWORD_FILE" ]; then
  admin_password=$(cat "$ADMIN_PASSWORD_FILE")
elif [ -n "${REGENT_ADMIN_PASSWORD:-}" ]; then
  admin_password=$REGENT_ADMIN_PASSWORD
elif [ "$onboarding" = "admin_password" ]; then
  admin_password=$(gen_password)
else
  fail "onboarding is \"$onboarding\" (past admin_password) but no saved password was found at $ADMIN_PASSWORD_FILE; sign in by hand and save a PAT under Settings, or wipe the data directory and start over"
fi
[ -n "$admin_password" ] || fail "$ADMIN_PASSWORD_FILE is empty"
save_secret "$ADMIN_PASSWORD_FILE" "$admin_password"

log "dev-bootstrap: signing in as $ADMIN_USERNAME ..."
login_body=$(jq -nc --arg u "$ADMIN_USERNAME" --arg p "$admin_password" '{username:$u,password:$p}')
login_response=$(curl -fsS -c "$COOKIE_JAR" -X POST "$SERVER_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" -d "$login_body") \
  || fail "POST $SERVER_URL/api/v1/auth/login failed -- if the server is already running with a *different* initial password (its own randomly generated one, printed to its terminal on first start), either copy that password into $ADMIN_PASSWORD_FILE and re-run, or restart the server with REGENT_ADMIN_PASSWORD set from this file so they match"
csrf=$(printf '%s' "$login_response" | jq -r '.csrf // .csrf_token // empty')
[ -n "$csrf" ] || fail "login response did not include a csrf token: $login_response"

if [ "$onboarding" = "admin_password" ]; then
  log "dev-bootstrap: completing the wizard's first screen (org \"$ORG_NAME\") ..."
  new_password=$(gen_password)
  onboarding_body=$(jq -nc \
    --arg name "$ORG_NAME" --arg slug "$ORG_SLUG" --arg url "$SERVER_URL" \
    --arg user "$ADMIN_USERNAME" --arg display "$ADMIN_DISPLAY_NAME" --arg pass "$new_password" \
    '{org:{display_name:$name,slug:$slug,server_url:$url,join_policy:"invite_only",default_role:"reader"},
      admin:{username:$user,display_name:$display,new_password:$pass}}')
  onboarding_response=$(curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" -X POST "$SERVER_URL/api/v1/onboarding/admin" \
    -H "Content-Type: application/json" -H "X-Regent-CSRF: $csrf" -d "$onboarding_body") \
    || fail "POST $SERVER_URL/api/v1/onboarding/admin failed"
  new_csrf=$(printf '%s' "$onboarding_response" | jq -r '.csrf // .csrf_token // empty')
  [ -n "$new_csrf" ] || fail "onboarding response did not include a csrf token: $onboarding_response"
  csrf=$new_csrf
  save_secret "$ADMIN_PASSWORD_FILE" "$new_password"
  new_password=""
  log "dev-bootstrap: organization \"$ORG_NAME\" ($ORG_SLUG) created; admin password replaced and saved to $ADMIN_PASSWORD_FILE"
else
  log "dev-bootstrap: onboarding is already \"$onboarding\" (past admin_password); skipping the wizard's first screen"
fi
admin_password=""

log "dev-bootstrap: creating a personal access token ..."
token_response=$(curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" -X POST "$SERVER_URL/api/v1/auth/tokens" \
  -H "Content-Type: application/json" -H "X-Regent-CSRF: $csrf" \
  -d '{"name":"dev-bootstrap","expires_in_days":365}') \
  || fail "POST $SERVER_URL/api/v1/auth/tokens failed"
pat=$(printf '%s' "$token_response" | jq -r '.secret // empty')
[ -n "$pat" ] || fail "token response did not include a secret: $token_response"
save_secret "$PAT_FILE" "$pat"

log "dev-bootstrap: signing the CLI in ..."
printf '%s\n' "$pat" | "$RGT_BIN" auth login "$SERVER_URL" --token-stdin >&2

log ""
log "\"$ORG_NAME\" is ready at $SERVER_URL"
log "Admin password saved to $ADMIN_PASSWORD_FILE (mode 0600)."
log "Owner personal access token saved to $PAT_FILE (mode 0600); printed once below on its"
log "own line (stdout) if you want it in a variable, e.g. PAT=\$(./scripts/dev-bootstrap.sh)."
log "The CLI is signed in (credential stored in ~/.regent/config.toml)."
log "Connect a project with: rgt connect $SERVER_URL"
printf '%s\n' "$pat"
pat=""
