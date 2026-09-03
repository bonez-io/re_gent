# re_gent web

The public re_gent explorer and its component workshop. The same static build
is intended for local, self-hosted, enterprise, and re_gent-hosted servers.

## Local development

Requires Node 24+ and pnpm 10.11.

```sh
pnpm install
pnpm dev
```

The app is available at <http://localhost:5173>.

The development server proxies the repository registry and repo-scoped API to
`http://127.0.0.1:7654`. Override it with `VITE_REGENT_SERVER_URL`; set
`VITE_REGENT_REPO_ID` to skip the repository picker when that repository is
registered. Authenticated deployments use the runtime capability document and
same-origin browser session; bearer tokens are never compiled into the UI.

Runtime routes are repository-scoped:

- `/repos/:repoId/sessions`
- `/repos/:repoId/sessions/:sessionId`
- `/repos/:repoId/steps`
- `/repos/:repoId/files`
- `/repos/:repoId/sync`

The runtime reads the real Go server API. Storybook owns the curated fixture
responses in `.storybook/msw-handlers.ts`; application code does not import
those fixtures.

## Component review

```sh
pnpm storybook
```

Storybook is available at <http://localhost:6006>. Components must be reviewed
there before new variants are assembled into routes.

## Verification

```sh
pnpm check
pnpm lint
```

`pnpm check` builds the app, runs Storybook interaction and accessibility
tests, and verifies the static Storybook build.

See [`THIRD_PARTY_NOTICES.md`](./THIRD_PARTY_NOTICES.md) for the component
sources adapted by the foundation.
