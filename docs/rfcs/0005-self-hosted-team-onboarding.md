# RFC 0005: Self-hosted team onboarding

- Status: Locked for `v1.2.0-beta.3`
- Owners: re_gent maintainers
- Last updated: 2026-09-02
- Supersedes: the bootstrap-token flow in [RFC 0003](./0003-authentication-authorization-tenancy.md)
  "Self-hosted secure mode" and in `docs/self-hosted.md`
- Related: [RFC 0004](./0004-managed-service-identity-and-enrollment.md) for the
  managed composition, which shares the wizard and the enrollment mechanics

## The three modes

| Mode | Who runs what | Login |
|---|---|---|
| **Local** | Nothing but `rgt` on one machine. `rgt init`, `rgt log`, `rgt blame`. | None. |
| **Team, self-hosted** | The team runs one server with Docker Compose. This RFC. | Username and password, set up by a wizard. |
| **Team, managed** | Bonez runs the server for many teams. RFC 0004. | GitHub, Google, invitation, domain. |

Local mode is unchanged. This document locks the self-hosted team mode.

## The promise

Running the server is one command. Everything after that happens in the
browser, in order, with nothing to copy out of a container and nothing to
hand to teammates by hand:

1. `docker compose up -d` prints the address and the initial admin sign-in.
2. The browser opens on a wizard: organization and admin, connect
   repositories, add users, done.
3. Teammates receive a link, set a password, install `rgt`, and connect.

## Step 0: start the server

```bash
docker compose up -d
```

On first start, with an empty data volume, the server:

- creates the user `admin` with a random 20-character initial password, or
  with `REGENT_ADMIN_PASSWORD` when that variable is set in `.env`;
- prints, to its own stdout so that `docker compose up` and
  `docker compose logs server` both show it:

```
re_gent is ready at http://127.0.0.1:8080
Sign in as admin with the initial password: k7Qv-3mZp-...
This password must be replaced on first sign-in.
```

- marks the instance `onboarding: admin_password`.

The initial password is valid only until it is replaced, which the wizard
forces immediately, so its exposure in a log is bounded to that window. The
`bootstrap-token` file and the `Authorization: Bootstrap` route are removed.
`--recover-owner-token` stays as the lost-admin recovery path.

A restart before onboarding completes keeps the same initial password; it does
not rotate, because rotation is what made the old flow confusing.

**Upgrading a data directory from the bootstrap-token era.** Such a
directory already has an instance owner and no password. The server adopts
that owner as the admin instead of creating a second one: it prints the same
three-line message with the owner's username, the owner must replace the
initial password on first browser sign-in, and every token the owner already
holds keeps working, because the initial-password gate applies to browser
sessions only. Existing projects appear in the organization the wizard
creates; nothing is renamed or moved.

## Step 1: sign in and the wizard

The sign-in page shows username and password. The capabilities document
reports `auth_methods: ["password", "browser_session"]` and
`onboarding: "<state>"`; the UI opens the wizard whenever the state is not
`done`.

The wizard has four screens. Each screen saves on its own, so a closed tab
resumes where it stopped.

### Screen 1: organization and admin

Fields, with defaults:

| Field | Default | Notes |
|---|---|---|
| Organization name | empty, required | Display only. One organization per self-hosted instance. |
| Server address | detected from the request, editable | Used in every command the wizard prints and in invitation links. |
| Admin username | `admin` | Renaming is allowed here and only here. |
| Admin display name | empty | |
| Admin email | empty, optional | Needed only for invitation replies. |
| New password | required, minimum 12 characters | Replaces the initial password. Argon2id. |
| Who can join | `invite_only` | Alternative `open`: anyone with the server address may register. Off by default. |
| Default role for new members | `reader` | Applies to projects a new member is granted at invitation time. |

Saving this screen replaces the initial password, records the organization,
and moves the state to `connect`.

### Screen 2: connect repositories

The screen shows one command block and waits.

```bash
curl -fsSL http://127.0.0.1:8080/install | sh && rgt connect http://127.0.0.1:8080 --setup 7KQ2-M9XA
```

- `/install` already exists and serves a matching `rgt` for the caller's
  platform from the server's own binaries.
- `--setup <code>` is new. The code is one-time, expires in 15 minutes, and
  is bound to the admin user. `rgt connect` exchanges it for a machine
  credential stored exactly as `rgt auth login` stores one, then enrolls the
  repository through `/api/v1/projects` (RFC 0004 connect-once), installs the
  agent hooks, and carries over local history.
