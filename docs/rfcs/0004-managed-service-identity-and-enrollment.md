# RFC 0004: Managed service identity, sign-in methods, and project enrollment

- Status: Draft for discussion
- Owners: re_gent maintainers
- Last updated: 2026-09-02
- Builds on: [RFC 0001](./0001-remote-repository-lifecycle.md) (remote lifecycle),
  [RFC 0003](./0003-authentication-authorization-tenancy.md) (authz contract)
- Related issues: #43 capabilities, #44 project ids, #45 atomic connect,
  #60 hosted auth, #34 managed infrastructure

## Summary

The managed service is a second composition of the public server core, not a
second product. This RFC specifies the parts RFC 0003 left to "private
follow-up work" and adds three requirements that came from product review:

1. **Connect once.** A source repository enrolls into an organization exactly
   once. Running `rgt connect` again from any clone of the same repository
   attaches to the existing project instead of creating a duplicate.
2. **Pluggable sign-in.** An organization chooses how its people sign in:
   GitHub, Google, email invitation, verified business domain, or a
   combination. An organization can require one method.
3. **Ordinary users just sign in.** After an operator has enrolled the
   repository and configured sign-in, a developer opens the URL, signs in with
   the method their organization allows, and sees the projects they are a
   member of. Nothing else is asked of them.

It also names the seams the **public** core must grow so the private
composition can be built without forking. Those seams are the critical path.

## Vocabulary

| Term | Meaning |
|---|---|
| **Organization** | The tenant. Owns projects, members, sign-in policy, domains, service tokens, quota. Identified by an immutable `org_<id>`. |
| **Project** | One enrolled source repository's history. Identified by an immutable `prj_<id>`. Has a mutable display name. Belongs to exactly one organization. |
| **Source fingerprint** | The stable identity of a source repository, derived on the client: normalized git remote host and path, plus the repository's smallest root commit. Used to make enrollment idempotent. |
| **Identity** | One (provider, subject) pair proving a person, for example `github:12345` or `google:sub`. A user has one or more identities. |
| **Sign-in method** | A way an organization allows identities to be created or used: `github`, `google`, `email_invite`, `domain`. |
| **Operator** | An organization owner or admin. Also, separately, Bonez staff with audited elevation. The text says "Bonez operator" when it means the latter. |
| **Enrollment** | The act of connecting a source repository to an organization, creating the project and its storage atomically. |

## Roles

Two role sets. Organization roles govern the tenant; project roles govern one
project's history and are unchanged from RFC 0003.

| Organization capability | Owner | Admin | Member |
|---|:---:|:---:|:---:|
| See the organization, switch to it | yes | yes | yes |
| Enroll (connect) a repository | yes | yes | policy |
| Manage members, invitations, domains, sign-in policy | yes | yes | no |
| Create and revoke service tokens | yes | yes | no |
| Change quota-affecting settings, delete organization | yes | no | no |

"policy" means the organization setting `members_can_enroll` (default off).
Enrolling a repository grants the enroller the project `owner` role.

Project roles: `owner`, `admin`, `writer`, `reader`, exactly as RFC 0003.
Organization owners and admins hold implicit project `admin` on every project
in their organization so they can always repair access; that implicit role is
shown in the UI as "via organization".

## Sign-in methods

An organization enables any subset. Each has its own trust story.

| Method | How an identity is established | Who may use it |
|---|---|---|
| `github` | GitHub OAuth. Subject is the GitHub user id. Verified primary email is read for domain matching and account linking. | Anyone; whether they land in an organization is decided by invitation or domain policy. |
| `google` | Google OpenID Connect. Subject is the Google `sub`. `email_verified` must be true. `hd` claim, when present, is the workspace domain. | Same as above. |
| `email_invite` | An admin invites an address. The invitee proves control of it by opening a signed, single-use, time-bound link. This creates an identity `email:<address>`. | Only invited addresses. |
| `domain` | Not a login on its own. A policy: an identity whose verified email is under a **verified** organization domain is admitted to that organization automatically as `member`. | Anyone signing in with `github` or `google` whose verified email matches. |

Rules that apply to every method:

- A person is one user with many identities. Linking a new identity to an
  existing user happens only through a verified email match **and** the
  organization policy `allow_email_linking` (default on), or through an
  explicit "link account" action while signed in.
