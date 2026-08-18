# UI development

The first UI vertical slice is a read-only explorer over the same canonical
objects and refs used by `rgt`. Runtime data comes from `regent-server`; mock
data exists only in Storybook.

## Start the local stack

Requirements: Go 1.22+, Docker, Node 24+, and Corepack.

```sh
make install
make dev
```

This builds and starts `regent-server` on `127.0.0.1:7654`, installs the locked
web dependencies, and starts Vite on `127.0.0.1:5173`. Vite proxies the re_gent
read API, so the browser and API remain same-origin during development.

To run the two processes separately:

```sh
make server
make ui
```

## Connect a repository

Run this from the repository whose agent history you want to inspect:

```sh
rgt connect http://127.0.0.1:7654 --as my-repo
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

Storybook remains the component and edge-state workshop:

```sh
cd web
corepack pnpm storybook
```

## Deployment boundary

The UI uses repository-scoped HTTP endpoints on `regent-server`. Local Vite
uses a proxy; the production self-hosted build will be served from the same
origin as the server. Hosted authentication and organization switching stay
outside this local MVP and can wrap the same typed client later.
