#!/usr/bin/env bash
#
# dev-smoke.sh — end-to-end check of the native local dev loop, on a free port
# and inside a scratch temp directory. Never touches ./bin, ./.local, the
# real ~/.regent/config.toml, or Docker.
#
# Flow, against the onboarding contract locked in
# docs/rfcs/0005-self-hosted-team-onboarding.md Appendix A: build
# regent-server + rgt -> start the server with a known REGENT_ADMIN_PASSWORD
# -> sign in as admin with it -> POST /api/v1/onboarding/admin (org
# "Local Dev Smoke") -> create a PAT through the existing PAT route ->
# `rgt auth login` -> `git init` a temp project -> `rgt connect` -> one
# manual Claude turn (per TESTING.md) -> `rgt sync` -> assert the project is
# listed via GET /api/v1/projects, the session is visible via the server API
# with a bearer token, and that an anonymous request is refused -> stop the
# server.
#
# S1 (the server side of RFC 0005) may not have landed the onboarding routes
# yet in this checkout. If POST /api/v1/auth/login or
# /api/v1/onboarding/admin answers 404, this script prints a clear message
# and exits 0 rather than failing, so `make smoke` stays usable for other
# streams while S1 is in flight.
#
# The server assigns its own opaque storage key (`prj_<hex>`) to an enrolled
# project; `--as` only supplies its display name. So the URL key used below is
# read back from the project's own .regent/config.toml after `rgt connect`
# (project_id, falling back to the legacy repo_id) rather than assumed to be
# the `--as` value.
#
# Exits non-zero on the first failing step, naming that step. `make smoke`
# runs this.

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT_DIR"

WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/regent-dev-smoke.XXXXXX")
BIN_DIR="$WORK_DIR/bin"
DATA_DIR="$WORK_DIR/data"
HOME_DIR="$WORK_DIR/home"
PROJECT_DIR="$WORK_DIR/project"
mkdir -p "$BIN_DIR" "$DATA_DIR" "$HOME_DIR" "$PROJECT_DIR"

DISPLAY_NAME="smoke-project"
SESSION_ID="dev-smoke-session"
SERVER_PID=""
CURRENT_STEP="starting up"

on_error() {
  local code=$?
  echo "" >&2
  echo "FAILED at step: $CURRENT_STEP" >&2
  if [ -f "$WORK_DIR/server.log" ]; then
    echo "--- last 40 lines of regent-server output ---" >&2
    tail -n 40 "$WORK_DIR/server.log" >&2 || true
  fi
  exit "$code"
}
trap on_error ERR

