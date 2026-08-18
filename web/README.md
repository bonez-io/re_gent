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
