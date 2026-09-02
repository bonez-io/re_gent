# Secure self-hosted re_gent

This guide runs the production-capable single-node re_gent composition: HTTPS,
persistent local identities, project memberships, roles, personal access
tokens, secure browser sessions, CSRF protection, an audit trail, and a
browser wizard that takes a team from one command to a working server with
teammates connected. See [RFC 0005](rfcs/0005-self-hosted-team-onboarding.md)
for the full design.

Use [`docker-compose.yml`](../docker-compose.yml) only for loopback
development. The production profile below never runs without authentication.

## Requirements

- A Linux host with Docker Engine and Docker Compose v2.
- A DNS name whose A/AAAA record points at the host.
- Inbound TCP 80 and 443 for Caddy certificate issuance and HTTPS.
- A host firewall and normal OS security updates.

## Start the server

```bash
git clone https://github.com/bonez-io/re_gent.git
cd re_gent
export REGENT_DOMAIN=regent.example.com
docker compose -f docker-compose.production.yml up -d --build
```

On an empty data volume the server creates the user `admin` with a random
20-character initial password — or with `REGENT_ADMIN_PASSWORD`, set in the
environment or `.env` beforehand, when you want a known one — and prints, to
its own stdout:

```
re_gent is ready at https://regent.example.com
Sign in as admin with the initial password: k7Qv-3mZp-...
This password must be replaced on first sign-in.
```

Read it any time with:

```bash
docker compose -f docker-compose.production.yml logs regent-server
```

That password only works until the wizard's first screen replaces it, which
bounds its exposure in the log to that window. A restart before onboarding
completes keeps the same password; it does not rotate.

## The wizard

Open `https://$REGENT_DOMAIN` and sign in as `admin` with the printed
password. The wizard has four screens; each saves on its own, so a closed tab
resumes where it stopped.

**1. Organization and admin.** Name the organization, confirm the server
address, optionally rename the admin user, and set a new password (minimum 12
characters — this replaces the initial one). Choose who can join
(`invited only` by default) and the default project role for new members
(`reader` by default). Saving this screen is what invalidates the initial
password.

**2. Connect repositories.** The screen shows one command and waits for it to
run, from any machine that can reach the server:

```bash
curl -fsSL https://regent.example.com/install | sh
rgt connect https://regent.example.com --setup 7KQ2-M9XA
```

The setup code is one-time, expires in 15 minutes, and is bound to the admin
user; `rgt connect --setup` exchanges it for a machine credential, enrolls the
repository, installs agent hooks, and carries over local history. The wizard
lists each repository the moment it enrolls. "Connect another repository"
issues a fresh code; "Continue" or "Skip for now" both move on — this screen
stays reachable later from Settings.

**3. Users.** Turn on sign-in methods (password is always on; invitation
links are on by default; GitHub and Google turn on once configured — see
below), then invite people: email or username, organization role (`admin` or
`member`), and which projects they get at which role. Without SMTP configured
each invitation shows a link to copy and send yourself.

**4. Done.** A summary, the same connect command from screen 2 (without
`--setup`) plus "sign in with your invitation link" for teammates, and links
to Settings and this guide.

## Teammate flow

