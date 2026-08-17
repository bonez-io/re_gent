# RFC 0001: Remote repository lifecycle

- Status: Accepted
- Date: 2026-08-17
- Implementation: Partial
- Owners: Regent core

## Summary

Regent has one remote protocol and two deployment models:

1. A self-hosted server, normally started with Docker Compose and selected by URL.
2. The Regent-hosted service, initially free and later able to add commercial plans.

The CLI, object model, synchronization rules, and repository lifecycle are the
same in both deployments. Authentication, tenancy, quotas, billing, and
operations are hosted-service concerns layered around that common protocol.

This RFC defines what it means to connect a source repository to a Regent
project, which state is portable, how existing history moves, and which
component is allowed to change the source repository.

## Why this comes before the UI

The UI, CLI, self-hosted server, and managed service all need the same answers
to these questions:

- What is a Regent project?
- How is a source repository bound to one?
- Which history is canonical?
- What should a fresh clone be able to recover?
- Which credentials and hooks may be committed?
- What happens when onboarding fails halfway through?

Building the UI before these answers are stable would make the UI depend on
incidental server routes and incomplete cache state. The first UI epic will use
the versioned API established by this lifecycle.

## Decisions

### One protocol, two deployments

"Self-hosted" and "Regent-hosted" are deployment models, not separate client
modes. Both implement the same versioned data-plane protocol.

| Concern | Self-hosted | Regent-hosted |
|---|---|---|
| Server location | User-selected URL | Regent-managed URL |
| Authentication | Optional server capability | Required |
| Project tenancy | Single operator or team | Organization scoped |
| Quotas and billing | Operator concern | Managed-service concern |
| Object/ref/session protocol | Common | Common |
| CLI and UI semantics | Common | Common |

The client discovers optional behavior from a capabilities endpoint. It must
not infer security or tenancy semantics from the hostname.

### Stable project identity

A remote project has:

- an immutable, opaque, server-generated `project_id`;
- a mutable display name;
- optional organization/tenant ownership;
- one or more source-repository metadata records.

The display name and checkout folder are never identity. Renaming either must
not split history. The current user-derived `repo_id` remains a legacy protocol
identifier until the server-generated project-id migration is implemented.

### Portable binding, machine-local credentials

The target committed binding is:

```toml
version = 1

[remote]
url = "https://regent.example.com"
project_id = "prj_01J..."
```

It contains no bearer token, user identity, cache path, timeout, organization
secret, or machine-specific executable path.

During migration, clients must continue to read the legacy shape:

```toml
[remote]
url = "https://regent.example.com"
repo_id = "legacy-project-name"
```

Credentials are stored per server origin in user configuration or an OS
keychain. Environment variables remain an operator override. Credentials are
never written inside the source repository.

### The server stores history, not source code

Connecting a project imports Regent history:

- content-addressed objects;
- session refs;
- steps, trees, tool payloads, and archived transcript blobs;
- normalized conversation data required by `log` and `show`;
- enough canonical data to rebuild query indexes and blame maps.

It does not upload the Git repository, working tree, `.env` files, or arbitrary
uncaptured files. A workspace snapshot only travels when it is already part of
a captured Regent step.

### The client wires the repository

The server never edits a Git checkout. `rgt connect` may write:

- `.regent/config.toml`;
- `.regent/.gitignore`;
- supported agent hook configuration files.

It reports the exact files it changed. It never stages, commits, pushes, or
changes a Git remote. Users decide which portable hook files belong in Git.

## Repository states

| State | Meaning |
|---|---|
| Uninitialized | No Regent store or portable binding exists. |
| Local | Regent captures into the repository-local store; no remote binding exists. |
| Connecting | A command is preparing a remote project and prospective cache; the portable binding has not changed. |
| Connected | A valid portable binding selects a server project; the server is canonical and the machine cache is disposable. |
| Connected, pending delivery | Capture continues locally and durable queue state says the server is behind. |
| Moving | History is being copied from the current project to a project on another server; the old binding remains active until cutover. |
| Disconnected | Remote binding and hooks were explicitly removed; server history was not deleted. |