- The UI listens on `GET /api/v1/orgs/{slug}/connections` (long-poll)
  and appends a row the moment a project is enrolled: display name, remote,
  machine name, and a green check.
- "Connect another repository" issues a fresh code and keeps the list.
- "Continue" and "Skip for now" both move the state to `users`. The same
  screen is reachable later from Settings and from the empty project list.

`rgt init <server-address>` is accepted as an alias for `rgt connect` so the
README's first command and the team command read the same way.

### Screen 3: users

Two decisions, then an invitation list.

**How people sign in.** Checkboxes, saved as instance settings:

| Method | Beta status | What it needs |
|---|---|---|
| Password | on, cannot be turned off in beta | nothing |
| Invitation links | on | nothing; email delivery is optional |
| GitHub | off until configured | a GitHub OAuth App: client id and secret, created by the admin in their GitHub organization; the wizard shows the callback URL to paste into GitHub. Optional base URL for GitHub Enterprise Server. |
| Google | off until configured | a Google OAuth client id and secret; same mechanics as GitHub |
| Single sign-on, OpenID Connect | planned, shown disabled with "coming soon" | a generic identity provider; after beta |

**GitHub and Google sign-in.** The provider code is one public package,
`identity`, used unchanged by the managed composition (RFC 0004).
A person who signs in with GitHub is admitted only when one of these holds,
checked in this order:

1. an invitation names their GitHub username or their verified primary
   email, in which case the invitation is consumed and the account created;
2. they are already a member with an identity or email that matches, in which
   case the identity is linked to that account;
3. the organization lists their GitHub organization in **allowed GitHub
   organizations**, an optional field on this screen, in which case they join
   as `member` at the default project role;
4. the join policy is `open`.

Otherwise the callback ends on a page that says the account is not invited
and shows the admin's contact. GitHub sign-in reads only the user's id,
login, and verified emails, plus organization membership when rule 3 is
configured. It works over plain http on a LAN because GitHub redirects the
browser, not the server; the callback URL is whatever server address screen
1 recorded. The admin account keeps its password as the break-glass method
regardless of which methods are on.

Email delivery: optional SMTP settings on this screen. When configured,
invitations are emailed. When not, each invitation shows a link to copy.
The wizard never blocks on email.

**Invite users.** A list of rows: email or username, organization role
(`admin` or `member`), and the projects and project role they get, defaulting
to every currently enrolled project at the default role. Each row produces
an invitation with a hashed token and a 7-day expiry.

"Continue" moves the state to `done`.

### Screen 4: done

A summary, the teammate instructions (the same command block as screen 2
without `--setup`, plus "sign in with your invitation link"), and links to
Settings and the docs.

## Teammate flow

1. Opens the invitation link. Sets a display name and a password, or, when
   GitHub sign-in is on, clicks "Continue with GitHub" and is linked to the
   invitation. Is signed in.
2. Runs the command from the invitation page: install `rgt`, then
   `rgt auth login <server>`, which prompts for username and password, or
   opens the browser for GitHub when that is the person's only method, and
   stores a machine credential. Tokens are never shown to people.
3. Clones the repository and runs `rgt connect` with no arguments; the
   committed `.regent/config.toml` names the server and project.

Personal access tokens remain available under Settings for CI and scripts,
and are the only thing that page shows.

## Storage

Users, credentials, memberships, invitations, setup codes, sessions, and
audit rows are persisted in SQLite at `/data/identity.db` on the compose
volume, in WAL mode. Nothing about identity is in memory beyond the process
cache of an open session. This is the same choice Gitea makes for the same
reason: one container, one volume, and a backup is a copy of that volume.

The identity store is behind an interface, `selfhosted.IdentityStore`, with
SQLite as the only public implementation. Postgres is not added to the
self-hosted stack in beta: it would add a second container, a database
password to manage, and a second migration dialect to keep green, for a
single-instance deployment that SQLite already serves. The managed
composition runs Postgres from day one (RFC 0004), and its implementation of
the same interface can be offered to self-hosted installs later, selected by
`REGENT_DATABASE_URL`, without changing the wizard or the routes.

What the beta does ship for durability: `rgt admin backup` writes a
consistent snapshot of `identity.db` and `projects.db` using SQLite's online
backup API, so a volume copy taken mid-write is never the only option, and
`docs/self-hosted.md` documents restore.

## Server changes

In `selfhosted/`:

