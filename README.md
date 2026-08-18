<div align="center">

  <a href="https://github.com/regent-vcs/regent">
    <img
      src="assets/regent-logo-dark.png"
      alt="re_gent"
      width="100%"
    />
  </a>
  <br />
  <br />
  <h1>Version Control for AI Agents</h1>
  <p>
    Track what your agent did, which prompt wrote each line, and inspect any step.
  </p>

[![Star on GitHub](https://img.shields.io/github/stars/regent-vcs/regent?style=for-the-badge&logo=github&color=gold)](https://github.com/regent-vcs/regent)
[![Apache 2.0 License](https://img.shields.io/badge/License-Apache%202.0-blue?style=for-the-badge)](LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/regent-vcs/regent?style=for-the-badge&logo=go&logoColor=white&color=00ADD8)](go.mod)

[![CI Status](https://img.shields.io/github/actions/workflow/status/regent-vcs/regent/ci.yml?style=for-the-badge&logo=githubactions&logoColor=white)](https://github.com/regent-vcs/regent/actions/workflows/ci.yml)
[![Coverage](https://img.shields.io/codecov/c/github/regent-vcs/regent?style=for-the-badge&logo=codecov&logoColor=white)](https://codecov.io/gh/regent-vcs/regent)
[![Contributions Welcome](https://img.shields.io/badge/Contributions-Welcome-10b981?style=for-the-badge&logo=github)](CONTRIBUTING.md)
[![Claude Code Compatible](https://img.shields.io/badge/Claude%20Code-Compatible-6366f1?style=for-the-badge&logo=anthropic&logoColor=white)](https://github.com/regent-vcs/regent) [![Codex Compatible](https://img.shields.io/badge/Codex-Compatible-10b981?style=for-the-badge&logo=openai&logoColor=white)](https://github.com/regent-vcs/regent) [![OpenCode Compatible](https://img.shields.io/badge/OpenCode-Compatible-ff6b35?style=for-the-badge)](https://github.com/regent-vcs/regent)
[![Discord](https://img.shields.io/discord/1503732569622053004?style=for-the-badge&logo=discord&logoColor=white&color=5865F2)](https://discord.gg/5k2Q8AmqC)

</div>

---

## Quick Start

```bash
# Install via Homebrew (macOS/Linux)
brew tap regent-vcs/tap
brew install regent

# Or via Go
go install github.com/regent-vcs/regent/cmd/rgt@latest

# Initialize in your project
cd your-project
rgt init

# Work with Claude Code, Codex, or OpenCode normally — activity is tracked automatically

# See what happened
rgt log
rgt blame src/file.go:42
rgt show <step-hash>
```

That's it. Your agent activity is now auditable.

---

## Demo

<div align="center">
 

https://github.com/user-attachments/assets/a19b7c56-2e3c-4f04-81a1-d8665e3963b8


  <p><em>Every agent turn is automatically captured. No manual commits needed.</em></p>
</div>

---

## Examples

- [Debugging a Bad Refactor](examples/bad-refactor/) - trace a realistic billing regression with `rgt log`, `rgt blame`, and `rgt show`.

---

## What You Get

### See what your agent actually did

```bash
$ rgt log

Step a1b2c3d  |  2 min ago  |  Tool: Edit
│ File: src/handler.go
│ Added error handling to request handler
│ + 5 lines, - 2 lines

Step d4e5f6g  |  5 min ago  |  Tool: Write
│ File: tests/handler_test.go
│ Created unit tests for handler
│ + 23 lines

Step f8g9h0i  |  8 min ago  |  Tool: Bash
│ Command: go mod tidy
│ Cleaned up dependencies
```

### Blame: which prompt wrote this line?

```bash
$ rgt blame src/handler.go:42

Line 42: func handleRequest(w http.ResponseWriter, r *http.Request) {

Step:    a1b2c3d4e5f6
Session: claude-20260502-143021
Tool:    Edit
Prompt:  "Add error handling to the request handler"
```

### Track multiple concurrent sessions

```bash
$ rgt sessions

Active Sessions:
claude_code:claude-20260502-143021  |  3 steps  |  Last: 2 min ago
codex_cli:codex-20260502-091534     |  7 steps  |  Last: 2 hours ago

$ rgt log --session claude_code:claude-20260502-143021
# Filter history by session
```

### See full context for any change

```bash
$ rgt show a1b2c3d

Step a1b2c3d4e5f6
Parent: d4e5f6g7h8i9
Session: claude-20260502-143021
Time: 2026-05-02 14:30:21

Tool: Edit
File: src/handler.go

Changes:
+ func handleRequest(w http.ResponseWriter, r *http.Request) {
+     if r.Method != "GET" {
+         http.Error(w, "Method not allowed", 405)
+         return
+     }
- func handleRequest(w http.ResponseWriter, r *http.Request) {

Conversation:
User: "Add error handling to reject non-GET requests"
Assistant: "I'll add method validation to the handler..."
```

---

## Why This Exists

AI agents have no version control of their own.

You know this pain:
- *"It was working five minutes ago"*
- *"Why did you change that file?"*
- *"Go back to before the refactor"*
- `/compact` and pray
- Copy-pasting code into a fresh chat

Three primitives that should already exist:

- **`rgt log`** — what did this session do?
- **`rgt blame`** — which prompt wrote this line?
- **`rgt show`** — inspect the full context for any step

We gave agents write access to our codebases. We did not give ourselves git for it. re_gent fixes that.

---

## How It Works

re_gent stores agent activity in `.regent/` (like `.git/`):

```
.regent/
├── objects/     # Content-addressed blobs (BLAKE3)
├── refs/        # Session pointers (one per agent)
├── index.db     # SQLite query index
└── config.toml
```

Every tool-using turn creates a **Step** — a content-addressed snapshot of what changed, why, and who asked:

```go
Step {
  parent:      <previous-step-hash>
  tree:        <workspace-snapshot>
  causes:      [{ tool_name: "Edit", args: <input>, result: <output> }]
  session_id:  "claude_code:claude-20260502-143021"
  timestamp:   "2026-05-02T14:30:21Z"
}
```

Steps form a **DAG**. Each session has its own branch. Common ancestors dedupe. You get git-level auditability for agent activity.

**Technical details:** See [POC.md](POC.md) for the complete specification.

---

## Installation

### Via Homebrew (macOS/Linux)

```bash
brew tap regent-vcs/tap
brew install regent
```

This installs the `rgt` command and automatically sets up shell completions for bash, zsh, and fish.

### Via Go Install

```bash
go install github.com/regent-vcs/regent/cmd/rgt@latest
```

**Shell Completion (manual setup):**
```bash
# Bash
rgt completion bash > /usr/local/etc/bash_completion.d/rgt

# Zsh
rgt completion zsh > "${fpath[1]}/_rgt"

# Fish
rgt completion fish > ~/.config/fish/completions/rgt.fish
```

### From Source

```bash
git clone https://github.com/regent-vcs/regent
cd regent
make install
rgt version
```

`make install` writes only to Go's bin directory. It never replaces a Homebrew
or other package-manager installation elsewhere on `PATH`.

### Binary Releases

Download pre-built binaries from [GitHub Releases](https://github.com/regent-vcs/regent/releases)

---

## Supported Tools

| Tool | Status |
|------|--------|
| **Claude Code** | Fully supported |
| **OpenAI Codex CLI** | Fully supported |
| **OpenCode** | Fully supported |
| Cursor, Cline, Continue | Planned |

Hooks auto-configure on `rgt init`. No manual setup required.

### Terminal experience

In a terminal, setup is an inline TUI: responsive re_gent cards, animated work,
one persistent row per verified milestone, a keyboard-driven sharing choice,
and a distinct completion panel. It does not take over the alternate screen, so
the useful setup history remains visible after it exits.

When output is redirected, the same flow automatically becomes stable plain
text with no animation, cursor control, or ANSI sequences. Package-manager,
provisioning, hook-path, and other internal detail stays out of the way unless
setup fails.

Use verbose mode when troubleshooting:

```bash
rgt --verbose init
rgt --verbose connect http://127.0.0.1:7654

# Equivalent for scripts and the server-hosted installer
REGENT_VERBOSE=1 rgt connect http://127.0.0.1:7654
curl -fsSL http://127.0.0.1:7654/install | REGENT_VERBOSE=1 sh
```

Color is disabled automatically for non-interactive output and can be disabled
explicitly with `NO_COLOR=1`.

---

## Commands

| Command | Description |
|---------|-------------|
| `rgt --verbose <command>` | Show setup diagnostics hidden by the compact default experience. `REGENT_VERBOSE=1` is the script-friendly equivalent. |
| `rgt connect [server-url-or-ssh-target]` | Bind to an existing `http(s)` server, or give an SSH target to provision it first. A project is written only after its public `/healthz` answers. |
| `rgt connect --as <name>` | Register this project under a name you choose instead of one derived from its git remote. Recorded in the project binding, so it is said once. |
| `rgt init` | Initialize `.regent/` in current directory |
| `rgt connect --no-git-hook` / `rgt init --no-git-hook` | Wire agent hooks but not the Git `pre-push` hook, so `git push` does not deliver queued capture. |
| `rgt log` | Show step history (supports `--session`, `-n`, `--json`, `--graph`) |
| `rgt sessions` | List all active sessions |
| `rgt status` | Show current repository state |
| `rgt show <step>` | Display full context for a step (tool call + conversation) |
| `rgt blame <path>[:<line>]` | Show per-line provenance for a file |
| `rgt repair blame` | Recompute every stored blame map with the current diff. `rgt blame` is annotated at write time, so a diff fix does not reach maps already on disk; `rgt show` diffs at query time and needs no repair. Idempotent and safe to interrupt. |
| `rgt cat <hash>` | Inspect any object by hash (debugging tool; runnable but not listed in `rgt --help`) |
| `rgt push` | Push session history to a repo on a server (`--url`, `--repo`, `--session`) |
| `rgt version` | Print version information |
| `rgt completion` | Generate shell completion scripts |
| `rgt sync` | Deliver queued server-mode capture (`--status`, `--pull`, `--repair`) |
| `rgt pull [ref]` | Fetch the project's history from the server into this machine's cache. With no ref it asks the server what exists. |

---

## Server Mode

re_gent can run with a re_gent server as the source of truth. The repository keeps only its
`.regent/config.toml` server binding; history is stored in a machine-local cache and delivered by
the hooks. Capture is spooled when the server is unreachable, so an outage never blocks a live
agent turn.

```bash
export REGENT_SERVER_URL=http://127.0.0.1:7654
export REGENT_REPO_ID=my-project

rgt sync --status   # what is queued (no network)
rgt sync            # drain the queue now
rgt pull            # fetch the team's history into this machine's cache
```

A teammate who clones a connected project runs `rgt pull` once and then reads the team's
sessions with the ordinary history commands, offline.

### Sync on `git push`

`rgt init` and `rgt connect` also install a Git `pre-push` hook, so queued capture is delivered
when you share your work — no `rgt sync` to remember after an outage.

The hook **never fails a push**. If the server is unreachable the work stays queued, one line says
so and names `rgt sync`, and the push proceeds. A `pre-push` hook that was already there is kept,
runs first, and keeps its veto; `rgt disconnect` restores it. Turn it off with `--no-git-hook` when
wiring, `REGENT_GIT_SYNC_ON_PUSH=0` on a machine, or `git push --no-verify` once. Repositories
managed by husky or lefthook (`core.hooksPath`) are left alone, with the line to add printed
instead.

See **[docs/server-mode.md](docs/server-mode.md)** for the configuration reference, the consistency
guarantee, and the full failure-mode table (network blip, server down, partial write, divergence,
cache loss) with the accepted risks stated explicitly.

---

## Local development server (Docker)

Start a loopback-only server for local development:

```bash
make server
curl http://127.0.0.1:7654/healthz

# Connect a project
cd ~/code/my-project
rgt connect http://127.0.0.1:7654

# Push or pull history
rgt push
rgt pull
```

The server is currently unauthenticated, and Compose binds it to `127.0.0.1`.
Remote authentication, TLS, and Terraform are intentionally not part of this local baseline yet. To provision a Linux VPS, run `rgt connect root@host` from a project (or add `--url https://public.example` for NAT/DNS), review the plan, and confirm. `make server-down` stops it; the named Docker
volume preserves its data between runs.

---

## Multiple repos, one server

One `regent-server` process hosts any number of repositories. Each repo is addressed
by id and stored separately, so two repos never share refs, objects or history —
even when they use the same session ids and contain identical files.

```bash
regent-server --addr 127.0.0.1:7654 --data ~/.regent-server

# in each project, once:
cd ~/code/alpha && rgt push --url http://127.0.0.1:7654 --repo alpha
cd ~/code/beta  && rgt push --url http://127.0.0.1:7654 --repo beta

# afterwards the project remembers where it belongs:
cd ~/code/alpha && rgt push
```

The first push records `[remote] url` and `repo_id` in `.regent/config.toml`, so
later pushes need no flags. A project that is already bound keeps its identity:
an explicit `--repo` pushes elsewhere once, it does not re-point the project.

Objects are deduplicated *within* a repo, never across repos. Two projects that
contain the same file hash it to the same address, and each repo stores its own
copy — deduplicating across repos would leak one project's content into
another's history.

---

## Features

- **Content-Addressed Storage** — BLAKE3 hashing, automatic deduplication
- **Fast Queries** — SQLite index, sub-10ms lookups
- **Per-Session DAG** — Concurrent sessions tracked as separate refs
- **Conversation Tracking** — Survives `/compact` and `/clear`
- **Hook-Driven** — Transparent Claude Code, Codex, and OpenCode integration
- **Zero Configuration** — Hooks auto-configure on `rgt init`
- **Concurrency-Safe** — CAS refs, ACID transactions
- **Gitignore-Compatible** — `.regentignore` support

---

## Editor Integration

### VSCode Extension

Get inline blame annotations directly in your editor:

```bash
# From VSIX (Recommended)
# Download the latest .vsix from:
# https://github.com/regent-vcs/vscode-regent/releases
# Then in VS Code: Extensions > ... > Install from VSIX...

# From source (Development)
git clone https://github.com/regent-vcs/vscode-regent
cd vscode-regent
npm install && npm run compile
# Press F5 in VS Code to launch Extension Development Host
```

**Features:**
- Inline blame annotations showing which step modified each line
- Hover tooltips with full step context (timestamp, tool name, arguments)
- Session timeline view in the sidebar
- One-click access to conversation history

**Requirements:** `rgt` CLI must be installed and `rgt init` run in your project.

[View Extension Docs →](https://github.com/regent-vcs/vscode-regent)

---

## re_gent vs Git

| | Git | re_gent |
|---|---|---|
| **Tracks code** | ✅ | ✅ |
| **Tracks agent activity** | ❌ | ✅ |
| **Blame with prompt** | ❌ | ✅ |
| **Conversation history** | ❌ | ✅ |
| **Concurrent sessions** | ⚠️ shared workspace conflicts | ✅ separate captured session refs |
| **Purpose** | Developer VCS | Agent audit trail |

**re_gent complements git, doesn't replace it.** Use both.

---

## Roadmap

See [ROADMAP.md](ROADMAP.md) for planned features including:
- Non-destructive rewind and fork operations
- Additional tool adapters (Cursor, Cline, Continue)
- Session sharing and merge support
- Garbage collection and integrity verification

---

## Contributing

Contributions welcome! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

### Team development workflow

GitHub Issues are the source of truth for planned work. Every implementation
starts from an assigned child issue with acceptance criteria; epic issues track
outcomes and dependencies, not one large PR.

#### Ownership

| Owner | Responsibility | Active epics |
|---|---|---|
| [Arad (`@arad1410`)](https://github.com/arad1410) | Core engineering and most of the implementation-heavy work | [Remote fidelity #35](https://github.com/regent-vcs/re_gent_headless/issues/35), [remote lifecycle #38](https://github.com/regent-vcs/re_gent_headless/issues/38) |
| [Amir (`@Amirshrim`)](https://github.com/Amirshrim) | Complete onboarding-friendly epics with independently useful deliverables | [Documentation platform #37](https://github.com/regent-vcs/re_gent_headless/issues/37), followed by the designed Git integration work in [#31](https://github.com/regent-vcs/re_gent_headless/issues/31) |
| [Shay (`@shayliv`)](https://github.com/shayliv) | R&D lead, review/merge owner, UI foundation, infrastructure, deployment, security, and authentication | [UI foundation #36](https://github.com/regent-vcs/re_gent_headless/issues/36), [infrastructure #34](https://github.com/regent-vcs/re_gent_headless/issues/34) |

Shay is the required reviewer and merge owner for every PR entering `dev`.
Shay-authored PRs still require green CI and the same self-review checklist;
peer review is requested when practical, especially for security-sensitive work.

#### Issue status

| Label | Meaning |
|---|---|
| `status: ready` | Fully specified and available to start. |
| `status: in progress` | Assigned owner is actively implementing it. |
| `status: blocked` | A named dependency must land first. |
| `status: needs design` | Do not implement until the open design questions are resolved. |
| `status: review` | A PR is open against `dev` and awaiting review. |

#### From issue to merge

1. Choose an assigned `status: ready` child issue. Do not implement an epic as
   one PR.
2. Comment that you are starting, change its label to `status: in progress`,
   and pull the latest `dev`.
3. Create a branch named `<github-login>/<issue-number>-<short-name>`, for
   example `arad1410/39-conversation-pull`.
4. Implement only that issue's acceptance criteria. Add focused tests and
   update documentation when behavior changes.
5. Push the branch and open a PR **to `dev`**, never directly to `main`. Put
   `Closes #<issue-number>` in the PR body.
6. Change the issue to `status: review`, ensure CI is green, and request
   `@shayliv`.
7. Address review feedback. Only the review/merge owner merges into `dev`.
8. Promote `dev` to `main` only through a separate, milestone-level release PR.

Do not push directly to `dev` or `main`. Do not combine unrelated issues in one
PR. Architecture, storage, protocol, authentication, and security changes must
follow an accepted RFC or include the required design/threat-model update.

Regent may integrate with Git hooks, but Regent itself never stages, commits,
pushes, or changes a source repository's Git remotes.

**Quick Start:**
- [QUICK_START.md](.github/QUICK_START.md) — 5-minute setup guide
- [Good first issues](https://github.com/regent-vcs/regent/labels/good%20first%20issue)

**Before opening a PR:**
- [ ] The PR targets `dev` and closes one assigned issue
- [ ] The issue is labeled `status: review`
- [ ] `@shayliv` is requested as reviewer
- [ ] Tests pass: `go test ./...` and `go test -race ./...`
- [ ] Linter passes: `golangci-lint run`
- [ ] Code formatted: `go fmt ./...`

---

## Built With

- [cobra](https://github.com/spf13/cobra) — CLI framework
- [blake3](https://lukechampine.com/blake3) — BLAKE3 hashing
- [go-diff](https://github.com/sergi/go-diff) — Myers diff
- [modernc.org/sqlite](https://modernc.org/sqlite) — Pure Go SQLite

---

## License

[Apache License 2.0](LICENSE)

---

<div align="center">
  <p>
    <sub>Built by <a href="https://github.com/regent-vcs/regent/graphs/contributors">contributors</a></sub>
  </p>
  <p>
    <a href="https://discord.gg/5k2Q8AmqC">Discord</a> •
    <a href="https://github.com/regent-vcs/regent/discussions">Discussions</a> •
    <a href="https://github.com/regent-vcs/regent/issues">Issues</a> •
    <a href="POC.md">Technical Spec</a>
  </p>
</div>
