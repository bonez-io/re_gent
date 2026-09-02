# re_gent v1.2 beta release plan

> Status: active release source of truth
>
> Last reviewed: 2026-09-02
>
> Target: `v1.2.0-beta.3` OSS release plus the first free managed beta
>
> Current integration branch: `dev`

This plan turns the current `re_gent_headless` line into the next canonical
re_gent release. It covers the public CLI and self-hosted product, a private
managed service, the web application, search and settings, skills, complete
documentation, repository migration, and the public launch.

The target is a **production-safe beta**, not a claim of general availability.
Real users must be able to trust its authentication, authorization, durability,
backup, restore, and migration behavior. The beta may remain single-region and
carry no uptime SLA, but it may not expose an open data server or rely on an
untested recovery story.

## Execution status

Completed through 2026-09-02:

- Transferred and renamed the successor repository to `bonez-io/re_gent` while
  preserving repository identity, branches, tags, issues, the open pull request,
  environments, and Actions variables.
- Imported the old stable tags with their original object IDs and preserved both
  existing beta tags without moving them.
- Transferred the Homebrew tap to `bonez-io/homebrew-tap`.
- Created private `bonez-io/re_gent-cloud` with an explicit public/private
  boundary, CODEOWNERS, passing baseline CI, protected `main`, vulnerability
  alerts, secret scanning, and push protection.
- Created the `v1.2 beta` milestone and enabled Discussions, vulnerability
  reporting, secret scanning, push protection, and release-oriented repository
  settings on the public repository.