- password credentials: Argon2id hashes, per-user, with rate-limited sign-in
  and a change-password route;
- initial admin creation and stdout message on first start;
- an `onboarding` state column with values `admin_password`, `connect`,
  `users`, `done`, exposed in capabilities;
- an `organization` record: name, server address, join policy, default role,
  SMTP settings;
- setup codes: hashed, one-time, 15-minute expiry, bound to a user, exchanged
  at `POST /api/v1/auth/setup-code` for a credential;
- invitations: create, list, revoke, accept; email delivery when SMTP is set;
- a connections feed for the wizard: `GET /api/v1/orgs/{slug}/connections`;
- GitHub and Google sign-in through `identity`: provider settings
  stored encrypted at rest with a key on the data volume, signed OAuth state
  bound to the browser session, callback handling, and the admission rules
  above; `identities(provider, subject)` unique per user;
- `rgt admin backup` and the online-backup route it calls;
- removal of the bootstrap file and route.

Routes are added to the route-policy matrix with the same fail-closed rule as
every other route: only sign-in, the OAuth start and callback routes,
invitation acceptance, capabilities, health, and install are public.

## CLI changes

- `rgt connect --setup <code>`: exchange, store credential, then the normal
  connect-once flow.
- `rgt init <url>` as an alias for `rgt connect <url>`.
- `rgt auth login <url>`: when capabilities list `password`, prompt for
  username and password and exchange them for a credential; keep the
  `--token-stdin` path for PATs; keep the device flow for managed servers.

## Compose and docs

- `docker-compose.yml` and `docker-compose.production.yml` print nothing
  themselves; the server's stdout carries the message.
- `.env.example` documents `REGENT_ADMIN_PASSWORD`, `REGENT_PORT`,
  `REGENT_WEB_PORT`, and `REGENT_DOMAIN`.
- `docs/self-hosted.md` is rewritten around the wizard.

## Acceptance

- A clean machine with Docker reaches the wizard's first screen with one
  command and one browser visit, with no file read from a container.
- The initial password stops working the moment the wizard's first screen is
  saved.
- The connect command block enrolls a repository with no other action in the
  terminal, and the wizard shows it within two seconds.
- Reusing a setup code fails; an expired one fails with a clear message.
- An invitee reaches a signed-in state from the link alone, and can connect a
  clone with `rgt auth login` plus `rgt connect`.
- With a GitHub OAuth App configured, an invited GitHub user reaches a
  signed-in state from the invitation link through GitHub, and an uninvited
  GitHub user is refused with the not-invited page and no account created.
- Restarting the stack, and restoring a backup taken with `rgt admin backup`
  onto an empty volume, both bring back every user, membership, and project.
- The conformance suite still passes; every new route denies anonymous access
  except the public ones listed above.
- No password, initial password, setup code, or invitation token appears in
  audit rows, application logs after first start, or API responses other than
  the one that created it.

## Work breakdown

| Stream | Owns | Deliverable |
|---|---|---|
| **S1. Server** | `selfhosted/`, `internal/server/` routes for onboarding, `serverauth/` if new actions | passwords, initial admin, onboarding state, organization, setup codes, invitations, connections feed, backup, bootstrap removal, conformance updates |
| **S5. Identity providers** | `identity/` | GitHub and Google OAuth: settings, state signing, callback, profile and email fetch, organization membership check, a fake provider for tests; consumed by S1 and by the managed composition |
| **S2. CLI** | `internal/cli/auth.go`, `internal/cli/connect.go`, `internal/remote/`, `internal/config/` | `--setup`, `init` alias, password login, remotetest endpoints |
| **S3. Compose and docs** | compose files, `.env.example`, `docs/self-hosted.md`, `scripts/dev-bootstrap.sh` | first-start message, env documentation, rewritten guide, dev bootstrap through the new routes |
| **S4. UI** | `web/` | sign-in with password, the four wizard screens, invitation acceptance page, Settings entry points |

S1 and S2 agree the setup-code exchange and the onboarding state names on day
one and then proceed in parallel. S5 agrees its interface with S1 on day one
and ships with a fake provider first so S1 and S4 never wait on real OAuth
apps. S4 tracks S1's capabilities and onboarding documents. S3 lands with S1.

## Relationship to the managed composition

