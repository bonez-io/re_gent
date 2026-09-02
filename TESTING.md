# re_gent Testing Guide

Quick checks for the `rgt` CLI and agent hook integrations.

## Prerequisites

```bash
make install
```

Or keep the build isolated and point the examples at it:

```bash
go build -o rgt ./cmd/rgt
export RGT=/path/to/re_gent/rgt
```

## Basic CLI

```bash
tmp=$(mktemp -d)
cd "$tmp"
printf '\n\n' | "$RGT" init --agent both

"$RGT" status
"$RGT" sessions
```

Expected results:

- `.regent/` exists.
- `.claude/settings.json` contains `UserPromptSubmit`, `Stop`, and `PostToolBatch`.
- `.codex/config.toml` contains `SessionStart`, `UserPromptSubmit`, `PostToolUse`, and `Stop`.
- `.codex/config.toml` has `[features] hooks = true`.
- `rgt sessions` reports no sessions until an agent hook fires.

Codex may ask you to trust the project and hook commands the first time it loads the project-local config.

## Manual Claude Turn

This exercises the current Claude per-turn flow without starting Claude Code.

```bash
cd "$tmp"
echo 'hello' > hello.txt

printf '{"session_id":"claude-manual","cwd":"%s","prompt":"create hello.txt"}' "$PWD" \
  | "$RGT" message-hook user

cat > /tmp/claude-tool-batch.json <<EOF
{
  "session_id": "claude-manual",
  "cwd": "$PWD",
  "tool_calls": [
    {
      "tool_name": "Write",
      "tool_use_id": "tool_1",
      "tool_input": {"file_path":"hello.txt","content":"hello"},
      "tool_response": "ok"
    }
  ]
}
EOF
"$RGT" tool-batch-hook < /tmp/claude-tool-batch.json

printf '{"session_id":"claude-manual","cwd":"%s","last_assistant_message":"done"}' "$PWD" \
  | "$RGT" message-hook assistant

"$RGT" log --session claude_code--claude-manual
"$RGT" sessions
```

Expected result: one step is created for the turn, with `origin: claude_code`.

## Manual Codex Turn

This exercises the Codex hook adapter without starting Codex.

```bash
cd "$tmp"
echo 'codex' > codex.txt

printf '{"hook_event_name":"SessionStart","session_id":"codex-manual","cwd":"%s","model":"gpt-5.5"}' "$PWD" \
  | "$RGT" codex-hook

printf '{"hook_event_name":"UserPromptSubmit","session_id":"codex-manual","turn_id":"turn-1","cwd":"%s","prompt":"create codex.txt"}' "$PWD" \
  | "$RGT" codex-hook

cat > /tmp/codex-post-tool.json <<EOF
{
  "hook_event_name": "PostToolUse",
  "session_id": "codex-manual",
  "turn_id": "turn-1",
  "cwd": "$PWD",
  "tool_name": "Bash",
  "tool_use_id": "call_1",
  "tool_input": {"command":"printf codex > codex.txt"},
  "tool_response": "ok"
}
EOF
"$RGT" codex-hook < /tmp/codex-post-tool.json

printf '{"hook_event_name":"Stop","session_id":"codex-manual","turn_id":"turn-1","cwd":"%s","last_assistant_message":"done"}' "$PWD" \
  | "$RGT" codex-hook

"$RGT" log --session codex_cli--codex-manual --json
HASH=$("$RGT" log --session codex_cli--codex-manual --oneline | awk 'NR==1 {print $1}')
"$RGT" show "$HASH"
```

Expected result: one step is created for the turn, with `origin: codex_cli` and `turn_id: turn-1`.

## No-Tool Turn

```bash
printf '{"hook_event_name":"UserPromptSubmit","session_id":"codex-manual","turn_id":"turn-2","cwd":"%s","prompt":"say ok"}' "$PWD" \
  | "$RGT" codex-hook
printf '{"hook_event_name":"Stop","session_id":"codex-manual","turn_id":"turn-2","cwd":"%s","last_assistant_message":"ok"}' "$PWD" \
  | "$RGT" codex-hook

"$RGT" log --session codex_cli--codex-manual
```