- Unverified emails never match a domain and never link accounts.
- An organization may set `required_method` to `github` or `google`. Then
  browser sessions for that organization are only issued to sessions
  established with that method, and CLI device logins must use it too.
  Invitation links still work for first sign-in but must finish with the
  required method before the invitation is consumed.
- Bonez operators are a separate identity pool with their own method and
  never appear as organization members.

### Domain verification

An admin adds `example.com`. The server issues a DNS TXT challenge
`regent-verify=<token>` and verifies it on demand, then re-verifies weekly.
Public email providers (`gmail.com`, `outlook.com`, and a maintained list) are
refused. A domain belongs to at most one organization. Losing verification
disables domain auto-join but removes nobody.

### Invitations

`POST /api/v1/orgs/{org}/invitations` with email, organization role, optional
project grants. The invitation has a hashed token, expiry (default 7 days),
inviter, and status. Accepting requires the invitee to sign in with any method
the organization allows; the resulting identity's verified email must equal
the invited address, or the invitee is asked to sign in differently. An admin
can revoke or resend. Pending invitations are visible to admins only.

## Browser flow

1. UI loads `/api/v1/capabilities`. For managed it returns
   `deployment: managed`, `auth_methods` including the provider start URLs,
   and `features` including `organizations`, `project_ids`, `invitations`,
   `domains`, `service_tokens`.
2. The sign-in screen renders one button per method. An invitation link
   arrives with `?invite=<token>`; the screen keeps it through the OAuth
   round trip in a signed state parameter.
3. The provider callback lands on the server. The server validates state,
   exchanges the code server-side, reads the verified profile, and resolves
   the user:
   - existing identity: sign in;
   - no identity, verified email matches an existing user and linking is
     allowed: link and sign in;
   - otherwise: create the user and the identity.
4. The server issues the same `__Host-` session cookie and CSRF token as
   self-hosted. Sessions are twelve hours, refreshed on activity, revocable.
5. The UI calls `/api/v1/auth/me`, which now returns the user, their
   organizations with roles, and the last-used organization. If the user has
   no organization the UI offers "create an organization" and, when an
   invitation is pending, "accept invitation".
6. Organization switching is a client-side choice stored in the URL prefix
   `/o/{org}/…`. Every subsequent API call carries the organization in the
   path; the server never infers it from a cookie.

## CLI flow

`rgt auth login https://app.regent.dev`:

1. Reads capabilities. If `auth_methods` contains `device`, starts
   `POST /api/v1/auth/device` and prints a URL plus a short code.
2. The user approves in a browser where they are already signed in through an
   allowed method. Approval binds the device code to that user and, if the
   organization requires a method, verifies the browser session satisfied it.
3. The CLI polls and receives an access token (one hour) and a refresh token
   (thirty days, rotated on use). Both are stored in the machine-local config
   keyed by server, mode 0600, as today. Nothing is written into the
   repository.
4. Hooks send `Authorization: Bearer <access token>` exactly as they send a
   PAT today. On `401` with `token_expired`, the client refreshes once and
   retries; on failure it spools and reports through `rgt doctor`.

Automation uses **service tokens** created in the UI: organization-scoped,
optionally project-scoped, role-bound (`writer` by default), prefix `rgt_svc_`,
hashed at rest, expiring, revocable without restart. They are used the same
way as PATs.

Self-hosted keeps PATs. The CLI decides by capabilities, never by hostname.

## Project enrollment: connect once

### Client side

`rgt connect https://app.regent.dev --org acme [--as "Payments API"]`

1. Preflight, no mutation: capabilities, authentication, organization
   membership, and enrollment permission are checked first.
2. The client computes the **source fingerprint**:
   - `remote`: normalized origin host and path as `identityFromRemote`
     already does (case-folded, port and `.git` stripped, credentials
     removed);
   - `root_commit`: smallest root commit of `HEAD`, when the directory is a
     git repository;
   - `fingerprint = blake3(remote || "\n" || root_commit)`, hex.
   A repository with no remote uses the root commit alone. A directory that is
   not a repository has no fingerprint and must pass `--as`; it is always a
   new project and the CLI says so.
3. `POST /api/v1/orgs/{org}/projects` with the fingerprint, the human-readable
   remote, the root commit, and the optional display name.
