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

## Searchable Sessions (insight)

This exercises RFC 0007 end to end with a local model, so it needs no key.
It assumes [Ollama](https://ollama.com) is running with a chat-capable model
(any instruct model works; `qwen2.5:3b` or larger reads better) and,
optionally, an embedding model such as `nomic-embed-text`.

Insight is off by default and has two halves: the repository switch in
`.regent/config.toml` (committed) and your provider in `~/.regent/config.toml`
(private). Use a scratch `HOME` so your real config is untouched:

```bash
export HOME=$(mktemp -d)
mkdir -p "$HOME/.regent"
cat > "$HOME/.regent/config.toml" <<'EOF'
[insight.model]
provider = "openai-compatible"
model = "qwen2.5:3b"
base_url = "http://localhost:11434/v1"

[insight.embedding]            # optional; without it search is full-text only
provider = "openai-compatible"
model = "nomic-embed-text"
base_url = "http://localhost:11434/v1"
EOF

cd "$tmp"
"$RGT" insight enable          # writes [insight] enabled = true, indexes existing messages
"$RGT" insight status          # "Insight: on", provider named, queue empty
```

Record a turn the way the Claude hooks do (the user prompt, a tool batch as
in Manual Claude Turn so the turn has a step and a changed file, then the
assistant message), and watch the worker the Stop hook spawned:

```bash
printf '{"session_id":"claude-manual","turn_id":"t1","cwd":"%s","prompt":"add exponential backoff to Retry in queue.go for https://github.com/acme/demo/issues/42"}' "$PWD" \
  | "$RGT" message-hook user
printf '{"session_id":"claude-manual","turn_id":"t1","cwd":"%s","last_assistant_message":"Done: Retry now backs off 100ms, 200ms, 400ms."}' "$PWD" \
  | "$RGT" message-hook assistant

until [ ! -f .regent/insight.lock ]; do sleep 1; done   # the detached worker released its lock
cat .regent/log/insight.log                              # one line per work item read, or the failure
"$RGT" insight status                                    # "1 done" in the queue, 1 work item read
"$RGT" work list
"$RGT" work show <id>                                    # goal, approach, outcome, entities with evidence, files
"$RGT" search "backoff"                                  # the item, matched by text and (with embeddings) meaning
"$RGT" search --entity acme/demo#42                      # the issue URL became an entity with no model involved
"$RGT" search --file queue.go
```

Expected result: one work item for the session, `status` set by the model,
the issue URL present as an `issue` entity with source `deterministic`, the
edited file under "Files changed" (only when the turn had a tool batch: a
turn with no tools has no step, so no files), and every model-added entity
carrying an evidence step you can `rgt show`. If the embedding endpoint is
not available, the log says so, the work items are stored without vectors,
and `rgt insight status` reports how many are unembedded; search is then
full-text only.

Worth checking on purpose:

- A second turn on a different topic produces a second item and closes the
  first (`rgt work list` shows both; the first is no longer "open").
- `rgt insight run` while the detached worker is running says so and exits.
- `rgt insight rebuild` then `rgt insight run` reads every session again
  from its first message and replaces the items.
- With no `[insight.model]` in your config, `rgt insight status` says
  "idle" and the hooks queue nothing.
- With a `[insight.scrub] patterns = ["acme"]` line in `.regent/config.toml`,
  the request the model sees has `[REDACTED:pattern]` where the text was
  (check by pointing `command` at `tee`).

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
self-hosted auth natively (no Docker), onboarding, CLI login, connect, a
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
./scripts/dev-bootstrap.sh          # signs in as admin, completes the wizard's first
                                     # screen, creates a PAT, and runs `rgt auth login`
                                     # for you

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
one; `scripts/dev-bootstrap.sh` drives the onboarding wizard over its API
(RFC 0005 Appendix A) rather than asking you to paste anything by hand, since
this is a single-host local loop rather than the browser-driven wizard in
[docs/self-hosted.md](docs/self-hosted.md). It saves the admin password and
the resulting personal access token under `.local/` (mode `0600`) — the PAT
is also printed once, on its own; save it (as `$PAT` above) if you want to
call the API directly, though the CLI itself stays signed in via
`~/.regent/config.toml` regardless.

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
