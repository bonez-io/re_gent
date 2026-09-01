# re_gent team onboarding kit

A copy-into-your-repo template that gets a teammate capturing agent activity to
your **self-hosted re_gent server** with either **zero steps** (devcontainer) or
**one paste** (installer).

## Run your server open — no token, no secrets

The frictionless default: run your `rgt serve` instance **open (no auth)** on a
**private network** — a VPN, Tailscale, or a trusted LAN. There is no token to
generate, distribute, or rotate, and nothing secret ever lands in a teammate's
environment. Onboarding is genuinely zero-config: clone (or open the
devcontainer), have `rgt` installed, and go.

A token is **optional** and only needed if your server is **publicly reachable**
(see [Optional: public server](#optional-public-server) below).

## What this kit is

re_gent captures what your AI agents do via **agent hooks**. Normally each person
runs `rgt connect` / `rgt init` to wire that up. This kit lets you do the wiring
**once, in the repo**, and commit it — so every clone is pre-wired. A teammate
only needs `rgt` installed (and, for a public server, the team token in their
environment).

## Attribution — no login needed

This targets a **small, trusted team** self-hosting **one** re_gent server. There
is no per-user login. "Who did what" comes for free: every step records the git
author (name + email), so the timeline attributes work per person without any
login system. Make sure teammates have their git identity set
(`git config user.name` / `user.email`).

Per-user auth (OAuth/SSO) is only needed for an untrusted/public deployment —
out of scope here.

## Exactly what to commit into your repo

Copy the contents of [`project-template/`](./project-template) into the root of
your project and commit:

```
.regent/config.toml        # [remote] url + repo_id — points clones at the server
.regent/.gitignore         # keeps local-mode object store / index out of git
.claude/settings.json      # the Claude Code hooks that trigger capture
.devcontainer/             # zero-step path (optional but recommended)
```

Before committing, edit `.regent/config.toml` and replace the two placeholders:

```toml
[remote]
url = "http://YOUR-TEAM-SERVER:7654"   # your `rgt serve` base URL
repo_id = "YOUR-REPO-ID"               # id this repo is registered under
```

None of these files contain a token. `.regent/config.toml` never has one. They
are safe to commit.

### How capture is triggered (`.claude/settings.json`)

The committed hooks are exactly what `rgt`'s own installer writes
(`internal/cli/init.go` `installClaudeHook`):

| Claude Code event | Command |
|-------------------|---------|
| `UserPromptSubmit` | `rgt message-hook user` |
| `Stop` | `rgt message-hook assistant` |
| `PostToolBatch` | `rgt tool-batch-hook` |

When Claude Code loads project-local `.claude/settings.json`, these fire on every
prompt / turn / tool batch and run `rgt`, which reads `.regent/config.toml`, sees
the `[remote]` block, and streams the step to your server.

> Codex users: `rgt init --agent codex` writes the equivalent `.codex/config.toml`
> hooks (`SessionStart`/`UserPromptSubmit`/`PostToolUse`/`Stop` → `rgt codex-hook`).
> This kit ships the Claude Code config; add the Codex file the same way if needed.

## The two onboarding paths

### 1) Devcontainer — zero steps ("open the repo = done")

Best for teams on VS Code Dev Containers / GitHub Codespaces / a shared dev box.

- `.devcontainer/devcontainer.json` installs `rgt` on create.
- With an **open server** (the default) there is nothing else to configure —
  opening the repo in the container is the whole setup.
- Teammate action: open the repo in the container (which they already do). Done.

### 2) One-line installer — one paste (plain local laptops)

Your running `rgt serve` instance **hosts the installer for you** at `GET /install`
(implemented in `internal/server/install.go`). Just share:

```sh
curl -fsSL http://YOUR-TEAM-SERVER/install | sh
```

**No Go toolchain required.** The server personalizes the script with its own
address, and the script:

1. Downloads the `rgt` binary from `<server>/bin/rgt` (the server's own running
   executable) into `~/.local/bin` — or `/usr/local/bin` if writable — then
   `chmod +x` and verifies it runs. This is dependency-free for teammates on the
   **same OS/arch as the server** (a Linux server + Linux devcontainers, or a
   macOS team + macOS host — the common case).
2. **Falls back** to `go install github.com/bonez-io/re_gent/cmd/rgt@latest`
   only if that binary can't exec here (OS/arch mismatch) and Go is present;
   otherwise it prints a clear manual instruction.
3. Detects whether **this server** is open or token-protected and prints the
   right final step. For an **open server** there is no token step — it just
   confirms you're wired up. For a token server it prints the `export
   REGENT_TOKEN=…` step.

Both `GET /install` and `GET /bin/rgt` are **unauthenticated** (like `/healthz`):
they expose the open-source tool and a token-free script, never repo data.

> **OS/arch caveat:** `/bin/rgt` serves the server's *own* binary, so it only
> runs directly on teammates matching the server's OS/arch. Cross-platform
> teammates hit the `go install` fallback (needs Go). For a fully binary-only
> cross-platform rollout, publish per-platform release binaries later.

It writes no config — the repo's committed `.regent/config.toml` already handles
the server wiring. If a teammate isn't inside a pre-wired repo, the script tells
them to run `rgt connect http://YOUR-TEAM-SERVER` (add `--token $REGENT_TOKEN`
only if the server requires one). It is idempotent and safe to re-run.

The standalone [`install.sh`](./install.sh) in this directory is a **source-only
fallback** (installs via `go install`, needs Go) for when you are not serving
from a live instance. Prefer the server-hosted `/install` above.

## Optional: public server

Everything above assumes the frictionless default — an **open server on a
private network**. Only if your server is **publicly reachable** should you turn
on the shared bearer token.

Auth is a **single shared token** (like a shared DB password): set it on the
server, and every teammate's client sends the same value. "Who did what" is still
the git author, exactly as above — the token only gates access, it is not a
login.

- The token is a **secret**. It is **never committed** to `.regent/config.toml`.
  It is supplied per teammate via the environment or a Codespaces secret.

### Token env var — read this once (it is easy to get wrong)

| Side | Process | Env var it reads |
|------|---------|------------------|
| **Server** | `rgt serve` | `REGENT_SERVER_TOKEN` |
| **Client** | `rgt` hooks on a teammate's machine | `REGENT_TOKEN` |

Same shared secret **value**, two different variable names depending on which
side you are on. Teammates run the client, so on their machines the token goes in
**`REGENT_TOKEN`**. (Verified in `internal/remote/config.go` `applyEnv` and
`internal/cli/serve.go`.)

Start the server with a token:

```sh
REGENT_SERVER_TOKEN=$(openssl rand -hex 32) rgt serve --addr 0.0.0.0:7654
# or, via Docker:
REGENT_SERVER_TOKEN=$(openssl rand -hex 32) make server
```

### Deliver the token to teammates

- **Devcontainer / Codespaces:** deliver it as a **Codespaces secret named
  `REGENT_TOKEN`** (Repo or Org Settings → Secrets → Codespaces). Codespaces
  injects it automatically; for a local Dev Container uncomment the
  `containerEnv` block in `.devcontainer/devcontainer.json` so it forwards from
  your host env via `"REGENT_TOKEN": "${localEnv:REGENT_TOKEN}"`.
- **Plain laptops:** the server-hosted `/install` script detects the token
  requirement and prints the `export REGENT_TOKEN=<team-token>` step. Add that
  line to the shell profile to persist it.

## Prerequisites

- `rgt` installed (both paths handle this).
- A reachable `rgt serve` instance at the `url` in `.regent/config.toml`.
- Git author identity configured for correct attribution.
- The team token in `REGENT_TOKEN` — **only if** the server is token-protected
  (public). Not needed for the default open/private-network setup.
