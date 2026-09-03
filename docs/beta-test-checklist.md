# Beta test checklist

Manual acceptance for the self-hosted team mode (RFC 0005) and the managed
composition (RFC 0006). Tick each box in order; every step names the exact
command or URL and what must be true afterward. Local ports below match the
`.env` files checked into neither repo: self-hosted on `8081`, managed on
`8091`.

Prerequisites: Docker Desktop running, `bin/rgt` built (`make bin/rgt`),
`jq` and `curl` available.

## A. Self-hosted, fresh install

- [ ] A1 `docker compose down -v && docker compose up -d --build` in this repo.
      `docker compose logs server` shows the three-line ready message with
      `http://127.0.0.1:8081`, `admin`, and a 20-character password.
- [ ] A2 `curl -s http://127.0.0.1:8081/api/v1/capabilities | jq` shows
      `"deployment":"self-hosted"` and `"onboarding":"admin_password"`.
- [ ] A3 Open http://127.0.0.1:8081. Sign in as `admin` with the printed
      password. The wizard opens on screen 1: organization, new password,
      defaults. (`scripts/dev-bootstrap.sh` does the same calls headlessly.)
- [ ] A4 After screen 1, the printed password is refused (401) and
      capabilities show `"onboarding":"connect"`.
- [ ] A5 Restart with `docker compose restart server`. Logs say the initial
      password is no longer in force; nothing is reprinted.

## B. Self-hosted, connect a repository

- [ ] B1 Mint a code: `POST /api/v1/orgs/<slug>/setup-codes` (screen 2, or
      curl with the admin session). Response contains `code` and `command`.
- [ ] B2 In a fresh clone: `rgt connect http://127.0.0.1:8081 --setup <code>`.
      Output ends with `Ready to capture`; `.regent/config.toml` has a
      `project_id = 'prj_…'` line.
- [ ] B3 `GET /api/v1/orgs/<slug>/connections` lists the repository with its
      remote and your machine name.
- [ ] B4 Run the same connect command again with the same code. It fails with
      "this setup code is invalid".
- [ ] B5 Wait 15 minutes on a fresh code, then use it. It fails with
      "expired".
- [ ] B6 Run an agent turn in the connected repo, then `rgt log` shows the step
      and `rgt sync` reports nothing queued.

## C. Self-hosted, users

- [ ] C1 Create an invitation (screen 3 or `POST /api/v1/orgs/<slug>/invitations`
      with `{"email":"…","org_role":"member","grants":[]}`). Response has
      `link` and `emailed:false` without SMTP.
- [ ] C2 Open the link in a private window. Set display name and password.
      You are signed in and `GET /api/v1/auth/me` lists the organization with
      role `member`.
- [ ] C3 As the member, `GET /api/v1/orgs/<slug>/invitations` is 403.
- [ ] C4 As the member, `rgt auth login http://127.0.0.1:8081` prompts for
      username and password and prints "Signed in as …". `rgt connect` in a
      clone of a connected repository works without a code.
- [ ] C5 Demote the last admin (`PATCH /api/v1/orgs/<slug>/members/<id>`
      `{"role":"member"}`). It is refused with `last_admin`.
- [ ] C6 Configure GitHub sign-in (`PUT /api/v1/orgs/<slug>/auth-methods`)
      with a real OAuth App. An invited GitHub user signs in through
      "Continue with GitHub"; an uninvited one lands on the not-invited page
      and no account exists afterward.

## D. Self-hosted, backup and upgrade

- [ ] D1 `rgt admin backup --out backup.tar` writes a tar with `identity.db`
      and `projects.db`, mode 0600.
- [ ] D2 Restore onto an empty volume (docs/self-hosted.md, "Backup and
      restore"). Every user, membership, and project is back.
- [ ] D3 Upgrade from a bootstrap-token volume (this repo's default volume is
      one): the new image adopts the existing owner, prints a password for
      that username, the owner's old token still lists projects, and after
      screen 1 all projects appear in the organization.

## E. Managed, local dev stack

- [ ] E1 In `~/Projects/re_gent-cloud`: `docker compose -f deploy/docker-compose.dev.yml -p regent-cloud --env-file .env up -d --build`.
      `curl -s http://127.0.0.1:8091/api/v1/capabilities | jq` shows
      `"deployment":"managed"` and `dev` among `auth_methods`.
- [ ] E2 Open http://127.0.0.1:8091, enter any email under "Dev sign-in".
      You land on "Create an organization".
- [ ] E3 Create the organization. The wizard opens on "Connect repositories"
      with a command block and "Listening for connected projects".
- [ ] E4 Run the command block's `rgt connect … --setup <code>` in a clone.
      The repository appears on the screen within two seconds, with its
      display name. Continue.
- [ ] E5 On "Users", invite `dana@…` and copy the link. Sign out, open the
      link, "Continue with Dev". dana lands in the app as a `member` and is
      not sent into the wizard.
- [ ] E6 Sign in as an uninvited user. `auth/me` has no orgs;
      `GET /api/v1/orgs/acme/projects` is 404.
- [ ] E7 That user creates `other`, mints a code, enrolls the same repository.
      A different `prj_` id is returned; neither org sees the other's project.
- [ ] E8 `rgt auth login http://127.0.0.1:8091` prints a URL and code. Approve
      with `POST /api/v1/auth/device/approve {"user_code":"…","approve":true}`
      from the owner's session. The CLI prints "Signed in" and stores an
      access and a refresh token; `rgt connect --org acme` works with it.
- [ ] E9 Create a fourth organization as the same user. Refused with
      `quota_exceeded` and a reason.

## F. Both stacks together

- [ ] F1 Self-hosted on 8081 and managed on 8091 run at the same time
      (`docker ps` shows both server containers healthy).
- [ ] F2 `go test ./...` in this repo and `make test` in the cloud repo are
      green; `golangci-lint run ./...` is clean here.

## G. UI (owner: Shay)

- [ ] G1 Sign-in page shows username and password on self-hosted, provider
      buttons on managed, driven by capabilities only.
- [ ] G2 Wizard screens 1 to 4 per RFC 0005, resuming from the saved state.
- [ ] G3 Connections list updates within two seconds of `rgt connect`.
- [ ] G4 Invitation acceptance page; not-invited page.
- [ ] G5 Project picker shows display names from `/api/v1/projects`.
