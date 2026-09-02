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
| Who can join | `invited only` | Alternative: `anyone with the server address may register`, off by default. |
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
- The UI listens on `GET /api/v1/onboarding/connections` (long-poll or SSE)
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
| Single sign-on, OpenID Connect | planned, shown disabled with "coming soon" | an identity provider; implemented once the managed composition's provider code exists, shared with it |

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

1. Opens the invitation link. Sets a display name and a password. Is signed in.
2. Runs the command from the invitation page: install `rgt`, then
   `rgt auth login <server>`, which prompts for username and password and
   stores a machine credential. Tokens are never shown to people.
3. Clones the repository and runs `rgt connect` with no arguments; the
   committed `.regent/config.toml` names the server and project.

Personal access tokens remain available under Settings for CI and scripts,
and are the only thing that page shows.

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
- a connections feed for the wizard: `GET /api/v1/onboarding/connections`;
- removal of the bootstrap file and route.

Routes are added to the route-policy matrix with the same fail-closed rule as
every other route: only sign-in, invitation acceptance, capabilities, health,
and install are public.

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
- The conformance suite still passes; every new route denies anonymous access
  except the five public ones listed above.
- No password, initial password, setup code, or invitation token appears in
  audit rows, application logs after first start, or API responses other than
  the one that created it.

## Work breakdown

| Stream | Owns | Deliverable |
|---|---|---|
| **S1. Server** | `selfhosted/`, `internal/server/` routes for onboarding, `serverauth/` if new actions | passwords, initial admin, onboarding state, organization, setup codes, invitations, connections feed, bootstrap removal, conformance updates |
| **S2. CLI** | `internal/cli/auth.go`, `internal/cli/connect.go`, `internal/remote/`, `internal/config/` | `--setup`, `init` alias, password login, remotetest endpoints |
| **S3. Compose and docs** | compose files, `.env.example`, `docs/self-hosted.md`, `scripts/dev-bootstrap.sh` | first-start message, env documentation, rewritten guide, dev bootstrap through the new routes |
| **S4. UI** | `web/` | sign-in with password, the four wizard screens, invitation acceptance page, Settings entry points |

S1 and S2 agree the setup-code exchange and the onboarding state names on day
one and then proceed in parallel. S4 tracks S1's capabilities and onboarding
documents. S3 lands with S1.

## Relationship to the managed composition

The wizard, the connect command block with a setup code, the connections
feed, and invitations are shared with RFC 0004's managed service. What
differs there: no initial password, sign-in through GitHub or Google, many
organizations per instance, and roles that can be derived from GitHub. The
managed RFC will reference this document for screens 2 and 3 rather than
restating them.
