# Secure self-hosted re_gent

This guide runs the production-capable single-node re_gent composition: HTTPS,
persistent local identities, project memberships, fixed roles, personal access
tokens, secure browser sessions, CSRF protection, and an audit trail for access
mutations.

Use [`docker-compose.yml`](../docker-compose.yml) only for loopback development.
It intentionally passes `--insecure-no-auth`. The production profile never does.

## Requirements

- A Linux host with Docker Engine and Docker Compose v2.
- A DNS name whose A/AAAA record points at the host.
- Inbound TCP 80 and 443 for Caddy certificate issuance and HTTPS.
- A host firewall and normal OS security updates.

## Install and create the first owner

```bash
git clone https://github.com/bonez-io/re_gent.git
cd re_gent
export REGENT_DOMAIN=regent.example.com
docker compose -f docker-compose.production.yml up -d --build
docker compose -f docker-compose.production.yml exec regent-server \
  sh -c 'cat /data/bootstrap-token'
```

On a new data volume, the server writes one bootstrap credential to
`/data/bootstrap-token` with mode `0600`; it never writes the secret to the
container log. The identity database stores only its hash. The credential
rotates if the unclaimed server restarts, and its delivery file is deleted as
soon as setup succeeds. Open `https://$REGENT_DOMAIN`, enter that credential,
and create the first owner. Save the initial personal access token when it is
shown; plaintext tokens cannot be recovered later.

The browser exchanges a PAT for a `Secure`, `HttpOnly`, `SameSite=Strict`
session cookie. Cookie-authenticated mutations also require a per-session CSRF
token. The static UI does not contain or persist a bearer token.

## Sign in the CLI and connect a project

`rgt auth login` never accepts a token as an argument or `--token` flag. In an
interactive terminal it reads a hidden prompt:

```bash
rgt auth login https://regent.example.com
rgt auth status

cd ~/code/my-project
rgt connect https://regent.example.com
```

For automation, provide the secret on stdin rather than in the process list:

```bash
printf '%s\n' "$REGENT_PAT" | rgt auth login https://regent.example.com --token-stdin
```

The machine credential is keyed by server and stored in
`~/.regent/config.toml` with mode `0600`. The repository's portable
`.regent/config.toml` contains only its server URL and project binding; it never
contains a credential. `REGENT_TOKEN` remains an explicit process-scoped
override for CI. The CLI refuses to send any bearer credential to a non-loopback
plain-HTTP endpoint; production login and sync require HTTPS.

## Users, roles, and tokens

Open **Settings → Access** in a project to:

- create a local user and copy their initial token once;
- add an existing user to the project;
- assign `owner`, `admin`, `writer`, or `reader`;
- rotate and revoke your personal access tokens.

Readers can browse history, objects, refs, files, blame, and members. Writers
can also ingest captured work. Admins can manage ordinary memberships. Project
owners can manage owner memberships. The first instance owner can create
projects and administer every project. Cross-project requests from a user with
no membership return `404` so identifiers do not disclose existence.

Security-sensitive mutations and session issuance are committed with an audit
event in `/data/identity.db`. PAT and session secrets are SHA-256 hashed before
storage; the inputs are random 256-bit values, not passwords. The temporary
first-start delivery file described above is the only plaintext bootstrap copy
and disappears after it is claimed.

## Operator recovery

If the instance owner loses every PAT and browser session, stop the application
and issue a 24-hour recovery PAT from the host:

```bash
docker compose -f docker-compose.production.yml stop regent-web regent-server
docker compose -f docker-compose.production.yml run --rm --no-deps regent-server \
  --data /data --recover-owner-token
docker compose -f docker-compose.production.yml up -d
```

Sign in with the printed token, create a replacement in Access settings, and
revoke the recovery token. Recovery refuses a directory with no existing
identity database and records the issuance as an operator audit event.

## Backup and restore

Repository objects, refs, indexes, identities, memberships, credential hashes,
and audit events all live in the `regent-data` volume. Stop writers before a
filesystem backup so SQLite and refs are one consistent point in time:

```bash
mkdir -p backups
docker compose -f docker-compose.production.yml stop regent-web regent-server
docker compose -f docker-compose.production.yml run --rm --no-deps --user 0 \
  -v "$PWD/backups:/backup" --entrypoint sh regent-server \
  -c 'tar -czf /backup/regent-data.tgz -C /data .'
docker compose -f docker-compose.production.yml up -d
```

Test restores on a separate host or Compose project. To replace the active
volume, keep the application stopped, retain a copy of the current volume, then
restore explicitly:

```bash
docker compose -f docker-compose.production.yml stop regent-web regent-server
docker compose -f docker-compose.production.yml run --rm --no-deps --user 0 \
  -v "$PWD/backups:/backup:ro" --entrypoint sh regent-server \
  -c 'find /data -mindepth 1 -delete && tar -xzf /backup/regent-data.tgz -C /data && chown -R 10001:10001 /data'
docker compose -f docker-compose.production.yml up -d
```

That restore command deletes the current contents of the selected Compose
volume. Verify the Compose project, archive name, and retained backup before
running it.

## Upgrade and rollback

Create a backup, record the current Git revision or image digest, then rebuild
and restart:

```bash
git fetch --tags
git checkout <reviewed-release-tag>
docker compose -f docker-compose.production.yml up -d --build
docker compose -f docker-compose.production.yml ps
curl -fsS "https://$REGENT_DOMAIN/healthz"
```

Rollback means checking out the recorded prior tag and rebuilding. If a future
release documents an irreversible data migration, restore the matching backup
instead of running older code against newer data. The current identity schema
uses additive `CREATE TABLE IF NOT EXISTS` initialization.

## Security notes and current limits

- The Go server is not published on a host port in the production profile.
  Caddy is the only public service and terminates TLS automatically.
- `/healthz`, `/install`, `/install.sh`, `/bin/rgt`, and the non-secret
  `/api/v1/capabilities` document are public. Repository, identity, settings,
  token, and skill data require authentication.
- Login and bootstrap attempts are rate-limited in-process. Put an external
  rate limit or WAF in front for an internet-facing deployment with substantial
  untrusted traffic.
- This beta is single-node. It does not provide SAML/SCIM, OIDC, HA, or a
  multi-region control plane.
- Caddy's automatic public certificates require a resolvable public hostname.
  Private-network deployments should replace the Caddy TLS policy with their
  organization's trusted certificate process.
