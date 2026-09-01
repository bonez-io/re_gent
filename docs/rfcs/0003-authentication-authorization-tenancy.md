# RFC 0003: Authentication, authorization, and tenancy

- Status: Proposed for `v1.2.0-beta.3`
- Owners: re_gent maintainers
- Last updated: 2026-09-01
- Related plan: [`../BETA-RELEASE-PLAN.md`](../BETA-RELEASE-PLAN.md)

## Summary

re_gent will support two secure compositions over one public protocol and
server core:

1. a production-capable, single-node self-hosted server with local users,
   project membership, roles, and personal access tokens; and
2. a private managed composition with GitHub OAuth, browser/device CLI login,
   organizations, tenant policy, service tokens, quotas, and operator controls.

The public server core owns route classification and invokes an injected access
controller before any non-public route. Authentication and product policy are
composition concerns. A server without an access controller is legacy local
mode and may not listen on a non-loopback address without the explicit
`--insecure-no-auth` override.

This RFC establishes the contract. It does not claim that the self-hosted or
managed identity stores are implemented yet.

## Goals

- Deny unauthenticated access to every repository, history, object, ref,
  settings, search, skills, export, and admin route.
- Make tenant and project scope an explicit policy input, not an implicit path
  convention.
- Keep credentials out of repository configuration and captured transcripts.
- Allow the managed service to remain private without forking the OSS protocol,
  storage model, shared UI, or server implementation.
- Preserve an intentional loopback-only development mode.
- Make authorization behavior executable through public conformance tests.

## Non-goals for the first beta

- Enterprise SSO/SAML, SCIM, custom roles, or organization policy languages.
- Third-party identity providers beyond GitHub in the managed service.
- A public skill marketplace or arbitrary remote skill publication.
- Multi-region active/active serving.
- Billing implementation. The managed beta is free and quota-bound.

## Trust boundaries

The source applications and local workspace remain the source of truth. A
managed re_gent server stores sensitive source snapshots, prompts, responses,
tool payloads, identities, and provenance. Possession of a repository identifier
is never authority to access it.

Trust boundaries are:

- browser or CLI to the public HTTPS endpoint;
- public ingress to the application server;
- identity/session/token store to policy evaluation;
- tenant/project policy to the content-addressed repository store;
- operator control plane to customer data plane;
- public `bonez-io/re_gent` packages to private `bonez-io/re_gent-cloud`
  implementations.

TLS termination, a private network, IAP, or an unguessable URL is defense in
depth. None replaces application authorization.

## Actors and credentials

| Actor | Credential | Intended use |
|---|---|---|
| Browser user | Secure, HTTP-only, SameSite session cookie plus CSRF token | Managed and self-hosted web UI |
| CLI user | Short-lived access token established by browser/device login | Interactive `rgt` use |
| Automation | Scoped, revocable service token | CI and approved agents |
| Self-hosted administrator | Bootstrap credential used once, then a normal session or PAT | Initial local server setup |
| Operator | Separate operator identity with audited elevation | Managed support and incident response |

Managed browser identity uses GitHub OAuth. CLI login uses a browser/device flow
and exposes `rgt auth login`, `rgt auth status`, and `rgt auth logout`. Tokens
are stored in the OS keychain where available, with a permission-restricted
user-config fallback. Repository `.regent/config.toml` stores only the server
URL and immutable project binding, never a bearer token.

Service tokens have a displayed prefix, a hashed-at-rest secret, explicit
tenant/project scopes, creation metadata, last-used metadata, and an expiry.
The plaintext secret is shown once. Revocation takes effect without waiting for
process restart.

## Principals, tenants, and projects

An authenticated principal contains:

- a stable subject identifier;
- authentication method;
- a tenant identifier for managed requests;
- zero or more resolved roles.

Managed organization IDs and project IDs are server-generated immutable IDs.
Human names and repository slugs are mutable display fields. A local filesystem
path, Git URL, or client-supplied slug must not become the security boundary.

Import creates a new project atomically: policy membership is committed before
the project becomes discoverable, and partial imports remain inaccessible.
Deleting or exporting a project operates on the immutable project ID.

## Roles

The beta has four project roles:

| Capability | Owner | Admin | Writer | Reader |
|---|:---:|:---:|:---:|:---:|
| Read history, objects, refs, blame, search | yes | yes | yes | yes |
| Push objects/refs and ingest captures | yes | yes | yes | no |
| Manage members and service tokens | yes | yes | no | no |
| Change retention/export settings | yes | yes | no | no |
| Delete project or transfer ownership | yes | no | no | no |

Organization owners may create projects and administer organization membership.
Operator access is not an implicit owner role: it uses a separate audited path
with a reason, bounded duration, and visible audit event.