1. Open the invitation link, set a display name and password (or continue
   with GitHub, when that's on), and you're signed in.
2. Install and sign in the CLI:

   ```bash
   curl -fsSL https://regent.example.com/install | sh
   rgt auth login https://regent.example.com
   ```

   This prompts for username and password (or opens the browser for GitHub,
   when that's the only method available) and stores a machine credential in
   `~/.regent/config.toml`, mode `0600`. Tokens are never shown to people.
3. Clone the repository and run `rgt connect` with no arguments — the
   committed `.regent/config.toml` already names the server and project.

`rgt init <server-url>` is accepted as an alias for `rgt connect <server-url>`
so the first command in a fresh clone and the team command read the same way.

Personal access tokens for CI and scripts remain under **Settings → Access**;
it's the only place a token is ever shown, once, at creation:

```bash
printf '%s\n' "$REGENT_PAT" | rgt auth login https://regent.example.com --token-stdin
```

`rgt auth login` never accepts a token as a command-line argument or a
`--token` flag, and refuses to send any bearer credential to a non-loopback
plain-HTTP endpoint — production login and sync require HTTPS.

## GitHub sign-in

Turn this on from the wizard's users screen (or **Settings → Access →
Authentication** later):

1. In the GitHub organization that should sign people in, go to **Settings →
   Developer settings → OAuth Apps → New OAuth App**.
2. Homepage URL: `https://regent.example.com`.
3. Authorization callback URL: exactly what the wizard's GitHub row shows,
   `https://regent.example.com/api/v1/auth/github/callback`.
4. Create the app, copy its client ID and generate a client secret.
5. Paste both into the wizard (or Settings) and save. Optionally set
   **allowed GitHub organizations** so any member of a listed org can join at
   the default role without an individual invitation, and a **GitHub
   Enterprise Server base URL** if you're not using github.com.

An invited GitHub user is admitted the moment their username or verified
primary email matches an invitation, or they already have a linked account,
or their GitHub org is on the allowed list, or the join policy is `anyone
with the server address may register`. Anyone else lands on a page that says
they're not invited and shows the admin's contact — no account is created.
Google sign-in works the same way, with a Google OAuth client instead.

## Backup and restore

```bash
rgt auth login https://regent.example.com   # once, as an admin
rgt admin backup --out regent-backup.tar
```

This downloads a consistent snapshot of `identity.db` and `projects.db`,
taken with SQLite's online backup API while the server keeps running, mode
`0600`. It's the primary way to get a restorable copy without stopping
writers or racing SQLite's WAL file. Test restores on a separate host or
Compose project before you need one for real.

If `rgt admin backup` isn't available yet in your build, the filesystem
fallback still works — stop writers first, `tar -czf` the whole `/data`
volume from a throwaway container, and reverse the same steps to restore
onto an empty volume (`find /data -mindepth 1 -delete`, extract, `chown -R
10001:10001 /data`, restart). That command deletes the volume's current
contents, so double-check the Compose project and retained backup before
running it. Restoring a backup, like restarting the stack, brings back every
user, membership, and project.

## Upgrade and rollback

**Coming from a release that used the bootstrap token.** Start the new image
on the same volume. The server finds the existing owner, adopts them as the
admin, and prints the ready message with that username and a new initial
password. Sign in with it, replace it on the first screen, and finish the
wizard; every project already on the volume shows up in the organization
you create. Tokens the owner already holds keep working throughout, so
hooks on teammates' machines never stop capturing.

Take a backup, record the current Git revision or image digest, then rebuild
and restart:

```bash
git fetch --tags
git checkout <reviewed-release-tag>
docker compose -f docker-compose.production.yml up -d --build
docker compose -f docker-compose.production.yml ps
curl -fsS "https://$REGENT_DOMAIN/healthz"
```

Rollback means checking out the recorded prior tag and rebuilding. If a
release documents an irreversible data migration, restore the matching
backup instead of running older code against newer data. The identity schema
uses additive `CREATE TABLE IF NOT EXISTS` initialization.

## Recovery

If the instance owner loses every password, PAT, and browser session, stop
the application and issue a 24-hour recovery token from the host:

```bash
docker compose -f docker-compose.production.yml stop regent-web regent-server
docker compose -f docker-compose.production.yml run --rm --no-deps regent-server \
  --data /data --recover-owner-token
docker compose -f docker-compose.production.yml up -d
```

Sign in with the printed token, set a new password in Settings, and revoke
the recovery token. Recovery refuses a directory with no existing identity
database and records the issuance as an operator audit event.

## Troubleshooting

- **Nothing printed on first start.** `docker compose -f
  docker-compose.production.yml logs regent-server` shows it any time after,
  not just on the first `up`. If the data volume already existed before this
  version, the server won't reprint or regenerate the password — it only
  runs on an empty volume. Use recovery above instead.
- **The wizard won't accept the initial password.** It's one-time: once
  screen 1 saves, that password is replaced immediately, including on a
  browser tab that still shows the sign-in form. Reload and use the new one.
- **`rgt auth login` says the initial password is still in force.** That's
  intentional — the CLI refuses to sign in a long-lived machine credential
  with a password the wizard is about to replace. Finish screen 1 in the
  browser first.
- **A setup code doesn't work.** Codes are one-time and expire in 15 minutes;
  the wizard's "Connect another repository" issues a fresh one.
- **GitHub sign-in refuses an invited user.** Check that the callback URL
  pasted into the GitHub OAuth App matches the server address exactly
  (including scheme), and that the invitation's email or username matches
  the GitHub account's login or a verified primary email.
- **Health check.** `curl -fsS https://$REGENT_DOMAIN/healthz` should return
  quickly; `/api/v1/capabilities` (also public) reports the current
  `onboarding` state, useful for confirming the wizard thinks it's further
  along than the browser does.

## Security notes and current limits

- The Go server is not published on a host port in the production profile.
  Caddy is the only public service and terminates TLS automatically.
- `/healthz`, `/install`, `/install.sh`, `/bin/rgt`, `/api/v1/capabilities`,
  sign-in, the OAuth start and callback routes, and invitation acceptance are
  the only public routes. Every other route denies anonymous access.
- Login and setup-code attempts are rate-limited in-process. Put an external
  rate limit or WAF in front for an internet-facing deployment with
  substantial untrusted traffic.
- This beta is single-node, backed by SQLite. It does not provide SAML/SCIM,
  OpenID Connect, HA, or a multi-region control plane.
- Caddy's automatic public certificates require a resolvable public
  hostname. Private-network deployments should replace the Caddy TLS policy
  with their organization's trusted certificate process.
