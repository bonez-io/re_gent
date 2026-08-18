# UI architecture decision

Status: proposed for the UI foundation epic

Scope: issues #36, #47, #48, and #49

## Decision

Build one public TypeScript web application in `web/` in this repository. The
same application artifact runs against a local, self-hosted, enterprise, or
re_gent-managed server. Product differences are returned by the server as
authenticated capabilities; they are not separate frontend forks or
compile-time trust decisions.

The first stack is:

| Concern | Choice | Why |
|---|---|---|
| UI runtime | React + TypeScript | Mature component ecosystem and familiar contributor path. |
| Build tool | Vite | Fast local HMR and a static production build that can be served by the Go server. No Node runtime is required in production. |
| Routing | React Router, data mode | Stable deep links and route-level pending/error boundaries without adopting an SSR framework. |
| Server state | TanStack Query | Owns request lifecycle, cache, polling, retry, and offline/error states. URL state remains in the router. |
| Styling | Tailwind CSS with re_gent tokens | A small, consistent visual vocabulary with zero runtime styling. Tokens, not arbitrary one-off colors, define the product. |
| Accessible primitives | Radix Primitives, adopted per component | Keyboard/focus/ARIA behavior for complex controls without imposing a visual identity. Prefer native HTML for simple controls. |
| Icons | Lucide | Small, consistent open-source icon set. Every icon-only control still needs an accessible name. |
| API contract | OpenAPI 3.1 + generated TypeScript types + `openapi-fetch` | The Go server and browser share one versioned contract instead of maintaining parallel handwritten types. |
| Unit/component tests | Vitest + Testing Library | Fast tests through the same Vite transform pipeline, asserted through user-visible behavior. |
| Browser tests | Playwright + axe | Real desktop/mobile flows, visual regression, and automated accessibility checks. |
| Package manager | pnpm, pinned by `packageManager` | Reproducible installs and a small dependency store. |
| Runtime baseline | Node 24 LTS for development/CI | Stable contributor and CI baseline; production remains the Go server plus static assets. |

Libraries are added only when a product requirement needs them. In particular:

- Do not add Redux or Zustand initially. Server data belongs to TanStack Query;
  navigation and filters belong in the URL; ephemeral component state stays
  local.
- Do not add a data-grid library for the activity feed. Use semantic lists and
  tables until scale proves virtualization is necessary.
- Add Shiki only with the historical file/blame screen. The file viewer is not
  an editor and does not need Monaco.
- Adopt Radix primitives individually. There is no generic component kit whose
  defaults become the re_gent design language by accident.

## Repository boundary

The public product repository owns everything necessary to run a complete
self-hosted system:

```text
re_gent_headless/
├── api/
│   └── openapi.yaml       # versioned public data-plane contract
├── cmd/
│   └── regent-server/     # Go server
├── internal/server/       # object/ref and read APIs
├── web/                   # public read-only explorer
└── docker-compose.yml     # self-hosted server + UI entry point
```

Keeping `web/` here makes an API change, generated client update, server
contract test, and consuming UI change reviewable in one pull request. It also
lets the self-hosted image serve the static UI without depending on another
repository or release train.

The previous issues #15 and #16 say that viewer work happens in a separate
viewer repository. No such repository currently exists in the `regent-vcs`
organization, and epic #36 supersedes that direction. Those tickets should be
updated to point at `web/` before implementation begins.

## Deployment boundary

The browser is never the security boundary. Hiding a managed feature in the UI
does not authorize it; every server operation remains authenticated and
authorized independently.

### Local development

One command starts the Go server and Vite dev server. Vite proxies `/api` and
other server routes to Go, so browser requests remain same-origin from the
application's point of view and production does not need a second API URL
model.

### Community self-hosted

- One Docker Compose stack and one public container image.
- The Go server serves the compiled SPA and the versioned API on the same
  origin.
- Loopback development can remain explicitly unauthenticated.
- Any non-loopback deployment must use authentication and TLS, either through
  the server's supported OIDC configuration or a documented trusted reverse
  proxy. Until that exists, the UI must not imply that an open remote server is
  safe.

### Enterprise self-hosted

- The public UI is unchanged.
- Enterprise server capabilities may add SSO policy, audit export, retention,
  organization administration, or external storage.
- The UI reads a server capabilities document and renders only routes the
  authenticated principal may use.
- Commercial server modules or deployment packaging may live in a private
  repository, but should not fork the explorer.

### re_gent managed

- Use a separate private control-plane/infrastructure repository for tenant
  provisioning, authentication, secrets, billing, abuse controls, and cloud
  deployment.
- Keep the content-addressed data plane and the read-only explorer in this
  public repository.
- The managed gateway establishes the user session, selects a tenant/project,
  and proxies the versioned data-plane API. The UI receives no permanent API
  token and no cloud credentials.
- Start with one deployed artifact. If managed-only administration screens
  eventually become substantial, they can be a private route package loaded by
  the managed shell without copying the public explorer.

## Runtime contract

The UI loads a small, same-origin bootstrap document before protected product
queries:

```json
{
  "deployment": "self-hosted",
  "version": "0.2.0",
  "viewer": {
    "name": "Shay Livne"
  },
  "capabilities": [
    "projects:read",
    "sessions:read",
    "files:read",
    "blame:read"
  ]
}
```

The exact identity fields wait for the authentication RFC. The important
invariant is that deployment and permissions come from the server at runtime,
not from `VITE_*` build flags.

## Versioned API shape

Existing `/repos`, `/{repo}/api/sessions`, and `/{repo}/api/log` endpoints are
useful prototypes but are not the UI contract. Issue #49 should expose the
read-only surface under `/api/v1`:

```text
GET /api/v1/meta
GET /api/v1/projects
GET /api/v1/projects/{project_id}
GET /api/v1/projects/{project_id}/sessions
GET /api/v1/projects/{project_id}/sessions/{session_id}/steps
GET /api/v1/projects/{project_id}/steps/{step_hash}
GET /api/v1/projects/{project_id}/steps/{step_hash}/tree
GET /api/v1/projects/{project_id}/steps/{step_hash}/files/{path}
GET /api/v1/projects/{project_id}/steps/{step_hash}/blame/{path}
```

Lists use cursor pagination, deterministic ordering, explicit empty arrays, and
a shared JSON error envelope. File endpoints distinguish text, binary, deleted,
oversized, and unavailable content. Conversation, reasoning, and tool payloads
are returned according to the current user's authorization and server
redaction policy.

## Rejected alternatives

### Next.js or another server-rendered framework

The explorer is an authenticated application, not an SEO surface. Requiring a
Node server would complicate the single-container self-hosted path and create a
second production runtime. The docs/marketing site can choose its own framework
without forcing it onto the product UI.

### A separate public viewer repository

This creates release skew between the API and its first-party client and makes
the self-hosted build depend on two repositories. Revisit only if independent
release cadence becomes a demonstrated constraint.

### Separate hosted and self-hosted frontends

Two explorers will drift in behavior, tests, and security assumptions. Runtime
capabilities provide the necessary variation while preserving one provenance
experience.