Expected result: no new step is created, and the no-tool messages do not attach to later tool-using turns.

## Server Mode Under Induced Failures

Server mode's failure behaviour is covered by automated tests against an in-process server
(`internal/remotetest`) that can be told to go offline, inject faults, drop objects, or diverge.
Each row of the failure-mode table in [docs/server-mode.md](docs/server-mode.md) maps to a named
test there.

```bash
# Outage, partial write, divergence, server-side object loss, cache loss
go test ./internal/remote/... ./internal/capture/... ./internal/cli/... -v -run 'Offline|Outage|Cooldown|Diverged|Network|Repair|Hydrate|Pull|Spool'
```

To exercise it by hand against a real server:

```bash
export REGENT_SERVER_URL=https://regent.example.com
export REGENT_REPO_ID=my-project

rgt sync --status              # queued work — never touches the network
# stop the server, run an agent turn, then:
rgt sync --status              # shows the lag; the agent turn was not blocked
# start the server again:
rgt sync                       # drains; --status then reports clean
```

Note: because capture consults the ambient environment, `REGENT_SERVER_URL` must be empty when
running the local-mode suites. The `TestMain` guards in `internal/capture` and `cmd/rgt` do this
automatically, so `go test ./...` is hermetic even on a machine configured for server mode.

## Self-hosted local loop

Quick check of the native local dev loop described in
[docs/ui-development.md](docs/ui-development.md) — `regent-server` running
self-hosted auth natively (no Docker), bootstrap, CLI login, connect, a
captured turn, and sync, with the server as the read of record.

The scripted version, on a scratch port and temp directories:

```bash
make smoke
# or directly:
./scripts/dev-smoke.sh
```

It exits non-zero and names the failing step on any problem, and always stops
the server it started.

The same flow as a Go test, driving the real `rgt` binary against the
`selfhosted` package in-process (`httptest`, no port or Docker needed):

```bash
go test ./test/ -run TestSelfHostedDevLoop -count=1
```

By hand, in two terminals:

```bash
# terminal 1
make serve                          # regent-server on 127.0.0.1:7655, self-hosted auth

# terminal 2, once /healthz answers
./scripts/dev-bootstrap.sh          # claims the bootstrap token, creates the first
                                     # owner, and runs `rgt auth login` for you

cd /path/to/some/project
git init -q                          # or use an existing git repo
rgt connect http://127.0.0.1:7655 --as demo-project --agent claude

# Manual Claude turn (see "Manual Claude Turn" above), then:
rgt sync

curl -s http://127.0.0.1:7655/demo-project/api/sessions            # 401, anonymous
curl -s -H "Authorization: Bearer $PAT" \
  http://127.0.0.1:7655/demo-project/api/sessions                  # your session, with the
                                                                     # PAT dev-bootstrap.sh printed
```

`rgt auth login` never accepts a token as an argument or displays a stored
one; `scripts/dev-bootstrap.sh` reads the one-time bootstrap credential from
`${REGENT_DATA:-.local/data}/bootstrap-token` directly (mode `0600`, written by
`regent-server` on first start) rather than asking you to paste it, since this
is a single-host local loop rather than the browser-driven production
bootstrap in [docs/self-hosted.md](docs/self-hosted.md). It prints the
resulting personal access token once, on its own — save it (as `$PAT` above)
if you want to call the API directly; the CLI itself stays signed in via
`~/.regent/config.toml`.

## VPS bootstrap (manual before release)

Use a disposable modern Linux host reachable through your normal `ssh` configuration. From a
throwaway project, run `rgt connect user@host`, inspect the Docker/Compose plan, confirm it, and
verify `curl http://host:7654/healthz`. Re-run the same command and confirm that it reports the
healthy server and makes no remote changes. Also verify a firewall-blocked port leaves the project
without `.regent/`; use `--url` when the public address differs from the SSH hostname.

## Full Verification

```bash
go test ./...
```

Before opening a PR, also run any available lint or race checks used by the project.
