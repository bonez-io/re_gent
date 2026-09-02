# UI development

The first UI vertical slice is a read-only explorer over the same canonical
objects and refs used by `rgt`. Runtime data comes from `regent-server`; mock
data exists only in Storybook.

## Start the local stack

Requirements: Go 1.22+, Node 24+, and Corepack. Docker is optional (see
below) — the native path does not need it.

```sh
make dev
```

This builds `regent-server` and `rgt` into `./bin/`, starts `regent-server` in
the background on `127.0.0.1:${REGENT_PORT:-7655}` in the same persistent
self-hosted auth mode production runs, and starts Vite in the foreground on
`127.0.0.1:5173`. Vite proxies the re_gent read API (target overridable with
`VITE_REGENT_SERVER_URL`), so the browser and API remain same-origin during
development. Ctrl-C stops Vite and the background server together.

Self-hosted mode requires a one-time setup step before anything can sign in.
Once `make dev` (or `make serve`, below) reports the server is up:

```sh
./scripts/dev-bootstrap.sh
```

This is idempotent: on a fresh data directory it claims the bootstrap
credential, creates the first local owner (username taken from `$USER`),
prints that owner's personal access token once, and signs the CLI in with it
(`rgt auth login --token-stdin`). On a server that already has an owner it
says so and exits 0 without doing anything. See `scripts/dev-bootstrap.sh` for
what it does step by step.

To run the two processes separately, in two terminals:

```sh
make serve   # terminal 1 — regent-server in the foreground
make ui      # terminal 2 — Vite (after ./scripts/dev-bootstrap.sh)
```

`make serve-open` runs the legacy fully-open (no application auth) mode
instead, still bound to loopback only — useful for quickly poking at the API
without going through bootstrap, but it is not what production or `make dev`
run.

### Docker (optional)

The native path above is the default and does not need Docker. A Dockerized
equivalent is still available:

```sh
docker compose up -d --build
```

This builds and starts both `regent-server` (self-hosted auth, port
`${REGENT_PORT:-7654}`) and the `web` container (nginx + the built UI, port
`8080`), both published to loopback only. Read the bootstrap token with:

```sh
docker compose exec server cat /data/bootstrap-token
```

For the legacy fully-open mode instead, layer the override file:

```sh
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d --build
```

`make server` / `make server-down` / `make server-logs` wrap the same Compose
commands.

## Connect a repository

Run this from the repository whose agent history you want to inspect:

```sh
rgt connect http://127.0.0.1:7655 --as my-repo
rgt doctor
```

`rgt connect` is the complete server-mode initialization path. It initializes
the local binding, registers the repository, carries over existing local
history, installs detected agent hooks, and wires sync-on-push. Restart any
agent session that was already running so it reloads its hook configuration.

Use the agent normally, then verify delivery:

```sh
rgt sync
rgt sessions --format json
```

Open <http://127.0.0.1:5173>. Select the repository, open a session, and inspect
its chronological transcript, tool calls, steps, files, and blame.

## Verify changes

```sh
make ui-check
go test ./internal/server/...
```

`make smoke` runs an end-to-end check of the whole native dev loop — server,
bootstrap, `rgt auth login`, `rgt connect`, a captured turn, `rgt sync`, and
the server-side read, all on a scratch port and temp directory. Run it after
touching anything in the connect/auth/capture/sync path:

```sh
make smoke
```

Storybook remains the component and edge-state workshop:

```sh
cd web
corepack pnpm storybook
```

## Deployment boundary

The UI uses repository-scoped HTTP endpoints on `regent-server`. Local Vite
uses a proxy; the production self-hosted build is served from the same origin
as the server (see `docker-compose.production.yml` and
[`docs/self-hosted.md`](self-hosted.md)). The public client negotiates
self-hosted capabilities and browser sessions; managed OAuth and organization
switching can wrap the same typed client later.