## Public server contract

The supported embedding packages are:

- `github.com/bonez-io/re_gent/server` for server construction and options;
- `github.com/bonez-io/re_gent/serverauth` for principals, permissions, policy
  errors, and the access-controller interface.

The dependency direction is private-to-public. The public repository never
imports the managed repository. Cloud implementations satisfy public interfaces
and are tested against the same conformance suite as self-hosted policy.

The access controller separates authentication from authorization:

1. `Authenticate` validates credentials once and returns a principal.
2. The server classifies the route into a stable action/resource permission.
3. `Authorize` evaluates that permission before the handler reads repository
   state or a request body.
4. Repository listings apply a second per-project decision and omit hidden
   projects.
5. The principal is attached to the request context for audited mutations.

Policy errors map to stable HTTP behavior:

- missing or invalid credentials: `401` plus `WWW-Authenticate`;
- authenticated but disallowed operation: `403`;
- cross-tenant or existence-concealing denial: `404`;
- policy backend failure: generic `500`, with details only in server logs.

`/healthz`, `/install`, `/install.sh`, and `/bin/rgt` remain public because they
contain no repository or identity data. Every other current and future route is
authorized by default. New actions must be added to the route-policy matrix and
conformance tests in the same change as the route.

## Route-policy matrix

| Route family | Read action | Mutation action | Resource scope |
|---|---|---|---|
| `/repos` | `repositories:list` | `repository:create` | tenant/global |
| `/{project}/objects/*` | `object:read` | `object:write` | project + object hash |
| `/{project}/refs/*` | `ref:read` | `ref:write` | project + ref |
| `/{project}/api/*` | `history:read` | `history:write` | project + API suffix |
| `/api/skills/*` | `skill:list`, `skill:read` | none in beta | global curated catalog |
| settings/search/export routes | explicit action required | explicit action required | tenant/project |
| admin routes | none for normal members | explicit operator action | operator control plane |

The generic `request` action is fail-closed for unknown or not-yet-classified
routes. Managed and secure self-hosted policies must not grant it broadly.

## Browser and API protections

- Cookie-authenticated mutations require CSRF validation.
- Bearer tokens are accepted only in the `Authorization` header, never query
  parameters.
- Login, token, invite, search, object, and export routes have bounded work and
  rate limits.
- Redirect URIs and forwarded-host handling use an explicit trusted-proxy
  configuration.
- Authentication and authorization errors do not echo credentials or policy
  backend details.
- Security-sensitive mutations append an immutable audit event with actor,
  tenant, project, action, target, outcome, request ID, and timestamp.
- Logs and telemetry redact authorization headers, cookies, OAuth codes, token
  bodies, and captured customer content by default.

## Self-hosted secure mode

First start creates a one-time bootstrap flow on loopback or emits a one-time
credential to the operator terminal. The operator creates the first owner and
the bootstrap credential is invalidated. Passwords, if supported, use a modern
memory-hard hash; personal access tokens are preferred for CLI access.

Binding an unauthenticated server to anything other than loopback fails closed.
`--insecure-no-auth` is reserved for the local Docker profile and the current
IAP-protected staging topology while authenticated serving is implemented. Its
presence must be visible in startup logs and deployment configuration.

## Protocol negotiation and compatibility

The authenticated API will expose `/api/v1/capabilities` with server version,
protocol version, enabled auth methods, supported features, and size limits.
Clients fail with an actionable message when a required capability is absent.

The beta supports one explicitly documented legacy protocol window. Legacy open
servers remain connectable only when the user chooses a loopback/insecure local
profile; the managed client never silently downgrades authentication.

## Required conformance evidence

- Anonymous denial for every non-public route family.
- Correct `401`, `403`, concealed `404`, and internal-error behavior.
- Role matrix tests for read, ingest, member, token, settings, export, and
  deletion operations.
- Cross-tenant direct-object, list, search, export, and identifier tests.
- Token expiry, revocation, rotation, and redaction tests.
- Cookie/CSRF and bearer-token separation tests.
- Secure non-loopback startup tests.
- Two-machine connect/push/pull/read fidelity through authenticated APIs.
- Audit-event completeness without credential or customer-content leakage.

## Rollout

1. Land the public access-controller boundary, route classification,
   list-filtering, and non-loopback guard.
2. Implement self-hosted identity, users, project memberships, PATs, settings,
   migrations, and recovery flows in public.
3. Implement GitHub OAuth, tenant policy, device login, quotas, and operations in
   the private managed composition.
4. Run the shared conformance suite against both compositions.
5. Remove `--insecure-no-auth` from every non-local deployment before any
   managed endpoint accepts beta users.
