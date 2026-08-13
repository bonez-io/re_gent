# Plan: Self-hostable re_gent server (docker run + one-command connect)

## Problem
`rgt serve` exists but isn't a product anyone can stand up. Today: no container image, it
binds `127.0.0.1` by default (unreachable from outside a container), and it enforces **no
auth** — so it can't safely run "wherever you want." Connecting a repo takes several manual
steps (`login` then `connect`). Goal: **`docker run` your own re_gent server anywhere, then
connect a repo to it in one command — securely.**

## Constraints / locked decisions
- Language: Go (`github.com/regent-vcs/regent`). Keep single static binary, no CGO.
- Backwards compatible: with no token configured, the server stays open (local-dev default).
- Reuse existing config surfaces: `REGENT_SERVER_URL` / `REGENT_REPO_ID` / `REGENT_TOKEN`
  (client) and `~/.regent/config.toml`. See `docs/server-mode.md`.
- Out of scope for now: the `drop` feature/UX, TLS termination (assume a reverse proxy),
  multi-tenant user accounts, and the regent-viewer GUI.

## Step 1 — Add bearer-token auth to `rgt serve`
The server accepts every request unauthenticated (no `Authorization` check in
`internal/server`). Add optional bearer-token auth: `serve` reads a token from
`--auth-token` or env `REGENT_SERVER_TOKEN`; when set, every `/objects`, `/refs`, `/repos`
request must send `Authorization: Bearer <token>` or receive `401`. When unset, behavior is
unchanged (open, local-dev). The client already sends `Authorization: Bearer <token>`.

## Step 2 — Health endpoint + Dockerfile
Add a tiny unauthenticated `GET /healthz` (200 "ok") for container healthchecks. Add a
multi-stage `Dockerfile` (build the static `rgt`, minimal runtime image) whose default
command is `rgt serve --addr 0.0.0.0:7654 --data /data`, `EXPOSE 7654`, `VOLUME /data`, and
reads `REGENT_SERVER_TOKEN`. Add `.dockerignore`.

## Step 3 — docker-compose + one-command run
Add `docker-compose.yml` (the server service, a named volume for `/data`, `REGENT_SERVER_TOKEN`
env) and a `Makefile`/`make server` target so a user can start a server with a single command
and a printed connect hint. Persisted data survives restarts.

## Step 4 — One-command connect DX
Make connecting trivial: extend `rgt connect <server-url>` to accept `--token` so a single
command both authenticates (stores token) and registers the repo (writes `[remote]`), instead
of requiring a separate `rgt login`. Keep `login` working. Print a clear success + next-step.

## Step 5 — Quickstart docs + published image
Write a README "Self-host your server" quickstart (docker run + connect + push) and extend the
existing release workflow (`.github/workflows/release.yml`) to build and publish the server
container image on release so users can `docker run` it without building locally.
