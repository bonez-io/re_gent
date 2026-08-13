# Testing the onboarding kit locally

A maintainer can dry-run the whole kit on one machine: run a server, wire a test
repo from the template, do an agent turn, and confirm the step shows up.

## 0. Build rgt

```sh
cd /path/to/re_gent_headless
go build -o /tmp/rgt ./cmd/rgt
export PATH="/tmp:$PATH"   # so `rgt` resolves to the build under test
```

## 1. Start a server (open — the default)

The default is OPEN (no auth), which is how you'd run it on a private
network/VPN. No token to set:

```sh
rgt serve --addr 127.0.0.1:7654 &
curl -fsS http://127.0.0.1:7654/healthz && echo " <- server up"
```

> Optional — testing a public/token-protected server instead? Export a token
> before starting: `export REGENT_SERVER_TOKEN=test-team-token-123` (the server
> reads this). Then add `-H "Authorization: Bearer $REGENT_SERVER_TOKEN"` to the
> curl calls below, and set the matching client token in step 3.

## 2. Make a test repo from the template

```sh
mkdir /tmp/regent-kit-test && cd /tmp/regent-kit-test
git init -q && git config user.name "Test Dev" && git config user.email "test@example.com"

# Copy the committed template files in:
cp -R /path/to/re_gent_headless/examples/team-onboarding/project-template/. .

# Point the [remote] block at the local server + register the repo id:
#   edit .regent/config.toml ->
#     url = "http://127.0.0.1:7654"
#     repo_id = "regent-kit-test"
```

Register the repo id with the server (or let the first push create it, depending
on your server build). On an open server no auth header is needed:

```sh
curl -fsS -X POST http://127.0.0.1:7654/repos \
  -H "Content-Type: application/json" \
  -d '{"repo_id":"regent-kit-test"}'
```

## 3. Set the client token (only for a token-protected server)

On the default open server there is nothing to set — skip to step 4. Only if you
started the server with `REGENT_SERVER_TOKEN` in step 1:

```sh
export REGENT_TOKEN=test-team-token-123   # CLIENT reads this (same value)
```

## 4. Run an agent turn

Open this directory in Claude Code and send a prompt that makes a small edit. The
committed `.claude/settings.json` fires `rgt message-hook` / `rgt tool-batch-hook`
on the events, which stream steps to the server.

No agent handy? Simulate the config resolution the hooks use:

```sh
cd /tmp/regent-kit-test
rgt status 2>/dev/null || rgt log   # should resolve server mode from .regent/config.toml
```

## 5. See it in the viewer

Point the regent-viewer at the server (server-client mode) and open the repo, or
hit the read API directly:

```sh
curl -fsS http://127.0.0.1:7654/regent-kit-test/api/sessions
# token server: add -H "Authorization: Bearer $REGENT_SERVER_TOKEN"
```

You should see the session, grouped by git author, with the step you just made.

## 6. Teardown

```sh
kill %1 2>/dev/null    # stop rgt serve
rm -rf /tmp/regent-kit-test
```

## Devcontainer smoke test

Open `project-template/` (copied into a repo) in VS Code → "Reopen in Container".
`postCreateCommand` runs `go install .../rgt@latest && rgt --version`. On the
default open server that is the whole setup. Only for a token-protected server:
uncomment the `containerEnv` block in `devcontainer.json` and set a local
`REGENT_TOKEN` in your host env first so `${localEnv:REGENT_TOKEN}` forwards it.