There is no implicit disconnect. Re-running `connect` is idempotent, and
pointing at another server requires an explicit move operation.

## Connect lifecycle

`rgt connect <server>` is a resumable state machine.

### 1. Preflight

Before a network or filesystem mutation, the client:

- resolves the exact source-repository root;
- reads any local Regent store and existing binding;
- detects supported agent hosts;
- identifies legacy history that must be imported;
- validates an explicitly supplied name;
- records the current Git HEAD and remote URLs for verification only.

It never searches sibling repositories or recursively wires a workspace.

### 2. Discover server capabilities

The client requests a versioned capabilities document containing at least:

- protocol versions;
- whether authentication is required;
- supported object and transcript transfer features;
- maximum object size;
- server deployment metadata needed for diagnostics.

An incompatible server fails before the repository binding is changed.

### 3. Authenticate when required

Self-hosted open servers may continue without credentials. A server that
requires authentication returns an actionable error that names the server and
the supported sign-in path. Hosted authentication must use machine-local
credentials and must not modify the repository binding.

### 4. Create or attach to a project

Project creation is idempotent. Repeating the same request returns the same
project. UI-created projects can provide an enrollment/project identifier;
CLI-first self-hosted onboarding can create the project directly.

Attaching to an existing project must prove authorization. A matching display
name or Git remote URL is not sufficient proof.

### 5. Stage and import existing history

Before cutover, the client builds the prospective machine cache and imports all
locally available history into it. It uploads objects before refs and confirms
the remote session tips.

Import is:

- idempotent because objects are content addressed;
- resumable after interruption;
- complete across objects, refs, sessions, and conversations;
- explicit about skipped or unavailable history.

Connect does not report success while known history remains reachable only
through the old store.

### 6. Commit the portable binding

Only after registration and import are confirmed does the client atomically
write `.regent/config.toml`. A failure before this point leaves the previous
capture mode active.

If no previous history exists, the empty import is immediately complete.

### 7. Wire supported agents

Hook configuration is merged and deduplicated. Existing unrelated settings are
preserved. A hook failure does not roll back a valid remote binding, but causes
`connect` to exit non-zero and print the exact repair command.

### 8. Verify and report

The closing verification is equivalent to `rgt doctor` and checks:

- binding validity;
- server reachability and project existence;
- authentication when required;
- hook configuration;
- hook binary resolution;
- pending import/delivery state.

The command lists changed files and tells the user to restart already-running
agent sessions. It does not claim that anything was committed or pushed.

## Reconnect, clone, move, and disconnect

### Reconnect

Connecting an already-connected repository to the same server and project is a
safe repair operation. It verifies server registration, repairs hooks, resumes
pending import, and does not change project identity.

### Fresh clone

A fresh clone carrying the portable binding can install/repair hooks and run
`rgt pull`. Pull restores every server session into a disposable local cache,
including the conversation context needed by `log` and `show`. Read commands
continue to work while the server is offline after that pull.

### Move to another server

Changing servers is explicit, for example `rgt connect <new-server> --move`.
The target behavior is:

1. Pull all history available from the current server.
2. Register or attach to the target project.
3. Import and confirm the complete history on the target.
4. Atomically replace the binding.
5. Leave the old server data untouched.

If the old server is unavailable, the move refuses by default. A future
cache-only override must state exactly which history cannot be verified.

### Disconnect

`rgt disconnect` removes the local binding and selected hooks only after an
explicit command. It never deletes server history, Git history, or the working
tree. The command must say where remote history remains.

## Synchronization semantics

Agent hooks capture and attempt delivery automatically. `rgt sync` drains or
repairs queued captured work; it does not scan the working tree and invent a
step for arbitrary files.

