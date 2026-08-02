# re_gent team onboarding kit

A copy-into-your-repo template that gets a teammate capturing agent activity to
your **self-hosted re_gent server** with either **zero steps** (devcontainer) or
**one paste** (installer).

## What this kit is

re_gent captures what your AI agents do via **agent hooks**. Normally each person
runs `rgt connect` / `rgt init` to wire that up. This kit lets you do the wiring
**once, in the repo**, and commit it — so every clone is pre-wired. A teammate
only needs `rgt` installed and the team token in their environment.

## The trust / token model

This targets a **small, trusted team** self-hosting **one** re_gent server. There
is no per-user login. Auth is a **single shared token** (like a shared DB
password). "Who did what" comes for free: every step records the git author
(name + email), so the timeline attributes work per person without any login
system.

- The token is a **secret**. It is **never committed**. It is supplied per
  teammate via the environment or a Codespaces secret.
- Per-user auth (OAuth/SSO) is only needed for an untrusted/public deployment —
  out of scope here.

### Token env var — read this once (it is easy to get wrong)

| Side | Process | Env var it reads |
|------|---------|------------------|
| **Server** | `rgt serve` | `REGENT_SERVER_TOKEN` |
| **Client** | `rgt` hooks on a teammate's machine | `REGENT_TOKEN` |

Same shared secret **value**, two different variable names depending on which
side you are on. Teammates run the client, so on their machines the token goes in
**`REGENT_TOKEN`**. (Verified in `internal/remote/config.go` `applyEnv` and
`internal/cli/serve.go`.)

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

None of these files contain the token. They are safe to commit.

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

- `.devcontainer/devcontainer.json` installs `rgt` on create and forwards the
  token.
- Deliver the token as a **Codespaces secret named `REGENT_TOKEN`** (Repo or Org
  Settings → Secrets → Codespaces). Codespaces injects it automatically; for a
  local Dev Container it is forwarded from your host env via
  `"containerEnv": { "REGENT_TOKEN": "${localEnv:REGENT_TOKEN}" }`.
- Teammate action: open the repo in the container (which they already do). Done.

### 2) One-line installer — one paste (plain local laptops)

Host [`install.sh`](./install.sh) on your team server (the planned `GET /install`
endpoint) and share:

```sh
curl -fsSL https://YOUR-TEAM-SERVER/install | sh
```

It installs `rgt`, verifies it is on PATH, and prints the final step:

```sh
export REGENT_TOKEN=<the-team-token>
```

It writes no config — the repo's committed `.regent/config.toml` already handles
the server wiring. It is idempotent and safe to re-run.

## Attribution

No login means no user table. Attribution is the **git author** already
configured on each machine (`git config user.name` / `user.email`), which re_gent
records on every step. Make sure teammates have their git identity set.

## Prerequisites

- `rgt` installed (both paths handle this).
- The team token in `REGENT_TOKEN`.
- A reachable `rgt serve` instance at the `url` in `.regent/config.toml`.
- Git author identity configured for correct attribution.