4. The server answers one of:
   - `201 Created` with the new project;
   - `200 OK` with the **existing** project when the fingerprint is already
     enrolled in this organization. The CLI prints "already enrolled as
     <name>, attaching" and continues. This is the connect-once guarantee.
   - `409 Conflict` when the fingerprint is enrolled in this organization but
     the caller lacks project access; the message names an admin to ask.
   - `403` when the caller may not enroll; `404` for an organization they
     cannot see.
5. Only after a `200`/`201` does the client stage the cache, import existing
   local history, verify it, and then write the binding (RFC 0001 phases
   5–6, made atomic per #45).

The binding written to `.regent/config.toml` is:

```toml
[remote]
url = "https://app.regent.dev"
project_id = "prj_2f9c1a4b7d3e6081"
```

`repo_id` remains readable as a legacy alias for self-hosted servers that have
not migrated. New bindings never write it.

### Server side

Enrollment is one transaction in the policy store:

1. insert `projects(id, org_id, display_name, created_by, storage_root)`;
2. insert `source_repositories(project_id, fingerprint UNIQUE(org_id,
   fingerprint), remote, root_commit)`;
3. insert `memberships(project_id, user_id, role='owner')`;
4. create the storage directory via the core's storage locator;
5. commit; write the audit event `project.enrolled`.

A crash between steps leaves nothing discoverable. A retry with the same
fingerprint hits the unique constraint and returns the existing project.

Cross-organization: the same fingerprint may exist in two organizations. That
is a fork or a consultancy working for two clients, not a duplicate. Neither
organization can see that the other has it.

Legacy self-hosted servers keep client-named `repo_id` until #44 lands there
too; the same fingerprint table backs both.

### Renames and moves

Display name is mutable through `PATCH /api/v1/projects/{id}`. Changing the
git remote does not change the project: the binding carries `project_id`.
`rgt connect` run in a clone whose remote changed finds the project by the
binding first and by fingerprint second, and offers `rgt project relink` to
record the new remote when the fingerprint no longer matches.

## Open source projects

Large open source repositories are a deliberate adoption target, and they
break three assumptions above: every contributor works in a **fork** with a
different remote, the audience is the **public**, and captured transcripts
become **public records**. This section locks how each is handled.

### Visibility

A project has `visibility ∈ {private, public}`; default `private`. Only an
organization owner can flip it, the UI confirms with the consequence spelled
out, and the change is audited. For a public project:

- `history:read`, `object:read`, `ref:read`, and `repositories:list` for that
  project are granted to **anonymous** principals. This is the one explicit
  exception to RFC 0003's deny-by-default, expressed as a policy decision in
  the controller, never as an unauthenticated route.
- Writes still require a role. Membership, tokens, and settings are never
  public.
- Public read is rate-limited per IP and served with cache headers, because
  a popular repository's history will be crawled.
- Search results and deep links are shareable without sign-in.

### Forks and the upstream project

The source fingerprint from enrollment uses remote plus root commit. A fork
has a different remote and the **same root commit**. So:

- Enrollment looks up by full fingerprint first, then by `root_commit` alone
  among **public** projects. A root-commit match against a public project is
  reported to the CLI as `upstream: prj_…` with its display name.
- The CLI then offers two things: enroll the fork as its own project in the
  contributor's organization, or **contribute** to the upstream project. The
  default for a fork of a public project is contribute.
- Contribution does not require membership in the upstream organization. It
  requires a GitHub identity, and the contributor's sessions are recorded on
  their own session refs under the upstream project, labeled with their
  GitHub login and the fork remote. History from forks never rewrites
  upstream refs; every session ref is owned by exactly one contributor.
- A root commit shared by unrelated repositories (templates, generated
  starters) is a false upstream. The upstream project may set
  `accept_fork_contributions: false`, and the CLI always shows the match and
  asks before contributing.

### Roles derived from GitHub

For a public project whose source is on GitHub, roles are computed, not
invited:

| GitHub relationship, checked through the contributor's OAuth grant | Project role |
|---|---|
| Push or admin permission on the upstream repository | `writer`, and `admin` for repository admins |
| Author of an open pull request against the upstream | `contributor`, a new role: may push session refs under their own login only |
| Anyone else | `reader` (public) |

The relationship is re-checked on each device login and at least daily for
service tokens. Losing push access downgrades to `contributor` and revokes
nothing already recorded. Manual memberships still work alongside derived
roles for organizations that prefer them.

`contributor` is added to the RFC 0003 role table below `writer`: it can push
objects and refs only for session refs prefixed with its own login, and cannot
read another contributor's unpublished session until it is linked to a pull
request.

### Privacy gate for public capture

A prompt is speech; a tool result can contain a `.env` file. Public projects
get a mandatory ingestion gate:

- Secret scanning on every blob before it is stored under a public project,
  using the same detector set as push protection. A hit blocks the object,
  reports the finding to the pusher only, and spools locally so nothing is
  lost. The pusher can redact and re-push or keep the session private.
- Path allowlist: captured file snapshots are limited to paths tracked by git
  in the upstream repository, so local untracked files, home-directory
  references, and ignored files are never uploaded to a public project.
- Contributor opt-in: the first push from a contributor to a public project
  requires an explicit `rgt connect --public-ok` or a UI confirmation that
  their prompts and tool output will be public.
- Redaction of user home paths and usernames in tool output is on by default
  for public projects and cannot be turned off by contributors.
- Deletion: a contributor can withdraw their own session refs from a public
  project; the objects become unreachable and are garbage collected. The
  upstream organization cannot prevent withdrawal.

### Pull request provenance

The value for maintainers is seeing why a change was made. The `git push`
hook already syncs owed steps; for public projects it additionally records a
`git_commit` effect on each step whose tree matches a pushed commit. The
server exposes `/api/v1/projects/{id}/commits/{sha}/steps`, and a GitHub App
(post-beta) comments a link on the pull request. Reviewers open the link
without signing in and see prompts, tool calls, and blame for that commit.

### Adoption motion

The path a maintainer follows, in order, with nothing optional skipped:

1. Maintainer enrolls the upstream repository as public from a clone with the
   canonical remote. Roles are derived from GitHub, so there is nobody to
   invite.
2. The maintainer commits the `.regent/config.toml` binding and a
   `CONTRIBUTING.md` paragraph. The binding contains the server URL and
   project id, nothing secret.
3. A contributor clones or forks, runs `rgt connect`, and because the binding
   is already present the CLI needs no arguments: it detects the fork,
   proposes contributing to the upstream, asks for the public-capture
   confirmation, and starts a device login.
4. They work with their agent; sessions arrive under their login. On
   `git push` the steps behind their commits are linked.
5. Maintainers review with the provenance link. Nothing in this flow requires
   a maintainer to manage users.

Open source projects on the managed service are free and get a higher
quota tier justified by public visibility. Abuse controls are the derived-role
rule, per-contributor rate limits, and the secret gate.

## Public core seams required

These land in `bonez-io/re_gent` first. Without them the managed composition
cannot be written against the public packages, and the dependency rule in
RFC 0003 forbids copying.

| Seam | Today | Required | Package |
|---|---|---|---|
| Project registry | `POST /repos` makes a directory named by the client | `server.ProjectRegistry` interface: `Create`, `Lookup(fingerprint)`, `Get(id)`, `List(principal)`. Default filesystem implementation keeps today's behavior for open and self-hosted mode. | `server` |
| Tenant on resources | `Resource{Kind, RepositoryID, Name}` | add `TenantID`; route classification fills it from `/o/{org}/…` or from the project record | `serverauth` |
| Storage locator | `dataDir/repos/<id>` hard-coded | `server.StorageLocator` interface returning a project's root; default is today's layout; managed returns a tenant-scoped root | `server` |
| Audit | private table inside self-hosted | `serverauth.Auditor` interface called by the core for every mutation and denial, with actor, tenant, project, action, target, outcome, request id | `serverauth` |
| Quota and limits | max object size only | `serverauth.Limiter` consulted before object and ref writes and before enrollment, returning a typed `ErrQuotaExceeded` that maps to `413`/`429` with a stable error code the CLI understands | `serverauth` |
| Versioned project API | `/repos`, `/{repo}/…` | `/api/v1/orgs/{org}/projects`, `/api/v1/projects/{id}`; legacy routes keep working for one compatibility window with deprecation headers | `internal/server` |
| Conformance suite | 7 subtests inside `server` | importable `servertest` package with a `RunConformance(t, factory)` entry point covering the RFC 0003 evidence list plus enrollment idempotency and cross-tenant concealment | `servertest` |
| Capabilities | static self-hosted document | composition-provided document; `auth_methods` becomes a list of `{method, start_url}` | `server` |
| Anonymous principal | anonymous requests are rejected before `Authorize` | the core calls `Authorize` with an explicit anonymous principal so a policy can grant public read; default policies still deny | `serverauth`, `internal/server` |
| Contributor role and ref ownership | any writer may move any session ref | the core passes the ref name to `Authorize`; policies can restrict `ref:write` to refs owned by the principal | `serverauth`, `internal/server` |
| Ingestion gate | objects are stored as received | `server.IngestFilter` interface invoked before an object is written, returning accept, reject-with-reason, or rewrite; default is pass-through; managed installs secret scanning and path allowlists for public projects | `server` |
| Commit linkage | `git_commit` effect exists on steps | `/api/v1/projects/{id}/commits/{sha}/steps` read route classified as `history:read` | `internal/server` |

## Managed composition components

All private, in `bonez-io/re_gent-cloud`, importing the public packages at a
pinned commit.

- `cmd/regent-cloud`: boots the public core with the managed controller,
  registry, locator, auditor, limiter, and capabilities.
- `identity/`: providers (`github`, `google`, `email`), state signing, callback
  handling, account linking, device flow, session and token issuance.
- `policy/`: organizations, memberships, invitations, domains, projects,
  source fingerprints, service tokens, quotas. SQLite for the first beta on
  the encrypted data disk, behind an interface so Postgres can replace it.
- `ops/`: Bonez operator identities, audited elevation, export, deletion,
  support views.
- `deploy/`: Compose for local development with a **dev identity provider**
  that mints any email on request, so nobody needs real OAuth apps to work on
  the service; Terraform and runbooks for GCP.

## Data model, managed policy store

```
organizations(id, slug, display_name, required_method, members_can_enroll,
              allow_email_linking, created_at, deleted_at)
org_memberships(org_id, user_id, role, created_at)
users(id, display_name, primary_email, created_at, disabled_at)
identities(id, user_id, provider, subject, email, email_verified, created_at,
           UNIQUE(provider, subject))
domains(org_id, domain UNIQUE, verify_token, verified_at, last_checked_at)
invitations(id, org_id, email, org_role, project_grants_json, token_hash,
            invited_by, expires_at, accepted_by, accepted_at, revoked_at)
projects(id, org_id, display_name, created_by, storage_root, created_at,
         deleted_at)
source_repositories(project_id, fingerprint, remote, root_commit,
                    UNIQUE(org_id, fingerprint))
project_memberships(project_id, user_id, role)
credentials(id, user_id NULL, org_id NULL, kind ∈ {session, access, refresh,
            service}, name, prefix, secret_hash UNIQUE, scopes_json, csrf,
            created_at, expires_at, last_used_at, revoked_at)
device_codes(device_code_hash, user_code, org_id NULL, user_id NULL,
             expires_at, approved_at)
audit_events(id, request_id, actor_id, actor_kind, org_id, project_id,
             action, target_type, target_id, outcome, created_at)
quota_usage(org_id, projects, members, bytes, updated_at)
```

## Security requirements beyond RFC 0003

- OAuth state is signed and bound to the browser session nonce; callbacks
  reject mismatches. Codes are exchanged server-side only.
- Provider tokens are not stored after profile retrieval, except a Google
  refresh token when the organization enables periodic re-verification, and
  then encrypted at rest.
- Device codes are eight characters from an unambiguous alphabet, expire in
  ten minutes, and are rate-limited per IP and per user.
- Access tokens are opaque, hashed at rest, and checked for revocation on every
  request. No JWT in the first beta.
- Invitation, domain, device, and enrollment endpoints are rate-limited and
  produce audit events on success and denial.
- Enrollment responses never reveal whether a fingerprint exists in another
  organization.

## UI scope

- Sign-in screen with per-method buttons and invitation acceptance.
- Organization switcher and "create organization" flow.
- Organization settings: sign-in methods and required method, domains with
  verification status, members and roles, invitations, service tokens,
  quota usage.
- Project enrollment page that shows the exact `rgt connect` command for the
  organization and lists enrolled repositories with their remotes.
- Everything else reuses the existing screens under an organization prefix.

## Acceptance

- Enrolling the same repository twice from two clones yields one project and
  a `200` on the second call; renaming the folder or display name changes
  nothing.
- A user with a verified `@acme.com` Google identity is admitted to Acme
  automatically when `domain` is on and cannot be when it is off.
- An invitee with the wrong email is refused and told why.
- With `required_method: github`, a Google session cannot enter the
  organization and a CLI device login through Google is refused.
- Two organizations enrolling the same fingerprint each see only their own
  project; direct requests across the boundary return `404`.
- The conformance suite passes unchanged against self-hosted and managed.
- No credential, provider token, or captured content appears in logs or audit
  rows.
- A fork of a public project is detected by root commit; contributing records
  sessions under the contributor's login without upstream membership, and a
  contributor cannot move another contributor's ref.
- A blob containing a known secret pattern is refused by a public project,
  the pusher is told, and the session stays spooled locally.
- A public project's history is readable without sign-in; its members,
  tokens, and settings are not.
- A GitHub collaborator with push access gets `writer` without an invitation,
  and loses it within a day of losing push access.

## Work breakdown

Shaped for parallel implementation with disjoint file ownership. Streams A
and B agree their interfaces in the first day and then proceed independently.

| Stream | Owns | Depends on | Deliverable |
|---|---|---|---|
| **A. Core seams** | `server/`, `serverauth/`, `internal/server/` | none | registry, tenant resource, locator, auditor, limiter, versioned project API, composition-provided capabilities; self-hosted adapted |
| **B. Enrollment client** | `internal/cli/connect*.go`, `internal/cli/identity.go`, `internal/remote/`, `internal/config/` | A's API shape | fingerprint, `project_id` binding with legacy alias, idempotent connect, atomic cutover (#45), device login and refresh in `rgt auth` |
| **C. Conformance** | `servertest/`, `selfhosted/*_test.go` | A | importable suite covering the RFC 0003 list, enrollment idempotency, cross-tenant concealment; self-hosted green |
| **D. Managed composition** | `bonez-io/re_gent-cloud` | A, C | `cmd/regent-cloud`, providers, policy store, device flow, service tokens, invitations, domains, quotas, audit, dev identity provider, local Compose |
| **E. UI** | `web/` | A's capabilities shape | sign-in methods, invitations, organization switcher and settings, enrollment page |
| **F. Local dev loop** | `Makefile`, `docker-compose*.yml`, `docs/ui-development.md`, `web/vite.config.ts` | none | native `make serve`, one-command full stack, bootstrap helper, smoke test |
| **G. Open source mode** | `internal/capture/` redaction and path allowlist, `internal/cli/connect*.go` fork detection and `--public-ok`, managed `policy/` derived roles and visibility, managed `ingest/` secret gate | A, B, D | fork detection, contributor role, public read policy, secret gate, commit linkage route, `CONTRIBUTING.md` template |

Order: F and A start immediately. B starts once A publishes the project API
shape. C starts when A's interfaces compile. D starts when A and C are on
`dev`. E tracks A's capabilities document and can begin with the dev identity
provider from D. G starts after B and D have their first pull requests
merged; its secret gate and path allowlist can be prototyped earlier inside
`internal/capture` because they are host-independent.

## Open questions

1. Should organization slugs appear in URLs (`/o/acme/…`) or only ids? Slugs
   read better and leak nothing that the organization did not choose.
2. Does the first beta need generic OIDC for non-GitHub, non-Google
   businesses, or is `email_invite` plus `domain` enough?
3. Should a project be transferable between organizations, or is
   export-and-re-enroll acceptable for the beta?
4. Where do Bonez operator identities live: a separate GitHub organization
   allowlist, or Google Workspace under `bonez.io`?
5. Open source: should contributor sessions be visible publicly as soon as
   they are pushed, or only once linked to a pull request? Immediate
   visibility is simpler and more honest; deferred visibility protects
   contributors who abandon an approach.
6. Open source: which secret-detector set is the baseline, and is a
   false-positive override allowed for public projects at all?
7. Open source: should re_gent host public projects from GitLab and Codeberg
   in the first beta, or GitHub only? Derived roles depend on the provider.
