# Canonical repository swap: reclaiming `re_gent`'s identity

> Status: decision required, not started
>
> Last reviewed: 2026-09-02
>
> Depends on: an explicit identity-strategy decision, an agreed freeze window,
> and administrators for both organizations. PRs #99 and #100 are merged.
>
> Decision candidate for: [`BETA-RELEASE-PLAN.md` §3.9](./BETA-RELEASE-PLAN.md#39-canonical-repository-migration),
> step 8 only. It does not supersede the locked archive-with-banner path until
> maintainers explicitly approve strategy B below.

This is a runbook for one specific, deliberately deferred operation: making
`bonez-io/re_gent` *be* the original `regent-vcs/re_gent` — same repository
identity, same stars, forks, watcher graph, and traffic history — instead of
merely continuing its code. Do not run this until active development has
landed and the team has agreed on a freeze window; see
[Preconditions](#preconditions).

- [Why this document exists](#why-this-document-exists)
- [Current state (observed 2026-09-02)](#current-state-observed-2026-09-02)
- [History compatibility](#history-compatibility)
- [The decision this document makes](#the-decision-this-document-makes)
- [Preconditions](#preconditions)
- [Execution sequence](#execution-sequence)
- [Verification checklist](#verification-checklist)
- [Rollback](#rollback)
- [Follow-up doc updates](#follow-up-doc-updates)

---

## Why this document exists

`BETA-RELEASE-PLAN.md` marks step 4 of its migration sequence "Completed on
2026-09-01": *"Transferred and renamed the successor repository to
`bonez-io/re_gent` while preserving repository identity, branches, tags,
issues, the open pull request, environments, and Actions variables."*

That is accurate for what it actually transferred — but the repository it
transferred was **`regent-vcs/re_gent_headless`** (the private working
continuation, per §3.9's own "Repository history" row), not
**`regent-vcs/re_gent`** (the original public OSS repo). GitHub transfer
preserves the identity of the repo you transfer. `re_gent_headless` had its
own issues/PRs/branches — which is why `bonez-io/re_gent` today has 43 open
issues, not 29, and a 1‑digit star count instead of 788. The stars, forks,
and traffic history were never on `re_gent_headless`; they're still sitting
on `regent-vcs/re_gent`, untouched.

§3.9 step 8, as currently written, does not reclaim them either — it calls
for leaving `regent-vcs/re_gent` "read-only with a migration banner... then
archive it," which keeps the original's star count permanently stranded on
an archived repo instead of on the live one. That may have been an
intentional trade-off (it's simpler and lower-risk — see
[The decision this document makes](#the-decision-this-document-makes)), but
it does not match "keep the old stars and traffic," which is the explicit
goal here. This document is the corrected step 8: instead of archiving
`regent-vcs/re_gent` in place, transfer *it* into `bonez-io` too, reusing its
identity, and fold the already-transferred `re_gent_headless` content into
it.

## Current state (observed 2026-09-02)

| | `regent-vcs/re_gent` (original) | `bonez-io/re_gent` (= transferred `re_gent_headless`) |
|---|---|---|
| Repo id | `1225427533` | `1332975336` |
| Created | 2026-04-30 | 2026-08-13 |
| Last pushed | 2026-07-02 | 2026-09-02 (active) |
| Stars / forks | **788 / 57** | 1 / 0 |
| Open issues | 29 | 43 |
| Open PRs | — | 1 (`#89` `codex/gcp-static-ips` — already flagged stale in §W0) |
| Releases | `v0.1.2` … `v1.1.0` (latest), plus tag `v0.1.1-beta` | none |
| `main` | 115 commits | 222 commits |
| Non-`main` branches | 14 (6 `dependabot/*`, `develop`, `chore/coderabbit-ai-review`, `emdash/test-5efqp`, `feat/fork-cmd`, `feat/integrate-opencode`, `feat/fluff_n_sessions_mgmt`, `feature/pi-harness-integration`, `fix/version-ldflags-64`, `hotfix/windows-ref-index-migration`) | ~40 (Amirshrim/\*, amir/\*, arad1410/\* — the F1–F14 server-backed-hooks stack, avichai/\*, codex/\*, dev, feat/\*, fix/\*) |
| Repo secrets | `HOMEBREW_TAP_TOKEN` | none |
| `main` branch protection | none | not checked — re-check at execution time |

Local repo state: `origin` already points at `git@github.com:bonez-io/re_gent.git`,
and `go.mod`'s module path is already `github.com/bonez-io/re_gent`. Both are
correct for the end state and need no change by this plan — the URL and
import path stay put throughout; only what GitHub considers to *be* that
repository changes underneath them.

## History compatibility

Verified 2026-09-02: `bonez-io/re_gent:main` and `regent-vcs/re_gent:main`
share the same root commit (`89a9bf5`), and `regent-vcs/re_gent:main`'s tip
(`359b264`) is an ancestor of `bonez-io/re_gent:main` — i.e. `bonez-io/re_gent`
is a clean, linear, non-rewritten fast-forward continuation, 107 commits
ahead. Re-verify this before executing, since both branches will have moved:

```bash
git fetch git@github.com:regent-vcs/re_gent.git main:refs/temp/old-main
```

```bash
git fetch origin main:refs/temp/new-main
```

```bash
git merge-base --is-ancestor refs/temp/old-main refs/temp/new-main && echo "still fast-forward" || echo "STOP: history diverged, re-plan step 5"
```

```bash
git update-ref -d refs/temp/old-main && git update-ref -d refs/temp/new-main
```

## The decision this document makes

There are two defensible strategies. Naming them explicitly so the choice is
visible rather than implicit:

- **A — archive-with-banner (currently locked in §3.9 step 8).** Leave
  `regent-vcs/re_gent` as a read-only archive with a banner pointing at
  `bonez-io/re_gent`. Simple, zero risk to the already-transferred
  `re_gent_headless` content (issues, PRs, GCP trust, Homebrew tap — none of
  it gets touched twice). Cost: the 788 stars, 57 forks, and traffic history
  never move; they stay permanently on an archived repo.
- **B — identity reclaim (this document).** Transfer `regent-vcs/re_gent`
  itself into `bonez-io`, carrying the stars/forks/traffic/29 issues/releases
  along automatically (same repo id), then replay everything
  `re_gent_headless` added on top. Cost: a second, more delicate operation on
  a repo the team is already actively pushing to, and the current
  `re_gent_headless` issues/PRs have to be re-created by hand since GitHub
  has no API to move issues across repos.

This document proposes **B** if the product decision is to keep the original's
stars and traffic. Confirm that choice before running it. Until then, §3.9 step
8's archive-with-banner strategy remains the accepted release path.

## Preconditions

- [x] `codex/self-hosted-auth` (PR #100) and the
      `codex/beta-release-foundation` stack are merged. This plan retires the
      current `bonez-io/re_gent` repo object, so nothing should be
      in-flight against it when it starts.
- [ ] Freeze window agreed with everyone with unmerged branches on
      `bonez-io/re_gent` (Amirshrim, amir, arad1410, avichai, and whoever
      owns the remaining `codex/*`/`feat/*`/`fix/*` branches at execution
      time) — even 15–30 minutes, so nothing pushed mid-swap gets stranded
      on the renamed-away repo.
- [ ] Re-run the counts in [Current state](#current-state-observed-2026-09-02)
      and [History compatibility](#history-compatibility) — they will have
      drifted.
- [ ] Decide who executes: this needs admin on both `regent-vcs` and
      `bonez-io` orgs.

## Execution sequence

**1. Freeze and snapshot.** Announce the freeze. Save the current issue/PR
list, secret and variable names, webhooks, and environments configured
directly on `bonez-io/re_gent` — anything not carried by a mirror push has to
be recreated by hand in step 6.

```bash
gh issue list --repo bonez-io/re_gent --state open --json number,title,url > /tmp/re_gent_headless_issues.json
```

```bash
gh pr list --repo bonez-io/re_gent --state open --json number,title,url,headRefName > /tmp/re_gent_headless_prs.json
```

**2. Rename the current `bonez-io/re_gent` out of the way**, freeing the name:

```bash
gh api repos/bonez-io/re_gent -X PATCH -f name=re_gent-migrating
```

**3. Transfer the original into `bonez-io`.** This is the step that actually
carries the stars, forks, watchers, 29 issues, releases, and
`HOMEBREW_TAP_TOKEN` — same repo id, nothing left behind:

```bash
gh api repos/regent-vcs/re_gent/transfer -f new_owner=bonez-io
```

It lands at `bonez-io/re_gent` (now free). `regent-vcs/re_gent` becomes an
auto-redirect for the 57 existing forks and anyone with the old URL — as long
as nothing new is ever created at that old name/owner again.

**4. Push everything `re_gent_headless` added on top.** History was verified
fast-forward-compatible in step 0, so this is a clean push, no force, no
rewrite — and it carries every contributor's branch, so their existing local
clones (already pointed at `bonez-io/re_gent`) keep working after a fetch:

```bash
git clone --mirror git@github.com:bonez-io/re_gent-migrating.git /tmp/re_gent-mirror
```

```bash
git -C /tmp/re_gent-mirror push git@github.com:bonez-io/re_gent.git --all
```

```bash
git -C /tmp/re_gent-mirror push git@github.com:bonez-io/re_gent.git --tags
```

**5. Recreate what git can't carry.** Using the snapshot from step 1:
recreate (or link back to) the `re_gent_headless` issues and PRs against the
now-canonical `bonez-io/re_gent` — GitHub has no move-across-repos API for
these. Recreate any repo secrets/variables/webhooks/environments that were
configured directly on `re_gent-migrating` rather than at the org level
(none were found as of 2026-09-02, but re-check).

**6. Re-verify integration credentials.** `HOMEBREW_TAP_TOKEN` should now
already be present (it travels with the transferred repo), which likely
resolves [issue #98](https://github.com/bonez-io/re_gent/issues/98) outright
— confirm rather than assume. Re-check the GCP Workload Identity Federation
trust condition from [issue #97](https://github.com/bonez-io/re_gent/issues/97);
it's keyed on the `owner/repo` string, which is unchanged (`bonez-io/re_gent`
throughout), so it may already be correct, but this is exactly the kind of
condition worth confirming against the live OIDC claim rather than assuming.

**7. Triage the original's issues.** This was already planned in §3.9 step 7,
now simplified since the target is the same repo: close as fixed/obsolete or
fold into the current milestone, the 29 issues that came over with the
transfer in step 3.

**8. Clean up.** Once every branch, tag, issue, and PR is confirmed present
on `bonez-io/re_gent`, delete (or archive, if you want a grace-period backup)
`bonez-io/re_gent-migrating`.

## Verification checklist

- [ ] `gh repo view bonez-io/re_gent --json stargazerCount,forkCount` shows
      788+ / 57+ (plus anything gained since).
- [ ] `git ls-remote --heads bonez-io/re_gent` contains every branch that was
      on `re_gent-migrating` before step 8's cleanup.
- [ ] `git ls-remote --tags bonez-io/re_gent` contains `v0.1.1-beta` through
      `v1.1.0`.
- [ ] Every `re_gent_headless` issue and PR from the step-1 snapshot is either
      present on `bonez-io/re_gent` or deliberately closed/linked.
- [ ] `gh secret list --repo bonez-io/re_gent` includes `HOMEBREW_TAP_TOKEN`.
- [ ] A release build / Homebrew tap update succeeds end-to-end.
- [ ] `regent-vcs/re_gent` redirects (web and `git clone`) to `bonez-io/re_gent`.
- [ ] Every contributor's local clone still pushes/pulls without a remote
      change (confirm with at least one teammate).

## Rollback

Up through step 3 (the transfer), nothing about `re_gent-migrating` has been
touched beyond its name — rename it back to `re_gent` and you're at the
starting state, minus a few minutes. Once step 4 (mirror push) has run,
rollback means force-pushing `bonez-io/re_gent` back to the pre-step-3 state
is *not* how to undo it — the transferred repo now holds the canonical stars
identity. Instead: rename `bonez-io/re_gent` to something safe, rename
`re_gent-migrating` back to `re_gent`, and treat the transfer as needing a
second attempt. Do not delete `re_gent-migrating` until step 8's checklist is
fully green.

## Follow-up doc updates

Once this runs, `BETA-RELEASE-PLAN.md` needs correcting, not just this
document:

- The "Completed on 2026-09-01" bullet should say plainly that it transferred
  `re_gent_headless`'s identity, not `re_gent`'s stars/traffic — so the
  historical record stays accurate even after this doc's job is done.
- §3.9 step 8 should be replaced with a pointer to this document instead of
  "leave `regent-vcs/re_gent` archived," since that path was superseded.
- The "Current operator blockers" bullets for #97/#98 should be updated or
  closed based on what step 6 finds.

This document intentionally doesn't make those edits itself — confirm the
swap actually happened and matches this runbook before updating the plan
that calls itself the "active release source of truth."