The wizard, the connect command block with a setup code, the connections
feed, and invitations are shared with RFC 0004's managed service. What
differs there: no initial password, no password sign-in at all, Postgres
instead of SQLite, many organizations per instance, verified-domain
admission, and roles that can be derived from GitHub. The GitHub and Google
providers are the same public package in both. The
managed RFC will reference this document for screens 2 and 3 rather than
restating them.

## Appendix A: API contract shared by both compositions

Locked on 2026-09-02 so that S1, S2, S4, S5, and the managed composition
(RFC 0006) build against one shape. Every route below sits behind the
route-policy matrix; the ones marked public are the only anonymous ones.
Errors are `{"error": "<message>", "code": "<snake_case>"}`. Sessions, the
`__Host-` cookie, and the CSRF header are exactly RFC 0003's.

Both compositions expose organizations in the path. Self-hosted has exactly
one, created on screen 1; its slug defaults to a slugified organization name
and is editable there. The UI never special-cases the deployment; it reads
`deployment` from capabilities only to choose the sign-in screen.

### Capabilities, `GET /api/v1/capabilities`, public

```json
{
  "deployment": "self-hosted",
  "auth_methods": ["password", "browser_session", "github"],
  "auth_starts": {"github": "/api/v1/auth/github/start"},
  "onboarding": "admin_password",
  "features": ["project_ids", "organizations", "invitations", "setup_codes"]
}
```

`onboarding` is present only on self-hosted and only until it is `done`;
it is what lets the UI open the wizard before anyone has signed in. Managed
reports `deployment: "managed"`, no `password`, and adds `device`, `google`,
`domains`, `service_tokens`.

### Sign-in

| Route | Body → Response | Notes |
|---|---|---|
| `POST /api/v1/auth/login`, public | `{username, password}` → `{user, csrf}` + cookie | self-hosted only; rate limited 10/min per IP and per user; `password_change_required: true` in the response while the initial password is in force |
| `POST /api/v1/auth/password` | `{current, new}` → `204` | minimum 12 characters, `password_too_short` |
| `POST /api/v1/auth/logout` | → `204` | |
| `GET /api/v1/auth/me` | → `{user:{id, username, display_name, email}, orgs:[{slug, display_name, role, onboarding}], last_org}` | `onboarding` per organization is `connect`, `users`, or `done` |
| `GET /api/v1/auth/{provider}/start?invite=&return=`, public | `302` to the provider | `provider` ∈ configured methods; `invite` and `return` travel in the signed state |
| `GET /api/v1/auth/{provider}/callback`, public | `302` to `return` or to the not-invited page | admission rules in screen 3 above; managed applies RFC 0004's rules instead |
| `POST /api/v1/auth/setup-code`, public | `{code, machine_name}` → `{token, expires_at, org, server_url}` | one-time; `setup_code_invalid`, `setup_code_expired`; the token is a machine credential identical in shape to a PAT |
| `POST /api/v1/auth/device`, `POST /api/v1/auth/device/token` | RFC 0004 | managed only in beta |

### Onboarding, self-hosted only

| Route | Body → Response |
|---|---|
| `POST /api/v1/onboarding/admin` | `{org:{display_name, slug, server_url, join_policy, default_role}, admin:{username, display_name, email, new_password}}` → `{user, csrf, org}` + cookie. Requires a session obtained with the initial password, or the `admin_password` state with the initial password in the body as `current_password`. Atomic: replaces the password, creates the organization, moves state to `connect`. |

### Organizations