When nothing is queued, the user-facing result should be:

> Up to date — all captured steps are already synced.

Delivery preserves the existing invariant: objects first, ref last. A failure
may leave unreferenced objects but never a ref that points to missing canonical
data.

## API and UI boundary

The remote API must be versioned before the UI becomes a supported client. The
first UI epic is read-only and uses only this API:

- project list and project details;
- server/sync health;
- sessions and step timeline;
- complete step context;
- file/tree view;
- blame.

The first epic excludes billing, organization administration, destructive
history operations, and source-repository writes.

## Managed-service repository boundary

The open Regent repository owns:

- CLI and hook adapters;
- object model and protocol specification;
- self-hosted server;
- common API types;
- shared read-only UI where practical.

A separate private managed-service repository owns:

- authentication and credential issuance;
- organizations and multi-tenant authorization;
- quotas, plans, and billing;
- tenant routing and administrative operations;
- secrets, Terraform, deployment, monitoring, and incident tooling.

The managed service reuses the common data-plane protocol and core server
implementation. It must not fork the storage model into an incompatible
commercial version.

## Acceptance contract

| Requirement | Executable coverage | Status |
|---|---|---|
| Registration against a live server | `TestE2EConnectRegistersWithALiveServer` | Implemented |
| Repeated connect is idempotent | `TestE2EConnectingTwiceToTheSameServerIsSafe` | Implemented |
| Existing local history remains readable and reaches the server | `TestE2EConnectingKeepsHistoryRecordedBeforeIt`, `TestE2EHistoryRecordedBeforeConnectingReachesTheServer` | Implemented |
| Hooks survive connect and reconnect | `TestE2EConnectingALocallyInitialisedProjectConnectsIt` | Implemented |
| Fresh clone can pull all session steps and read offline | `TestE2ESecondCloneCanPullAndReadTheTeamsHistory`, `TestE2EPulledHistoryReadsWithTheServerGone` | Implemented |
| Portable binding contains no credentials | `TestE2ERemoteLifecycleBindingIsPortableAndSecretFree` | Implemented by this RFC |
| Failed registration does not commit a binding | `TestE2ERemoteLifecycleRegistrationFailureLeavesNoBinding` | Implemented by this RFC |
| Server-generated immutable `project_id` | Planned lifecycle acceptance test | Gap |
| Capability negotiation and protocol rejection | Planned lifecycle acceptance test | Gap |
| Atomic cutover after confirmed import | Planned lifecycle acceptance test | Gap |
| Complete conversation context after fresh pull | Planned lifecycle acceptance test | Gap |
| Explicit full-history server move | Planned lifecycle acceptance test | Gap |
| Hosted authentication with no repository secret | Planned lifecycle acceptance test | Gap |

## Current implementation gaps

The accepted contract intentionally differs from the current implementation in
these places:

1. The server still uses a user-derived `repo_id` instead of an opaque project ID.
2. There is no capabilities/version-negotiation endpoint.
3. Connect writes the binding before carryover is confirmed and does not fail on an incomplete import.
4. Repointing at another server leaves old remote history behind instead of performing an explicit move.
5. A fresh pull reconstructs steps and blame but does not yet restore normalized conversation rows for `show`.
6. Hosted sign-in, tenancy, and enrollment do not yet exist.
7. `rgt sync` reports `Nothing queued`, which users reasonably read as nothing having been captured.

These are the implementation order for the remote-lifecycle epic; they are not
alternative design options.

## Consequences

- The UI can target one stable API in both deployments.
- A committed binding is safe to share because it contains no secret.
- Managed-service security and commercial code can remain private without
  fragmenting the Regent data model.
- Connecting and moving may take longer because success means history was
  actually confirmed, not merely that a config file was written.
- The legacy `repo_id` shape needs an explicit compatibility period and
  migration rather than an in-place rename.
