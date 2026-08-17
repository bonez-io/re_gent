# Server mode

Server mode moves re_gent's source of truth off the client. Hooks talk to a re_gent server
directly. The repository keeps only its small `.regent/config.toml` binding; objects, refs,
and query state live in the machine-local cache and are disposable.

This document is the operational contract for that mode: what it guarantees, how it behaves when
the network or the server misbehaves, and what you do about each case.

- [Enabling it](#enabling-it)
- [What lives where](#what-lives-where)
- [The consistency guarantee](#the-consistency-guarantee)
- [Failure-mode table](#failure-mode-table)
- [Accepted risks](#accepted-risks)
- [Operating it](#operating-it)
- [How this is tested](#how-this-is-tested)

---

## Enabling it

Server mode turns on when **both** a server URL and a repo id are configured. Half a configuration
is deliberately treated as *no* configuration, so a typo degrades to local mode rather than to a
broken remote.

Environment variables (they win over the file, so one process can be redirected or disabled without
editing shared state):

| Variable | Meaning | Default |
|---|---|---|
| `REGENT_SERVER_URL` | Base URL of the server. Scheme must be `http` or `https`. | — (required) |
| `REGENT_REPO_ID` | Repository name registered with the server. Letters, digits, `.`, `_`, `-`; max 64; must start with a letter or digit. | — (required) |
| `REGENT_TOKEN` | Reserved for future authenticated remote deployments; the current local server is open. | empty |
| `REGENT_SERVER_TIMEOUT` | Network budget for one hook invocation. Clamped to 60s. | `5s` |
| `REGENT_CACHE_DIR` | Overrides the machine-local cache location. | `<user cache>/regent` |

Or `~/.regent/config.toml`:

```toml
[server]
url     = "https://regent.example.com"
repo_id = "my-project"
timeout = "5s"
```

Setting `REGENT_SERVER_URL=""` (empty, not unset) disables server mode regardless of the file. That
is the documented kill switch, and it is what the test suites use to stay hermetic.

## What lives where

| | Location | Canonical? | Safe to delete? |
|---|---|---|---|
| Objects + refs | The server | **Yes** | No |
| Machine-local cache | `<user cache>/regent/repos/<server>/<repo_id>/` | No — a disposable mirror | Yes, then `rgt pull` |
| Outbox (spool) | `<cache>/spool/` | No — derived state | Yes, but unsent work is then re-pushed wholesale |
| Server binding | `<repo>/.regent/config.toml` | **Yes** | No — teammates may share it through Git |

The cache lives outside the repository on purpose: server mode keeps captured history out of
the working tree. Only the small server binding belongs in the repository.

**The outbox stores no payloads.** It holds only durable high-water marks (`spool/refs/<escaped
ref>` = the tip most recently *confirmed* by the server), markers for loose objects, and a
`spool/retry-after` cooldown stamp. Pending work is *derived* — local ref minus high-water mark —
so a crash mid-turn loses nothing and replaying the whole spool is always safe.

## The consistency guarantee

**Delivery order is the guarantee: objects first, ref last.**

For each step, oldest first: file blobs → tree → tool-call payload blobs → the step object → and
only then the ref CAS.

The consequence is the invariant worth remembering:

> A failure at any point leaves the server holding **extra unreferenced objects**, never a ref
> pointing at a step whose contents never arrived.

Unreferenced objects are harmless and GC-able. A dangling ref would be corruption. The server
enforces the same rule independently, refusing a ref update with `422` when it is missing objects
the step depends on.

Every operation is idempotent, so a retry after *any* partial failure converges rather than
duplicating.

## Failure-mode table

`Agent impact` is the column that matters: server mode's core promise is that **the agent is never
blocked and nothing is ever silently dropped.**

| # | Failure | What re_gent does | Agent impact | Server state | Your action |
|---|---|---|---|---|---|
| 1 | **Network blip** (transient DNS/TCP error, 5xx, 429) | `HTTPClient.do` retries up to 3 attempts with exponential backoff (50ms → 400ms cap), bounded by the 5s context. 4xx are terminal and not retried. | None | Consistent | None |
| 2 | **Server down / unreachable for the whole turn** | Flush fails; work stays queued. A 30s cooldown is stamped so later hooks skip the network entirely. Logged with the queue location and `run 'rgt sync'`. | None — capture continues at local-disk speed | Consistent (behind) | None; it drains itself. `rgt sync` to force. |
| 3 | **Long outage (hours)** | One timeout paid per 30s cooldown window, not per hook. Steps accumulate in the cache; the outbox tracks only marks. | None | Consistent (far behind) | `rgt sync --status` to see the lag |
| 4 | **Partial write — connection dies mid-upload** | Objects landed so far are a valid *prefix* (oldest-first ordering). The ref was never advanced. Next sync re-pushes the delta; already-present objects are skipped. | None | Extra unreferenced objects only | None |
| 5 | **Partial write — objects land, ref CAS fails** | High-water mark is *not* advanced, so the ref is retried next sync. | None | Extra unreferenced objects only | None |
| 6 | **Server ref diverged** (another machine pushed a non-ancestor tip) | `ErrDiverged` — push refuses rather than clobbering someone else's history. Work stays queued. | None | Untouched | Investigate; the two sessions genuinely disagree |
| 7 | **Ref CAS conflict** (concurrent writer, `409`) | Not recorded as pushed; the next flush re-reads the server tip and re-checks ancestry. | None | Consistent | None |
| 8 | **Server lost objects an ancestor needs** (`422 ErrIncomplete`) | Push auto-recovers: forgets the high-water mark, re-uploads from the root skipping what the server has, retries the CAS. | None | Repaired | None. If it persists, `rgt sync --repair` |
| 9 | **Server silently lost an *ancestor* object** (no 422 — the reachability check only inspects the step being pointed at) | **Not auto-detected.** Push only sends the delta the server does not claim to have. | None | Silently incomplete history | `rgt sync --repair <ref>` — one existence check per object, deliberately off the hook path |
| 10 | **Local cache deleted / lost machine, or a fresh clone** | Nothing is lost: the server is canonical. | None | Consistent | `rgt pull` fetches every session the server holds; the index and blame maps are recomputed from the objects |
| 11 | **Corrupt high-water mark** | Treated as absent → falls back to a full push. Fails toward a redundant push, never a skipped one. | None | Consistent | None |
| 12 | **Outbox full** (4096 loose objects already queued) | `ErrSpoolFull`. Logged as an **error**, because it means captured bytes are being dropped from the delivery queue — a data-retention event, never swallowed. | None | Consistent (missing those transcripts) | `rgt sync` to drain |
| 13 | **Auth failure / bad token (401, 403)** | Terminal, not retried. Logged; work stays queued. | None | Untouched | Fix the token, then `rgt sync` |
| 14 | **Misconfiguration** (bad URL, unparsable TOML, invalid repo id) | Server mode is *disabled* for that invocation and capture falls back to local mode — or to a clean no-op if there is no local store. Reason is logged. | None | n/a | Fix the config |
| 15 | **Object too large (413)** | Terminal, not retried. Logged; work stays queued. | None | Consistent | Investigate the oversized payload |
| 16 | **Hook exceeds its network budget** | The 5s context deadline cancels in-flight work; the turn proceeds and work stays queued. | None — bounded by design | Consistent (behind) | None |
| 17 | **Local ref diverged from the server's on `rgt pull`** | The mirror of row 6. Objects are downloaded (additive, and needed to compute ancestry at all) but the local ref is not moved, so no local step is orphaned. The session is named, the other sessions are still pulled, and the command exits non-zero. | None | Untouched | Deliver yours with `rgt sync`, which reports the same disagreement from the push side |

The single mechanism behind rows 2, 3 and 16 is worth stating plainly: **the cooldown is what keeps
a long outage from feeling like a broken agent.** Without it, every hook invocation would pay the
full network timeout, and "the server is down" would present to the user as "my agent is slow and
broken."

## Accepted risks

Stated explicitly, because an unstated risk is indistinguishable from a bug:

1. **Row 9 needs a manual `--repair`.** Detecting server-side loss of an *ancestor* object on every
   push would cost one existence check per object per turn. That is too expensive for the hook path,
   so it is an explicit, documented command instead of an automatic behaviour.
2. **The outbox is bounded at 4096 loose objects.** Beyond that, new loose objects (archived
   transcripts) are refused and **logged as errors**. Refusing to grow without bound is preferred
   over silently filling the user's disk. Steps and refs are *not* subject to this bound — they are
   derived from refs, not queued individually.
3. **A machine-local cache is still required.** This is a cutover of the *source of truth*, not the
   removal of all local state: capture needs somewhere to write objects before it can deliver them,
   and that buffer is what makes offline operation possible at all. The cache is disposable — row 10.
4. **Concurrent hooks race on high-water marks; last writer wins.** Safe in both directions: an
   older mark causes only a redundant re-push, and a newer mark is only ever written after the
   server confirmed that tip.
5. **Blame maps and the SQLite index are derived, not transferred.** Hydrate downloads objects and
   recomputes them. They are cache, not canon.

## Operating it

```bash
rgt sync --status          # what is queued — no network, works offline
rgt sync                   # drain the outbox now
rgt pull                   # fetch the server's history into this cache (asks the server what exists)
rgt sync --pull <ref>      # rebuild a lost cache from one named ref
rgt sync --repair <ref>    # re-verify the whole history server-side, re-upload anything missing
```

`rgt pull` is the command for a machine that has pushed nothing: it asks the server which
sessions exist rather than reading this machine's outbox, which lists only what this machine
sent. It also refuses to move a session ref onto history that does not contain the local
one — a diverged session is reported and left alone, and the rest are still fetched.
`rgt sync --pull <ref>` remains the lower-level form for rebuilding one named ref, and it
does overwrite the local ref: use it on a cache you have lost, not on one holding work.

`--status` never touches the network — it compares local refs against durable marks, so it is the
right first command during an outage. Manual sync gets a 60s budget (versus 5s on the hook path):
a human is waiting and can Ctrl-C.

### Reading history

The read commands — `rgt log`, `show`, `blame`, `sessions`, `status`, `cat` — follow the same
precedence as capture: when server mode is configured they read from the machine-local cache
instead of looking for a `.regent/` directory. A repository under server mode therefore has no
local store at all, and `rgt log` still works in it.

If the cache is missing or empty, they report that the project is connected and not yet pulled,
and point at `rgt pull` — not at `rgt init`, which would be the wrong advice when the server is
the source of truth, and not at a ref name a fresh clone has no way of knowing. A broken or
half-written configuration degrades to the local store, so a stray environment variable can never
make an ordinary local repository unreadable.

Failures are logged to the cache directory, not the repo — in server mode there is no
repository-local place to write. Look in `<cache>/log/`.

## How this is tested

Each row above is backed by a test rather than an assertion of intent:

| Rows | Tests |
|---|---|
| 1, 13, 15, 16 | `TestClientRetriesServerErrorsButNotClientErrors`, `TestClientAuthenticatesWithBearerToken`, `TestClientStatusMapping`, `TestClientHonoursContextDeadline`, `TestClientOfflineIsATransportError` |
| 2, 3 | `TestServerModeSurvivesAnOutageAndConverges`, `TestServerModeCooldownStopsHammeringADeadServer`, `TestFlushKeepsWorkQueuedWhileOffline` |
| 4, 5 | `TestPushLeavesNoDanglingRefWhenTheNetworkDies` |
| 6, 7 | `TestPushRefusesToClobberDivergedServer`, `TestClientRefCASAndConflict` |
| 8, 9 | `TestPushRecoversFromServerSideObjectLoss`, `TestRepairRestoresLostAncestorObjects`, `TestSyncRepairRestoresObjectsTheServerLost` |
| 10 | `TestHydrateRebuildsACacheFromTheServer`, `TestSyncPullRebuildsCacheAndIndexFromServer` |
| 11 | `TestSpoolCorruptMarkFallsBackToFullPush` |
| 12 | `TestSpoolIsBounded` |
| 14 | `TestServerConfigForRejectsBrokenConfiguration`, `TestOpenWithoutServerConfigOrLocalStoreIsANoOp` |
| Cache is disposable / repo untouched | `TestServerModeCacheLivesOutsideTheWorkspace`, `TestServerModeCapturesWithoutLocalRegentDir` |
| Offline status | `TestSpoolStatusIsComputedOffline`, `TestSyncStatusReportsQueuedWorkWithoutNetwork` |

Induced failures use `internal/remotetest`, an in-process server that can be told to fail, go
offline, drop objects, or diverge — so the failure modes are exercised deterministically rather
than by unplugging a cable.

```bash
go test ./internal/remote/... ./internal/capture/... ./internal/cli/...
go test -race ./...
```