| Route | Body → Response | Notes |
|---|---|---|
| `POST /api/v1/orgs` | `{slug, display_name}` → org | managed only; self-hosted returns `409 single_org` |
| `GET /api/v1/orgs/{slug}` | → `{slug, display_name, server_url, join_policy, default_role, onboarding, allowed_github_orgs}` | |
| `PATCH /api/v1/orgs/{slug}` | any of the above except `onboarding` → org | admin |
| `POST /api/v1/orgs/{slug}/onboarding` | `{state}` → org | admin; only forward moves, `connect → users → done` |
| `GET /api/v1/orgs/{slug}/auth-methods` | → `{password:{enabled}, github:{enabled, client_id, base_url, has_secret, callback_url}, google:{…}, smtp:{enabled, host, port, username, from, has_password}}` | admin; secrets never returned |
| `PUT /api/v1/orgs/{slug}/auth-methods` | same shape with `client_secret` and `password` fields → `204` | admin; a missing secret field keeps the stored one |
| `POST /api/v1/orgs/{slug}/setup-codes` | `{}` → `{code, expires_at, command}` | admin; `command` is the full block the wizard prints; 15 minutes; invalidates none of the earlier codes |
| `GET /api/v1/orgs/{slug}/connections?cursor=` | → `{connections:[{project_id, display_name, remote, machine_name, connected_by, connected_at}], cursor}` | member; long-poll: holds up to 25 seconds for a new row after `cursor`, returns immediately otherwise |
| `POST /api/v1/orgs/{slug}/invitations` | `{email?, username?, org_role, grants:[{project_id, role}]}` → `{id, link, expires_at, emailed}` | admin; `org_role` ∈ `admin`, `member`; exactly one of `email`, `username` |
| `GET /api/v1/orgs/{slug}/invitations` | → list with `status` ∈ `pending`, `accepted`, `expired`, `revoked` | admin |
| `DELETE /api/v1/orgs/{slug}/invitations/{id}` | → `204` | admin |
| `GET /api/v1/orgs/{slug}/members` | → `[{user_id, username, display_name, role, methods}]` | member |
| `PATCH /api/v1/orgs/{slug}/members/{user_id}` | `{role}` → `204` | admin; the last admin cannot be demoted, `last_admin` |

### Invitations, public

| Route | Body → Response |
|---|---|
| `GET /api/v1/invitations/{token}` | → `{org_display_name, email, username, methods}`; `invitation_expired`, `invitation_revoked`, `404` otherwise |
| `POST /api/v1/invitations/{token}/accept` | `{display_name, username?, password?}` → `{user, csrf, org}` + cookie. Password required when it is the only method; with GitHub or Google the caller instead follows `auth_starts` with `?invite=`. |

### Enrollment through a setup code

`rgt connect <url> --setup <code>`:

1. `GET /api/v1/capabilities`.
2. `POST /api/v1/auth/setup-code` with the code and the machine's hostname.
   Store the token under the server URL exactly as `rgt auth login` does.
3. Proceed with the normal connect-once flow from RFC 0004 against
   `/api/v1/projects`, passing `org` from the setup-code response.
4. The server records the connection row for the feed when the enrollment
   call succeeds, whether it created or reused the project.

### Backup, self-hosted only

`POST /api/v1/admin/backup` → `application/x-tar` of `identity.db` and
`projects.db` taken with SQLite's online backup API. Admin. `rgt admin
backup [--out FILE]` calls it and writes the file, mode 0600.

## Appendix B: the `identity` package

Public, at the repository root so that the managed module can import it
(`internal/` cannot cross a module boundary).

```go
package identity

type Profile struct {
    Provider      string   // "github", "google"
    Subject       string   // provider user id, stable
    Login         string   // GitHub login; empty for Google
    DisplayName   string
    Email         string   // verified primary email, or ""
    EmailVerified bool
    Orgs          []string // GitHub organization logins, only when requested
    AvatarURL     string
}

type Config struct {
    ClientID, ClientSecret string
    BaseURL                string   // GitHub Enterprise Server; "" for github.com
    Scopes                 []string // defaults per provider
    ReadOrgs               bool     // request read:org and fill Profile.Orgs
}

type Provider interface {
    Name() string
    AuthURL(state, redirectURL string) string
    Exchange(ctx context.Context, code, redirectURL string) (Profile, error)
}

func NewGitHub(Config) Provider
func NewGoogle(Config) Provider
func NewFake(profiles map[string]Profile) Provider // code == key; for tests and the dev composition

// State carries what must survive the round trip. Signed with an HMAC key
// the composition owns; bound to the browser session nonce.
type State struct{ Nonce, Invite, Return, Provider string; IssuedAt time.Time }
type Signer interface{ Sign(State) (string, error); Verify(string) (State, error) }
func NewHMACSigner(key []byte, ttl time.Duration) Signer

// Resolver is the composition's admission logic. It receives the verified
// profile and the state and decides what happens: a session for an existing
// or new user, a link to an invitation, or a refusal with a reason the UI
// can show.
type Resolver interface {
    Resolve(ctx context.Context, p Profile, s State) (Outcome, error)
}
type Outcome struct{ UserID string; Refused bool; Reason string; Redirect string }

// Handlers wires start and callback for every configured provider under a
// prefix and is mounted by the composition. It knows nothing about users.
func Handlers(providers map[string]Provider, signer Signer, resolver Resolver, opts ...Option) http.Handler
```

Options cover the session-nonce accessor, the callback base URL, and a
rate limiter. The package has no database and no cookie of its own.
