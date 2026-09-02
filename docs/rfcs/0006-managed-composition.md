# RFC 0006: Managed composition

- Status: Locked for `v1.2.0-beta.3`
- Owners: re_gent maintainers, Bonez
- Last updated: 2026-09-02
- Builds on: [RFC 0004](./0004-managed-service-identity-and-enrollment.md)
  for policy, roles, open-source rules, and the data model;
  [RFC 0005](./0005-self-hosted-team-onboarding.md) for the wizard, the API
  contract (Appendix A), and the `identity` package (Appendix B)
- Lives in: `bonez-io/re_gent-cloud`, private; imports the public module at a
  pinned commit

RFC 0004 says what the managed service is. This document says what gets
built for the first beta, in what order, and what a person sees.

## The promise

Nothing to run. A team lead opens the app, signs in with GitHub, names an
organization, and is on the same "connect repositories" screen a self-hosted
admin sees. Teammates arrive by invitation link or by verified company
domain and sign in with GitHub or Google. There is no password anywhere.

## What a new user sees

1. Opens `https://app.regent.dev`. One screen: "Continue with GitHub",
   "Continue with Google". No registration form.
2. After the provider round trip, `GET /api/v1/auth/me` shows no
   organizations, so the UI shows "Create an organization": display name and
   slug, with the slug derived and editable. Free-plan limits are shown on
   the same screen.
3. Screen 2 of RFC 0005, connect repositories, unchanged, with the server
   address fixed to `https://app.regent.dev` and the command block carrying
   the setup code.
4. Screen 3 of RFC 0005, users, with these differences: no password row;
   GitHub and Google rows are on and not configurable, because the service
   owns the OAuth apps; a **verified domains** row appears, RFC 0004's
   domain policy, with the DNS TXT record to add; "required method" appears.
5. Done. The organization's onboarding state is `done`; everything after
   that lives under `/o/{slug}/`.

A person who arrives with an invitation link goes through step 1 with the
invitation in the signed state, is admitted to that organization, and lands
on its project list. A person whose verified email is under a verified
domain is admitted to that organization on first sign-in with no link at
all.

## Composition

```
cmd/regent-cloud/        boots server.New with the managed controller, registry,
                         locator, auditor, limiter, capabilities, ingest filter
identity/                account linking, device flow, session and token issuance,
                         the Resolver for the public identity package, the private
                         email-invite provider
policy/                  Postgres store: organizations, memberships, invitations,
                         domains, projects, source repositories, credentials,
                         device codes, setup codes, connections, audit, quotas
routes/                  the RFC 0005 Appendix A routes that self-hosted implements
                         in selfhosted/, implemented here against policy/
ops/                     operator identities, elevation, export, deletion, support
deploy/                  docker-compose.dev.yml, Terraform, runbooks
```

Imports from the public module: `server`, `serverauth`, `servertest`,
`identity`. Nothing under `internal/` is reachable and nothing is copied;
where the managed composition needs the same route that `selfhosted/`
implements, it reimplements the route against its own store and proves
parity with the conformance suite plus the onboarding contract tests.

## Storage

Postgres from the first request. Migrations are embedded SQL files applied
at boot in order with an advisory lock, so two replicas cannot race. The
schema is RFC 0004's data model plus:

```
setup_codes(code_hash UNIQUE, org_id, created_by, expires_at, used_at,
            used_by_machine)
connections(id, org_id, project_id, remote, machine_name, connected_by,
            connected_at)
org_onboarding(org_id UNIQUE, state, updated_at)
```

Objects, trees, and blobs go through the public `StorageLocator` seam to a
per-organization prefix in an object-storage bucket. Local development uses
a directory locator.

Free-plan quotas, enforced through the `Limiter` seam and shown on the
organization page:

| Limit | Beta default |
|---|---|
| Organizations owned per user | 3 |
| Projects per organization | 10 |
| Members per organization | 25 |
| Stored bytes per organization | 2 GiB |
| Service tokens per organization | 10 |

Numbers are configuration, not code.

## Sign-in and admission

- Providers are the public `identity` package's GitHub and Google, with the
  service's own OAuth apps configured from the secret manager.
- The Resolver implements RFC 0004's rules: existing identity signs in;
  a verified email matching an existing user links when the organization
  allows linking; otherwise a user is created. Organization admission is by
  invitation, verified domain, or an explicit membership; a signed-in user
  with no organization can always create one.
- Sessions are the public server's cookie and CSRF scheme. Device login and
  refresh tokens are RFC 0004's CLI flow, already implemented client-side in
  `rgt auth login`.
- Service tokens, `rgt_svc_`, are created on the organization page.
- `required_method` per organization is enforced at session use, not only
  at sign-in, so a Google session cannot enter a GitHub-only organization.

## Local development

`deploy/docker-compose.dev.yml` runs Postgres, `regent-cloud` with the
`identity.NewFake` provider exposed as a "Dev sign-in" method that accepts
any email, and the public web image. No real OAuth app is needed to work on
the service. The UI shows the dev method only when capabilities list it.

## Production shape

GCP. One region. `regent-cloud` on Cloud Run behind a load balancer with
managed TLS; Cloud SQL Postgres with point-in-time recovery; a Cloud Storage
bucket per environment with per-organization prefixes; Secret Manager for
OAuth secrets and the state-signing key; Cloud Logging with credentials and
captured content excluded by construction, because the public core never
logs them. Terraform describes all of it. Nothing is created until the
release gates in the public beta plan pass.

## Acceptance

RFC 0004's acceptance list, plus:

- A new user reaches an enrolled repository with two browser screens and one
  terminal command, with no password created anywhere.
- The onboarding contract tests, shared with self-hosted, pass unchanged
  against the managed routes for screens 2 and 3.
- Two organizations enrolling the same fingerprint each see only their own
  project; the setup code of one cannot enroll into the other.
- A verified-domain user is admitted on first sign-in; removing the
  verification stops admission of new users and does not remove existing
  members.
- Every quota, when exceeded, returns `ErrQuotaExceeded` with a reason the
  UI shows, and the agent turn that hit it is spooled, not lost.
- The dev composition starts with one command and signs in with the fake
  provider.

## Work breakdown

| Stream | Owns | Deliverable |
|---|---|---|
| **D1. Policy store** | `policy/` | Postgres schema and migrations, store interface, per-table CRUD, quota accounting, audit sink, testcontainer-free tests against a `DATABASE_URL` and a SQLite-free fake for unit tests |
| **D2. Routes and composition** | `cmd/regent-cloud/`, `routes/`, `identity/` | boot, capabilities, sign-in Resolver, sessions, device flow, organizations, onboarding routes from RFC 0005 Appendix A, invitations, domains, service tokens, setup codes and connections feed, conformance suite green |
| **D3. Deploy** | `deploy/`, `Makefile`, `.github/workflows/` | dev compose with Postgres and the fake provider, CI that runs D1 and D2 tests against Postgres, Terraform skeleton, runbook stubs |

D1 publishes the store interface on day one from the schema above; D2 codes
against the interface with an in-memory fake until D1's Postgres
implementation lands. D3 starts immediately.