- Reconciled the existing issue epics into the milestone and created the
  [`v1.2.0-beta.3` release tracker](https://github.com/bonez-io/re_gent/issues/96).
- Merged the
  [release and authorization foundation](https://github.com/bonez-io/re_gent/pull/99)
  into `dev`: canonical namespaces, public server embedding and authorization
  contracts, route-level conformance tests, secure non-loopback startup, and
  accepted RFC 0003.
- Merged the
  [secure self-hosted access slice](https://github.com/bonez-io/re_gent/pull/100)
  into `dev` after fresh Linux, macOS, lint, build, and UI CI passed:

  - secure-by-default server composition with first-owner bootstrap,
    hashed PATs, browser sessions/CSRF, project memberships and roles,
    transactional audit events, rate limits, and operator recovery;
  - server-scoped `rgt auth login/status/logout` credential lifecycle with no
    secret in process arguments or repository configuration;
  - authenticated UI setup/login plus real user, membership, role, and token
    settings APIs; and
  - a Caddy HTTPS production Compose profile and self-hosted operations guide.

Current operator blockers:

- [GCP Workload Identity Federation](https://github.com/bonez-io/re_gent/issues/97)
  still trusts the previous GitHub repository
  claim. Applying the Terraform migration requires an interactive `gcloud`
  reauthentication, then a reviewed plan and `infra/gcp/configure-github.sh`.
- The [Homebrew release credential](https://github.com/bonez-io/re_gent/issues/98)
  exists only in the old official repository; GitHub does not expose
  secret values and the successor repository has no copy. A new fine-grained
  token or GitHub App credential must be configured before a release can update
  the transferred tap.

## 1. Audited baseline

The plan starts from observed repository and GitHub state, not the older roadmap.

| Area | Current state | Release implication |
|---|---|---|
| Repository history | `regent-vcs/re_gent_headless` is a direct descendant of `regent-vcs/re_gent`: its current `main` is 107 commits ahead of the old official `main` and has the old tip as an ancestor. | Transfer the new repository rather than rebuilding history or force-pushing the old one. |
| Published version | The old official repository has a latest GitHub Release of `v1.1.0`. The new repository has tags `v1.2.0-beta.1` and `.2`, but no GitHub Release. `.1` is in current history; `.2` is on a divergent historical branch. | Preserve every immutable tag, prove the `.2` wire-protocol fixes exist in current code, and cut `.3` from the new canonical repository. |
| Release automation | GoReleaser and helper scripts still target `regent-vcs/re_gent`; the release helper checks `develop` rather than `dev` and its version regex rejects a suffix such as `beta.3`. | Repair and test release automation before creating any new tag. |
| CLI/core | The new line adds remote connect/push/pull/sync, rewind, merge, repair, doctor, agent wiring, the server, read APIs, usage accounting, and skills. | This is a release successor, not a sidecar or experimental rewrite. |
| Server security | `regent-server` has route and path validation, object size limits, CAS refs, and per-repository directories, but no application authentication, tenant model, or route authorization. | No public managed endpoint can launch from this binary as-is. |
| Client auth plumbing | The Go and browser clients can send bearer tokens, and user config can store one, but the current CLI deliberately has no sign-in command because the server is open. | Reuse the transport plumbing, replace the incomplete credential lifecycle, and add a real auth contract. |
| Existing deployment | GCP infrastructure builds and deploys immutable server/UI images to private VMs with persistent disks, snapshots, rollback, and IAP-only access. Recent `dev` and `main` workflows passed. | Keep this as a staging/operations baseline. IAP is operator access, not the managed product's user authentication. |
| Self-hosted path | Docker Compose is explicitly local-only, loopback-bound, and unauthenticated. | Add a separate production profile with secure defaults, users, TLS guidance, upgrades, backup, and restore. |
| UI | The app has repository selection, Sessions, metadata search and filters, transcript/step detail, file tree/blame, Team, Skills, responsive panels, Storybook, and error/empty states. | Build on this foundation. Do not describe it as absent, but do not confuse local metadata filtering with full history search. |
| UI scale/readability | A live visual pass showed an unbounded repository picker with no search, raw repository identifiers, prompt-length session titles, and transcript layouts that become hard to scan on a wide desktop. | Add searchable/paginated project selection, display names, concise generated session summaries, and explicit readability targets. |
| Search/history | Session title/user/agent/date filtering exists. “Semantic” mode is a metadata fallback. There is no indexed full-text search across prompts, responses, tool calls, files, and steps, nor a global activity/history surface. | Indexed keyword history search is required for beta; semantic search is not. |
| Settings | General, Users, and Data pages are static, explicitly read-only placeholders. | Settings APIs and real mutation/audit flows are release work. |
| Skills | Ten skills are embedded; one (`rewind`) is withheld from default installation because its copy over-promises. Registry, CLI list/install, host-aware installation, and UI tests exist. A local clean-project smoke installed skills successfully. | Treat the implementation as a working foundation. Fix truthfulness, trust, verification, examples, and end-to-end host acceptance. |
| Skills UI | The server registry is authoritative when reachable, but the offline fallback mixes real skills with three proposals and cannot know what is installed on a user's machine. | Replace “installed” claims with accurate availability/source state and separate proposals from installable skills. |
| Docs | There are 11 Markdown files under `docs/`, a large README, and useful RFCs, but no complete user docs site. Several links, release targets, roadmap items, and module paths still point at different repositories. | Build a versioned docs site and generate or test reference material against the released binary. |
| OSS governance | CI passes, but the new repository's `main` and `dev` branches are not protected, there is no release milestone, Discussions are disabled, and community health is 75%. The old official repository still has open issues and PRs. | Governance, issue triage, security policy, release provenance, and migration must land before announcement. |
| Current verification | On 2026-09-01, `go test -count=1 ./...`, `go vet ./...`, UI lint, production build, 23 Storybook files/131 tests, and the static Storybook build passed. UI lint retained two Fast Refresh warnings and Vite reported large chunks. | The current foundation is testable, but the release needs the additional security, race, E2E, migration, restore, and performance gates below. |
| Performance | Large payload rendering has been bounded, but prior large-repository capture took more than five minutes and did not produce a usable partial session. | A repeatable large-repository capture benchmark and latency budget are release blockers. |

## 2. Release definition

The release is one coordinated product launch with two distributions.

### Public OSS distribution

- `rgt` CLI and supported agent adapters.
- The protocol, object model, read APIs, search index, and reusable server core.
- A secure, production-capable single-node self-hosted server and web UI.
- First-party skills and cookbooks.
- Complete user, operator, contributor, protocol, and security documentation.
- Apache 2.0 licensing remains unchanged.

### Private managed distribution

- A Bonez-operated service at the managed production domain.
- GitHub OAuth for browser accounts and browser/device login for the CLI.
- Organizations, projects, memberships, roles, invitations, API/service tokens,
  audit events, quotas, support operations, and deletion/export flows.
- Managed deployment, secrets, backups, restore, monitoring, alerting, incident
  runbooks, and cost controls.
- A free beta plan with no credit card.

### Beta boundaries

- The managed beta is single-region and has no formal uptime SLA.
- The managed catalog is curated first-party content. Third-party skill uploads
  and a public marketplace are post-beta.
- Full-text keyword search is required. Embedding-based semantic search is
  post-beta unless it meets privacy, cost, deletion, and relevance gates early.
- The UI remains non-destructive for history. Rewind/merge stay explicit CLI
  operations for this release.
- Secondary-parent DAG work, live conversation rewind, garbage collection, and
  broad new agent-adapter coverage are not allowed to delay this beta unless
  testing proves one is required for data safety.

## 3. Locked architecture decisions

### 3.1 One public core, two product builds

The managed service will be private, but it will not fork re_gent.

The public `bonez-io/re_gent` repository owns:

- CLI and agent adapters;
- object, tree, step, blame, ref, and protocol formats;
- reusable protocol/read/search server packages;
- authentication and authorization interfaces;
- filesystem/SQLite storage and the self-hosted implementation;
- shared web application components and APIs;
- public docs, migrations, compatibility tests, and skills.

The private `bonez-io/re_gent-cloud` repository owns:

- the managed service entrypoint and composition;
- hosted identity provider integration;
- organization/tenant control data and policy;
- managed quotas, abuse controls, operational admin tools, and future billing;
- cloud-only infrastructure, secrets, alert routing, incident procedures, and
  customer support operations.

The private build must consume an immutable public core version. Copying public
packages into the private repository or maintaining a second protocol handler is
forbidden. To enable this, reusable server code currently under `internal/server`
must move behind a supported public package boundary with focused conformance
tests. The managed repository pins a release or commit and upgrades deliberately.

### 3.2 Authentication and authorization

All repository data routes are deny-by-default in production.

- Browser sessions use secure, HTTP-only, same-site cookies and CSRF protection.
- Managed accounts use GitHub OAuth for the first beta.
- The CLI gains `rgt auth login`, `rgt auth status`, and `rgt auth logout`.
- Interactive CLI login uses a browser/device flow. Automation uses revocable,
  scoped service tokens.
- Credentials are keyed by server/account and stored in the OS credential store;
  mode-`0600` config is a documented fallback, not the preferred path.
- Repository `.regent/config.toml` bindings remain portable and contain no
  credential.
- Roles are `owner`, `admin`, `writer`, and `reader`. Capture requires `writer`;
  browsing requires `reader`; membership/token/settings changes require
  `admin` or `owner` as defined by policy.
- Every list, object, ref, read, search, skills, settings, and admin route has an
  explicit policy decision and cross-tenant denial coverage.
- Tokens are only shown once, stored as hashes server-side, scoped, expiring when
  appropriate, revocable, and audited.
- `healthz` stays public. Installer/binary access may stay public because it
  contains no customer data. Team-published skills require authorization.

Self-hosted beta supports an initial local admin bootstrap, local users, and
personal/service tokens. Optional OIDC is a follow-up unless it can reuse the
managed interface without delaying the beta.

Open loopback mode remains available for development. Binding a production
server to a non-loopback address without authentication must fail unless an
explicit, loudly named insecure override is supplied.

### 3.3 Protocol and compatibility

- Introduce a versioned `/api/v1` contract and a capability endpoint before
  requiring auth.
- Keep legacy routes for one beta compatibility window and emit deprecation
  metadata; remove them only through a documented later release.
- Self-hosted and managed deployments implement the same protocol conformance
  suite.
- Project identity becomes server-generated and immutable; human names are
  mutable display metadata.
- Connect/import is atomic: history is uploaded and verified before the portable
  binding changes.
- A two-machine acceptance suite proves log, transcript, tool call, file, blame,
  author, usage, and ordering fidelity after pull.

### 3.4 Managed beta storage and operations

The first managed beta reuses the current content-addressed filesystem/SQLite
engine on an encrypted persistent data disk. This is intentionally a
single-primary architecture for the beta, with project directories isolated
behind tenant authorization. A storage interface must keep an object-storage or
database-backed implementation possible without changing the protocol.

Required operating contract:

- immutable image deploys with automatic rollback;
- hourly managed snapshots, encrypted backup copies, and a tested restore drill;
- documented RPO/RTO for the beta;
- staging and production isolation;
- structured audit and application logs with secret/content redaction;
- health, error, latency, queue, storage, quota, and backup-age metrics;
- rate limits and bounded request/object/search work;
- account/project export and deletion, including backups under the documented
  deletion window;
- a status page, incident ownership, and operator runbooks.

Initial free-plan defaults are 3 projects, 5 members, and 1 GiB stored data per
account, with no automatic time-based deletion. Crossing the storage quota stops
remote ingestion with a clear error while local capture remains safely spooled.
These values may be lowered before launch only from measured capacity/cost data
and must be reflected in product copy and docs.

### 3.5 Self-hosted production profile

The self-hosted release is a secure single-node product, not the open local
Compose file exposed to the internet.

- Keep `docker-compose.yml` as the loopback development path.
- Add a production Compose profile with pinned images, persistent named/bind
  storage, health checks, resource limits, secrets, an authenticated first-run
  flow, and a supported reverse-proxy/TLS configuration.
- Add tested install, upgrade, rollback, backup, restore, export, and disaster
  recovery commands/runbooks.
- Expose real server settings: server identity/base URL, registration policy,
  users, project access, tokens, retention/quota, backup state, and diagnostics.
- Publish a compatibility matrix between CLI, server, UI, protocol, and storage
  schema versions.
- Run a clean-host smoke test against each release artifact.

### 3.6 Search and UI information architecture

The beta navigation is:

- **Home** — projects, connection health, recent activity, onboarding progress.
- **History** — sessions and step activity with indexed search and filters.
- **Code** — tree at a selected step, file history, diffs, and blame-to-prompt.
- **Team** — members, roles, invitations, tokens, and activity.
- **Skills** — curated catalog, trust/source/version details, commands, and
  verified cookbooks.
- **Settings** — General, Access, Data, and Diagnostics backed by real APIs.

Search is server-side SQLite FTS over normalized prompt/assistant text, tool
names and bounded textual results, session titles/ids, authors/agents, step ids,
and file paths. Binary/base64 payloads and secrets are not indexed. Results are
permission-scoped, paginated, deep-linkable, and explain which field matched.

The History view must support:

- repository-wide keyword search;
- user, agent, date, branch/ref, path, and event-type filters;
- recent/oldest/relevance ordering;
- shareable authenticated deep links;
- virtualized long sessions and bounded tool/diff payloads;
- direct navigation from a blamed line or search result to the producing step
  and conversation context.

Semantic search must not appear as working until there is a real index. The
current metadata fallback should be relabeled or removed for beta.

### 3.7 Skills product and trust model

For the beta, the catalog contains only reviewed first-party skills.

- The embedded files remain the canonical bytes for built-in skills.
- Every catalog entry exposes name, version/content hash, source, tool grant,
  supported hosts, arguments, examples, and withheld/deprecated state.
- The UI says **available**, **built in**, **team-published**, or **withheld**. It
  does not claim a skill is installed on a local machine it cannot inspect.
- Proposed skills are separated from the installable registry.
- `rgt skill doctor` verifies installed bytes, host location, tool grants, and
  restart requirements.
- CLI, registry, UI, and checked-in skill copies have exact parity tests.
- Release acceptance covers Claude Code, Codex, OpenCode, and Pi installation;
  at least Claude Code and Codex receive a real agent-session smoke test.

Required first-party cookbooks:

1. Trace a regression from a failing line to its prompt and tool calls.
2. Prime an agent with the history of an unfamiliar file before editing.
3. Find files that repeatedly change together.
4. Build a PR/release narrative from captured work.
5. Audit token usage by session and agent.
6. Create and review a custom skill with a minimal tool grant.

Each cookbook includes a sample repository/fixture, expected commands and output,
supported hosts, failure modes, privacy notes, and an automated or recorded
acceptance run.

### 3.8 Documentation platform and structure

Documentation source stays in the canonical product repository for the beta so
code, CLI help, API definitions, migrations, and docs ship from the same tag.
Issue #56's separate-repository assumption should be superseded.

Build a Docusaurus docs-only site under `docs-site/` and deploy it at the release
docs domain. Keep only the current beta and the latest stable docs active; older
versions may be immutable archives. The site must generate `llms.txt`, support
search, copyable commands, edit links, previews, broken-link checks, and a clear
version banner.

The information architecture follows the useful journey-first pattern visible in
[Entire's documentation index](https://docs.entire.io/llms.txt), while using
re_gent's own vocabulary and behavior:

- Overview and concepts.
- Five-minute quickstart.
- Installation, update, uninstall, and release channels.
- Local, self-hosted, and managed getting-started paths.
- Agent guides: Claude Code, Codex, OpenCode, and Pi, with capability matrix.
- History, blame, rewind, collaboration, search, and team workflows.
- Skills overview, catalog, trust model, and cookbooks.
- CLI reference generated from the released Cobra command tree.
- HTTP/OpenAPI and agent-adapter reference.
- Server administration: configuration, TLS, users, tokens, backup, restore,
  upgrades, monitoring, and troubleshooting.
- Security, privacy, data location, redaction, retention, export, and deletion.
- Contributor architecture, testing, governance, and release process.
- Migration guides, release notes, FAQ, glossary, and support.

Every getting-started page contains prerequisites, exact commands, expected
output, verification, cleanup, and “what next.” Docs commands are exercised by CI
against the released behavior rather than copied from old README text.

### 3.9 Canonical repository migration

The canonical public repository becomes `bonez-io/re_gent` before the bulk of
release implementation, so new code and docs do not accumulate another set of
stale paths.

Migration sequence:

1. Export repository settings, issue/PR lists, releases, tags, branch refs,
   Actions variables/environments, packages, webhooks, and deploy configuration.
2. Import the old official repository's stable tags into the new descendant and
   verify each tag still names the original commit.
3. Prove the `v1.2.0-beta.2` protocol fixes are present in current behavior; do
   not move or recreate the immutable historical tag.
4. Transfer `regent-vcs/re_gent_headless` to `bonez-io` and rename it `re_gent`,
   preserving its issues, PRs, refs, and history.
5. Change the Go module/import path to `github.com/bonez-io/re_gent` and update
   GoReleaser, Homebrew, GHCR, badges, install scripts, examples, infrastructure,
   docs, links, domains, and agent/plugin repositories.
6. Recreate historical release pages/assets in the canonical repository where
   checksums can be verified; otherwise link clearly to the archived source.
7. Triage every open issue and PR in the old official repository: close as fixed
   or obsolete, or recreate it in the canonical tracker with backlinks.
8. Leave `regent-vcs/re_gent` read-only with a migration banner until the beta is
   live, then archive it. Do not delete it.
9. Add redirects or compatibility notes for old `go install`, Homebrew, Docker,
   VS Code, OpenCode, Pi, website, and API endpoints.

The release tag is `v1.2.0-beta.3`. It is cut only from protected `main` after a
release-candidate soak. The free managed beta may carry a separate deployment
version, but its UI and protocol report the compatible public core version.

## 4. Workstreams and acceptance

### W0 — Release control and repository reconciliation

Deliverables:

- Approve this plan and convert it into a GitHub `v1.2 beta` milestone.
- Reconcile the 44 current open issues and the old official repository's open
  issues/PRs against this plan.
- Protect `dev` and `main`; require CI, review, linear/known merge policy, and
  signed or otherwise attributable release tags.
- Make `dev` the integration branch and `main` release-only.
- Resolve or close stale PR #89 deliberately.
- Add `SECURITY.md`, maintainers/governance, support policy, and a private
  vulnerability reporting route.
- Add an automated release audit for repository names, links, generated docs,
  version stamps, artifacts, SBOM, checksums, signatures/provenance, and images.

Exit gate: one canonical milestone, no ambiguous release repository, protected
branches, and no stale issue status presented as current truth.

### W1 — Protocol, security contract, and public server core

Deliverables:

- Accepted auth/tenancy threat model and public/private boundary RFC.
- `/api/v1` capabilities, project ids, atomic connect/import, and compatibility.
- Extracted public server-core boundary with authn/authz/storage interfaces.
- Local self-hosted identity/token provider and deny-by-default middleware.
- Authenticated conformance, cross-tenant, abuse, traversal, CSRF, XSS, token,
  and resource-exhaustion tests.
- Large-repository capture benchmark and incremental-snapshot work sufficient to
  keep agent hooks within the agreed latency budget.

Exit gate: self-hosted and managed compositions pass the same protocol/security
suite, and an auth bypass or multi-minute capture is a release blocker.

### W2 — Private managed beta

Deliverables:

- Private `bonez-io/re_gent-cloud` repository with restricted ownership, CI,
  secret scanning, dependency policy, and public-core pinning.
- GitHub OAuth, CLI login/device flow, memberships/roles/invites, scoped tokens,
  audit log, quotas, export, and deletion.
- Production GCP environment with public HTTPS entrypoint, WAF/rate limits as
  appropriate, encrypted state, snapshots, restore, monitoring, alerting,
  rollback, status page, and runbooks.
- Free-plan enforcement that never discards locally captured work.
- Staging soak and production restore/incident exercises.

Exit gate: two test tenants cannot observe or mutate each other's projects;
backup restore and deployment rollback are demonstrated from recorded runbooks.

### W3 — Self-hosted production server and settings

Deliverables:

- Production Compose profile and clean-host installer.
- Admin bootstrap, user/role/token/project management, authenticated UI, and
  server-side settings APIs.
- TLS/reverse-proxy, upgrade/rollback, backup/restore, compatibility, monitoring,
  and troubleshooting docs.
- UI Settings pages that mutate real state, confirm risky actions, surface
  authorization failures, and record audit events.

Exit gate: a clean host can install, create an admin, invite a reader and writer,
capture/browse with correct permissions, upgrade, back up, restore to another
host, and roll back without data loss.

### W4 — UI, history, and search

Deliverables:

- Home, History, Code, Team, Skills, and Settings information architecture.
- Searchable, paginated project switching with human-readable project names and
  concise session summaries rather than raw identifiers or full prompts.
- Indexed project history search and filters with paginated APIs.
- File history and complete blame-to-step/transcript navigation.
- Auth/login/onboarding and project/account switching.
- Accurate loading, empty, denied, offline, quota, and incompatible-version
  states.
- Keyboard accessibility, responsive layouts, virtualization, code splitting,
  and end-to-end tests against the production topology.

Exit gate: a user can connect a project, find a past decision by text or file,
jump to its exact step and blame, manage access, and understand sync/quota state
without using a database or raw API.

### W5 — Skills and cookbooks

Deliverables:

- Catalog truthfulness and trust metadata.
- `rgt skill doctor` and exact parity/version tests.
- Six reviewed cookbooks and fixtures.
- Claude Code/Codex real-session acceptance; install checks for OpenCode and Pi.
- Skills docs and a short “build your own skill” path.

Exit gate: every advertised first-party skill is installable, host-visible after
restart, invokes only its declared tools, and produces the documented class of
answer on a release fixture.

### W6 — Complete docs

Deliverables:

- Docusaurus site and journey-first IA.
- All user, agent, workflow, skills, operator, security, API, migration, and
  contributor sections listed above.
- Generated CLI/OpenAPI reference, `llms.txt`, search, version banners, previews,
  analytics with privacy review, and link/example CI.

Exit gate: a clean user can complete local, self-hosted, and managed journeys
using only the docs, and every command in those journeys is release-tested.

### W7 — OSS release and launch

Deliverables:

- Canonical repository/org migration and old-repo issue/PR triage.
- Changelog, upgrade/migration guide, compatibility matrix, beta limitations,
  known issues, and rollback instructions.
- Cross-platform signed binaries, checksums, SBOM/provenance, GHCR image, and
  working Homebrew/install paths.
- GitHub milestone/roadmap, Discussions, labels, issue forms, support/security
  contacts, and release ownership.
- A 60–90 second captioned demo: install, managed login/connect, captured agent
  turn, history search, blame-to-prompt, and one skill.
- Release candidate soak, launch checklist, announcement copy, and post-launch
  monitoring/triage rotation.

Exit gate: `v1.2.0-beta.3` installs from every advertised channel, migrations
preserve real v1.1/local/server data, managed signup works, and all release gates
are linked from the GitHub Release.

## 5. Release gates

### Correctness and fidelity

- `go test -count=1 ./...`, `go test -race ./...`, `go vet ./...`, UI lint/build,
  Storybook interaction/a11y tests, production UI E2E, release smoke, and
  cross-platform build all pass.
- Two-machine remote fidelity matches steps, conversation, tools, files, blame,
  authors, usage, and ordering.
- Migration fixtures cover old official v1.1, beta.1/.2, local-only stores,
  connected caches, offline spool, and server moves.

### Security and privacy

- Threat model is reviewed and linked.
- No project/history route is anonymously accessible in production mode.
- Cross-tenant IDOR tests cover list/read/write/search/settings/skills/export.
- CSRF, XSS/untrusted transcript rendering, path traversal, oversized bodies,
  decompression/resource exhaustion, brute force, and rate limits are tested.
- Dependency, container, secret, and license scans have no unaccepted critical or
  high finding.
- Secrets and captured content do not enter application/audit logs.
- Export and deletion are tested, including the documented backup-deletion path.

### Reliability and performance

- Define and publish measured hook, API, search, deployment, RPO, and RTO budgets
  during W1; a 10k-file fixture and large historical transcript are mandatory.
- No agent hook can block indefinitely; remote timeouts spool and return.
- Backup restore and release rollback are exercised in staging and production.
- Quota and outage tests preserve local capture and heal after recovery.
- A seven-day release-candidate soak has no unresolved P0/P1 data-loss, auth,
  isolation, migration, or capture-latency issue.

### Product and documentation

- All three onboarding journeys pass from clean environments.
- Search, History, access settings, and Skills meet their workstream exit gates.
- Docs links/examples/version labels pass CI.
- Demo assets contain no private repository, token, email, or customer data.

## 6. Delivery order and dependencies

The critical path is:

1. Release/repository decision and tracker reconciliation.
2. Threat model, managed boundary, protocol capabilities, and project identity.
3. Public authz interfaces and self-hosted auth implementation.
4. Private managed composition, login, tenancy, quota, and operations.
5. Settings/search UI on stable authenticated APIs.
6. Migration, restore, security, performance, and release-candidate gates.
7. Canonical beta release and managed launch.

Docs IA, cookbooks/fixtures, UI research, and release asset planning can proceed
after step 1. Final docs, screenshots, and demo recording wait for stable flows.

A small team should plan roughly 8–12 weeks for this full release, depending on
what the threat model and large-repository benchmark uncover. That is a planning
range, not a date commitment. Do not compress it by dropping auth, isolation,
restore, or migration gates; reduce beta breadth instead.

## 7. Immediate next slice

The repository move, release control baseline, public authorization boundary,
and first secure self-hosted implementation are integrated in `dev`. The next
slice proves their production properties before building more surface area.

1. Expand the security suite across every route family: cross-project access,
   malformed credentials, CSRF, rate limits, secret redaction, and resource
   bounds; record a production-topology smoke test.
2. Exercise the documented clean-host bootstrap, reader/writer capture,
   backup/restore-to-another-host, upgrade, and rollback journey.
3. Benchmark the 10k-file capture path and land a measured latency budget before
   widening the beta.
4. Build the docs information architecture and six executable cookbook fixtures
   while the private managed composition starts against the reviewed public
   contract.
5. After those contracts settle, take the next product slice: indexed history
   search, searchable project switching, and truthful Skills installation state.

## 8. Go/no-go rule

Cut `v1.2.0-beta.3` only when every release gate above has linked evidence. If the
managed service is not ready, ship neither an open “temporary” endpoint nor a
release that implies it is managed-ready. It is acceptable to publish an OSS
release candidate first; it is not acceptable to weaken authentication,
isolation, durability, or migration to meet an announcement date.