cleanup() {
  if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

step() {
  CURRENT_STEP="$1"
  echo "==> $1"
}

pick_free_port() {
  if command -v python3 >/dev/null 2>&1; then
    python3 - <<'PY'
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
    return
  fi
  local port
  for port in $(seq 20000 20200); do
    if ! (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
      echo "$port"
      return
    fi
    exec 3>&- 2>/dev/null || true
  done
  echo "no free port found between 20000 and 20200" >&2
  return 1
}

step "picking a free port"
PORT=$(pick_free_port)
SERVER_URL="http://127.0.0.1:$PORT"
echo "    using $SERVER_URL"

command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 1; }

skip() {
  echo ""
  echo "SKIPPED: $*"
  echo "(RFC 0005 stream S1 has not landed the onboarding routes yet in this checkout)"
  exit 0
}

# http_status POSTs (or GETs, with no -d/body args) and writes the response
# body to $BODY_FILE, printing just the status code -- lets the onboarding
# steps below tell "not implemented yet" (404) apart from a real failure
# without curl's -f aborting the script first.
BODY_FILE="$WORK_DIR/http-body.json"
http_status() {
  curl -sS -o "$BODY_FILE" -w '%{http_code}' "$@"
}

# head -c bounds /dev/urandom itself (rather than piping tr's output through
# a second `head -c`) so tr always hits a natural EOF -- under `set -o
# pipefail` above, a downstream `head -c N` closing the pipe early would
# SIGPIPE tr and fail the whole assignment.
ADMIN_PASSWORD="smoke-$(head -c 512 /dev/urandom | LC_ALL=C tr -dc 'A-Za-z0-9' | head -c 20)"

step "building regent-server and rgt"
go build -o "$BIN_DIR/regent-server" ./cmd/regent-server
go build -o "$BIN_DIR/rgt" ./cmd/rgt

step "starting regent-server (self-hosted auth, known REGENT_ADMIN_PASSWORD)"
REGENT_ADMIN_PASSWORD="$ADMIN_PASSWORD" "$BIN_DIR/regent-server" \
  --addr "127.0.0.1:$PORT" --data "$DATA_DIR" --auth-mode self-hosted \
  > "$WORK_DIR/server.log" 2>&1 &
SERVER_PID=$!

step "waiting for /healthz"
healthy=0
for _ in $(seq 1 60); do
  if curl -fsS -o /dev/null "$SERVER_URL/healthz" 2>/dev/null; then
    healthy=1
    break
  fi
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    echo "regent-server exited before becoming healthy" >&2
    break
  fi
  sleep 0.5
done
[ "$healthy" -eq 1 ]

step "checking capabilities reports an onboarding state"
capabilities=$(curl -fsS "$SERVER_URL/api/v1/capabilities")
onboarding=$(printf '%s' "$capabilities" | jq -r '.onboarding // empty')
[ -n "$onboarding" ] || skip "capabilities has no \"onboarding\" field: $capabilities"

step "signing in as admin"
COOKIE_JAR="$WORK_DIR/cookies.txt"
login_body=$(jq -nc --arg p "$ADMIN_PASSWORD" '{username:"admin",password:$p}')
status=$(http_status -c "$COOKIE_JAR" -X POST "$SERVER_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" -d "$login_body")
[ "$status" != "404" ] || skip "POST /api/v1/auth/login"
[ "$status" -ge 200 ] && [ "$status" -lt 300 ] || { echo "POST /api/v1/auth/login: status $status: $(cat "$BODY_FILE")" >&2; exit 1; }
csrf=$(jq -r '.csrf // .csrf_token // empty' "$BODY_FILE")
[ -n "$csrf" ] || { echo "login response did not include a csrf token: $(cat "$BODY_FILE")" >&2; exit 1; }

step "completing the wizard's first screen"
onboarding_body=$(jq -nc \
  '{org:{display_name:"Local Dev Smoke",slug:"local-dev-smoke",server_url:$url,join_policy:"invite_only",default_role:"reader"},
    admin:{username:"admin",display_name:"Smoke Test",new_password:$pass}}' \
  --arg url "$SERVER_URL" --arg pass "smoke-new-$(head -c 512 /dev/urandom | LC_ALL=C tr -dc 'A-Za-z0-9' | head -c 20)")
status=$(http_status -b "$COOKIE_JAR" -c "$COOKIE_JAR" -X POST "$SERVER_URL/api/v1/onboarding/admin" \
  -H "Content-Type: application/json" -H "X-Regent-CSRF: $csrf" -d "$onboarding_body")
[ "$status" != "404" ] || skip "POST /api/v1/onboarding/admin"
[ "$status" -ge 200 ] && [ "$status" -lt 300 ] || { echo "POST /api/v1/onboarding/admin: status $status: $(cat "$BODY_FILE")" >&2; exit 1; }
new_csrf=$(jq -r '.csrf // .csrf_token // empty' "$BODY_FILE")
[ -n "$new_csrf" ] || { echo "onboarding response did not include a csrf token: $(cat "$BODY_FILE")" >&2; exit 1; }
csrf="$new_csrf"

step "creating a personal access token"
status=$(http_status -b "$COOKIE_JAR" -c "$COOKIE_JAR" -X POST "$SERVER_URL/api/v1/auth/tokens" \
  -H "Content-Type: application/json" -H "X-Regent-CSRF: $csrf" \
  -d '{"name":"dev-smoke","expires_in_days":1}')
[ "$status" -eq 201 ] || { echo "POST /api/v1/auth/tokens: status $status: $(cat "$BODY_FILE")" >&2; exit 1; }
PAT=$(jq -r '.secret // empty' "$BODY_FILE")
[ -n "$PAT" ]

step "rgt auth login"
printf '%s\n' "$PAT" | HOME="$HOME_DIR" "$BIN_DIR/rgt" auth login "$SERVER_URL" --token-stdin >/dev/null

step "git init temp project"
git -C "$PROJECT_DIR" init -q
git -C "$PROJECT_DIR" config user.email "smoke@example.com"
git -C "$PROJECT_DIR" config user.name "Smoke Test"

step "rgt connect"
(cd "$PROJECT_DIR" && HOME="$HOME_DIR" "$BIN_DIR/rgt" connect "$SERVER_URL" --as "$DISPLAY_NAME" --agent claude) >/dev/null

step "reading the enrolled project's storage key from .regent/config.toml"
config_file="$PROJECT_DIR/.regent/config.toml"
[ -f "$config_file" ] || { echo "connect did not write $config_file" >&2; exit 1; }
# project_id is the server-generated key (RFC 0004); repo_id is the legacy
# client-derived one written by a server without the project API. Either can
# be single- or double-quoted depending on the TOML encoder.
PROJECT_KEY=$(sed -n "s/^project_id[[:space:]]*=[[:space:]]*['\"]\\([^'\"]*\\)['\"].*/\\1/p" "$config_file" | head -1)
if [ -z "$PROJECT_KEY" ]; then
  PROJECT_KEY=$(sed -n "s/^repo_id[[:space:]]*=[[:space:]]*['\"]\\([^'\"]*\\)['\"].*/\\1/p" "$config_file" | head -1)
fi
[ -n "$PROJECT_KEY" ] || { echo "no project_id or repo_id found in $config_file" >&2; exit 1; }
echo "    project key: $PROJECT_KEY"

step "manual Claude turn (message-hook user)"
printf '{"session_id":"%s","cwd":"%s","prompt":"create hello.txt"}' "$SESSION_ID" "$PROJECT_DIR" \
  | HOME="$HOME_DIR" "$BIN_DIR/rgt" message-hook user >/dev/null

echo 'hello' > "$PROJECT_DIR/hello.txt"

step "manual Claude turn (tool-batch-hook)"
tool_batch_payload=$(cat <<EOF
{
  "session_id": "$SESSION_ID",
  "cwd": "$PROJECT_DIR",
  "tool_calls": [
    {
      "tool_name": "Write",
      "tool_use_id": "smoke_tool_1",
      "tool_input": {"file_path":"hello.txt","content":"hello"},
      "tool_response": "ok"
    }
  ]
}
EOF
)
printf '%s' "$tool_batch_payload" | HOME="$HOME_DIR" "$BIN_DIR/rgt" tool-batch-hook >/dev/null

step "manual Claude turn (message-hook assistant)"
printf '{"session_id":"%s","cwd":"%s","last_assistant_message":"done"}' "$SESSION_ID" "$PROJECT_DIR" \
  | HOME="$HOME_DIR" "$BIN_DIR/rgt" message-hook assistant >/dev/null

step "rgt sync"
(cd "$PROJECT_DIR" && HOME="$HOME_DIR" "$BIN_DIR/rgt" sync) >/dev/null

step "asserting GET /api/v1/projects lists the enrolled project"
projects_response=$(curl -fsS -H "Authorization: Bearer $PAT" "$SERVER_URL/api/v1/projects")
# id is immediately followed by display_name in the server's fixed field
# order (see internal/remote.Project / projectJSON), so this need not span
# object boundaries.
case "$projects_response" in
  *"\"id\":\"$PROJECT_KEY\",\"display_name\":\"$DISPLAY_NAME\""*) : ;;
  *) echo "GET /api/v1/projects did not list $PROJECT_KEY with display_name $DISPLAY_NAME: $projects_response" >&2; exit 1 ;;
esac

step "asserting the session appears via the API with a bearer token"
sessions_response=$(curl -fsS -H "Authorization: Bearer $PAT" "$SERVER_URL/$PROJECT_KEY/api/sessions")
case "$sessions_response" in
  *"$SESSION_ID"*) : ;;
  *) echo "server session listing did not contain $SESSION_ID: $sessions_response" >&2; exit 1 ;;
esac

step "asserting an anonymous request is refused"
anon_status=$(curl -s -o /dev/null -w '%{http_code}' "$SERVER_URL/$PROJECT_KEY/api/sessions")
if [ "$anon_status" != "401" ]; then
  echo "anonymous GET $SERVER_URL/$PROJECT_KEY/api/sessions returned $anon_status, want 401" >&2
  exit 1
fi

step "stopping the server"
kill "$SERVER_PID"
wait "$SERVER_PID" 2>/dev/null || true
SERVER_PID=""

echo ""
echo "dev-smoke: all checks passed."
