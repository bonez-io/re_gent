# RFC 0002: Git push integration

- Status: Draft
- Date: 2026-08-17
- Implementation: None
- Owners: @Amirshrim
- Issue: [#31](https://github.com/regent-vcs/re_gent_headless/issues/31)

## Summary

When a developer runs `git push`, any captured Regent history their machine still
owes the server should be delivered — without a second command.

This RFC specifies how: hook wiring gains one more target, a chain-preserving Git
`pre-push` hook that runs the existing sync path with the existing hook budget and
**never fails the push**.

It answers the four questions [#31](https://github.com/regent-vcs/re_gent_headless/issues/31)
was held in `status: needs design` for — offline behavior, hook portability,
failure visibility, and opt-in/opt-out — inside the boundary
[RFC 0001](0001-remote-repository-lifecycle.md) already fixed: Regent may
integrate with Git hooks, but Regent itself never stages, commits, pushes, or
changes a source repository's Git remotes.

## The problem this actually solves

The gap is narrower than "sync does not happen automatically", and stating it
honestly is what justifies the conservative design below.

Delivery is already automatic in the common case. Every agent turn in server mode
calls `remote.Flush` (`internal/capture/servermode.go`), which drains the **whole**
spool, not just the step it just recorded. So a queue that built up during an
outage heals itself on the next agent turn, with no human action.

The uncovered window is specific: **the outage ends, the developer pushes, and no
agent turn has run since.** That window is real and common — the agent finishes,
the developer reviews and pushes, and the next agent turn may be hours away or on
another machine. During it, a teammate who runs `rgt pull` after seeing the push
finds the code but not the steps that produced it.

`git push` is the right moment to close that window because it is the moment of
sharing. The developer is saying "my work is now visible to the team"; the agent
history behind it should become visible in the same gesture.

Because the window is narrow, the integration can afford to be conservative: a
short budget, no retries, and unconditional success. It is a second chance at
delivery, not the delivery mechanism.

## What already exists (and is reused unchanged)

| Piece | Where | Reused for |
|---|---|---|
| `rgt push` **is** `runSync` — one delivery path | `internal/cli/push.go` | The hook runs that path; no new transport |
| Whole-spool drain (`remote.Flush`) | `internal/remote/push.go` | The hook delivers everything owed, not one ref |
| Spool with durable high-water marks; objects first, ref last | `internal/remote/spool.go`, `docs/server-mode.md` | Nothing new to lose; replay always safe |
| `retry-after` cooldown after a failed attempt | `internal/remote/spool.go` | Repeated pushes during an outage do not hammer the server |
| Hook network budget: 5s default, 60s cap | `internal/remote/config.go` | The pre-push budget is the same knob |
| `wireAgents` — the single hook-wiring entry point, merge-don't-clobber, reports only what was written | `internal/cli/wire.go` | The Git hook becomes one more target in it |
| `rgt doctor` verifies wiring and names the repair | `internal/cli/doctor.go` | Doctor learns one more check |

The only genuinely new artifact is a shell script in `.git/hooks/`.

## Decisions

### D1 — Mechanism: a chain-preserving `pre-push` hook

Git has **no post-push hook**; `pre-push` is the only client-side hook tied to a
push. It runs after the user confirms and before any transfer, and a non-zero exit
aborts the push — a property this design neutralizes rather than uses (D2).

- If no `pre-push` hook exists, wiring writes one containing a marked Regent block.
- If one exists, wiring writes a hook that **runs the pre-existing hook first and
  propagates its exit code**, then runs the Regent block. A failing user hook still
  aborts the push exactly as before; Regent runs only when the push will proceed.
- The Regent block is delimited by markers, so removal deletes only our lines and
  restores the prior behavior byte for byte.
- The block guards itself: if the `rgt` binary is absent — a CI runner, a machine
  that never installed Regent — it exits 0 silently. Wiring embeds the resolved
  binary path with a `PATH` fallback, following the existing rule that hooks must
  not depend on `PATH`.

### D2 — The hook never fails the push, and never blocks it meaningfully

The Regent block exits 0 unconditionally. No Regent failure — server down, cache
gone, binary missing, config invalid, panic — may abort a `git push`. This is the
Git-hook restatement of the capture rule "never break the user's agent."

Time is bounded by the existing budget (`REGENT_SERVER_TIMEOUT`, default 5s, capped
60s). The hook runs sync in hook mode: **one** attempt within budget, honoring the
spool cooldown. If a recent attempt failed, the hook skips the network entirely and
only reports queue depth. Worst-case added latency is the timeout; the typical
offline case is near zero because the cooldown short-circuits.

### D3 — Wired where agent hooks are wired: `wireAgents`, so both `init` and `connect`

The issue asks for `rgt init` to set up the correlation. Both `rgt init` and
`rgt connect` already install agent hooks through one function — `init` via
`configureHooks`, `connect` directly — and that function is the single entry point
precisely because two wiring paths previously disagreed about what got installed
(`internal/cli/wire.go`).

The Git hook is therefore wired **inside `wireAgents`**, as one more target. That
satisfies the issue literally (`init` sets it up), keeps one answer to "what got
wired", and inherits the existing guarantees: every target attempted even after a
failure, failures collected and reported together, and only what was actually
written reported to the user.

The hook is written in local mode too, and is inert there (D4). Writing it early
means a project that later connects is already correlated, with no second wiring
step — which is the outcome the issue asks for.

### D4 — Offline and edge behavior: spool stays, push proceeds, one line says so

| Situation | What happens | What the user sees |
|---|---|---|
| Server reachable, nothing owed | No network call | Nothing |
| Server reachable, work owed | Spool drains within budget | `Regent: delivered N steps` |
| Server unreachable | Spool untouched, cooldown stamped | `Regent: server unreachable — N steps queued (rgt sync)` |
| Cooldown active | No network attempt | `Regent: N steps queued, retry cooling down (rgt sync to force)` |
| Budget exceeded mid-drain | Partial delivery; high-water mark holds only what the server confirmed | `Regent: delivered M of N steps, rest queued` |
| Local mode (no server binding) | Hook is inert | Nothing |
| `git push --no-verify` | Git skips all pre-push hooks | Nothing — see D6 |

Delivery preserves the server-mode invariant unchanged — objects first, ref last —
because the hook is the same `Flush` path. A push during an outage can never leave
the server with a ref pointing at missing data.

**Concurrency is safe by construction.** An agent turn and a pre-push hook can sync
simultaneously, and the spool takes no lock. This is not a gap: spool writes are
atomic temp-file-plus-rename (`internal/remote/spool.go`), objects are content
addressed so a duplicate upload is a no-op, and the high-water mark only advances
on server confirmation. The worst case is redundant work, never corruption. The
acceptance contract pins this rather than leaving it implicit.

### D5 — Portability: wired per clone, by the client, at wiring time

`.git/hooks/` is never committed and never transmitted by `git clone`. That is a
deliberate Git security decision — arbitrary code must not execute on clone — and
this RFC does not fight it. Portability works the way agent-hook portability
already does:

- Wiring happens on the machine that runs `init` or `connect`.
- RFC 0001's fresh-clone flow already requires a per-machine step (install or
  repair hooks, then `rgt pull`); the Git hook joins that existing step.
- `rgt doctor` checks presence, chain integrity, and binary resolution, and prints
  the exact repair command when any of them fails.

Hook managers are detected, not fought. If `core.hooksPath` is set (husky,
lefthook), wiring does not write into `.git/hooks/` — it would be dead code.
It reports what it found and the single line to add to that manager's own config,
and `doctor` reports the same. Worktrees share the common `.git` directory's hooks;
the hook resolves the Regent binding from the worktree it runs in, so each worktree
syncs its own project.

### D6 — Opt-out: two explicit scopes, plus Git's own escape hatch

The issue's title asks for this to be standard, so the default is **on** wherever
hooks are wired. Opt-out exists at two scopes:

| Scope | Mechanism | Audience |
|---|---|---|
| This wiring run | `--no-git-hook` on `init` / `connect` | The person wiring |
| This machine or process | `REGENT_GIT_SYNC_ON_PUSH=0` | Operators and CI; mirrors the existing `REGENT_SERVER_URL=""` kill switch |

Git supplies a third, per-invocation: **`git push --no-verify` skips all pre-push
hooks.** It is built into Git and cannot be disabled, which means "mandatory" is
not enforceable at the Git layer and this RFC does not pretend otherwise. That is
the correct outcome — a developer who needs to push right now must never be held
by Regent — and it is documented rather than worked around.

**No committed opt-out key.** An earlier draft proposed `[git] sync_on_push` in
`.regent/config.toml`. It is dropped: RFC 0001 defines that file as a *binding* —
which server, which project — and deliberately keeps it minimal. Mixing team policy
into an identity file widens a contract that RFC 0001 narrowed on purpose. If
team-wide policy is later needed, it deserves its own RFC and probably its own file.

`rgt disconnect` removes the Regent block along with the agent hooks, restoring any
pre-existing hook exactly.

### D7 — Failure visibility: one line, or `doctor`

Silence means "nothing to say", never "something failed quietly":

- The hook prints nothing only when nothing was owed.
- Every other outcome prints exactly one line to stderr, and every line reporting
  remaining work names the command that finishes it (`rgt sync`).
- The hook never reports delivery the server did not confirm — the reused path only
  advances the high-water mark on confirmation.
- `rgt doctor` owns the persistent view: hook wired (or why not), chain intact,
  binary resolvable, queue depth, cooldown state.
- `rgt sync --status` remains the no-network query it is today.

### D8 — Push only, not "each git update"

The issue's second bullet says "each git update will update the rgt server". This
RFC deliberately narrows that to push, and the narrowing is a decision Shay should
accept or reject explicitly.

`commit` is local — syncing there adds network to an offline operation and
duplicates what agent hooks already do per turn. `fetch` and `pull` move work in the
opposite direction. Push is the only Git verb whose meaning ("share this") matches
what sync does. Widening later is cheap; a hook on every Git operation is not.

## The boundary, restated as testable prohibitions

From RFC 0001 and the workflow contract, the implementation must hold:

1. Regent never executes `git add`, `git commit`, `git push`, `git remote`, or any
   other Git write operation. The hook is *triggered by* Git; it triggers only sync.
2. The Regent block exits 0 unconditionally.
3. Wiring writes only: `.regent/config.toml`, `.regent/.gitignore`, agent hook
   configuration, and — new here — the `pre-push` hook file. Each changed file is
   reported, per RFC 0001 §The client wires the repository.
4. The refs Git passes to `pre-push` on stdin are read and discarded. Regent session
   refs are unrelated to Git refs; scoping sync by pushed branches would invent a
   correlation the data model does not have.
5. No credential, and no machine path other than the embedded hook binary path,
   enters any committed file.

## Rejected alternatives

| Alternative | Why not |
|---|---|
| Wrap `git` in an alias or shim | Invasive, undiscoverable, breaks other tooling; violates "never change the user's Git" |
| Point `core.hooksPath` at a Regent-owned directory | Clobbers husky/lefthook and any team's hook management; D5 coexists instead |
| Repo-level `git config` entries as the correlation | `.git/config` is not committed either, so it solves no portability problem RFC 0001 has not already solved with the binding — and it puts Regent state in Git's own config file |
| Commit the hook plus a bootstrap script | Git will not run committed hooks, so a per-machine activation step remains; adds a committed artifact and removes nothing |
| Sync on `post-commit` | See D8 |
| Detached background sync from the hook | Loses failure visibility (D7) for a latency win the cooldown already provides; a process outliving the push has no channel to report on |
| Abort the push when Regent cannot deliver | Turns a Regent outage into a Git outage; forbidden by D2 |

## Acceptance contract

| Requirement | Executable coverage |
|---|---|
| `git push` with work owed and a live server delivers it | `TestE2EGitPushDeliversQueuedSteps` |
| `git push` with the server down completes and leaves the queue intact | `TestE2EGitPushWithServerDownDoesNotBlockThePush` |
| A pre-existing `pre-push` hook still runs and can still abort the push | `TestE2EPrePushChainPreservesExistingHook` |
| No Regent failure can abort a push | `TestGitHookRegentBlockAlwaysExitsZero` |
| `rgt disconnect` removes only the Regent block and restores prior behavior | `TestE2EDisconnectRestoresPriorPrePushHook` |
| Repeated pushes during an outage respect the cooldown | `TestGitPushDuringOutageHonorsRetryCooldown` |
| Concurrent agent-turn sync and pre-push sync corrupt nothing | `TestConcurrentFlushFromHookAndAgentTurnIsSafe` |
| `init` and `connect` both wire it; local mode leaves it inert | `TestWireAgentsInstallsGitHookForBothInitAndConnect` |
| `--no-git-hook` and the env variable each disable it at their scope | `TestGitHookOptOutScopes` |
| `core.hooksPath` present → nothing written to `.git/hooks`, guidance printed | `TestWireDetectsHooksPathManager` |
| Regent invokes no Git write command anywhere in the hook path | `TestGitHookNeverInvokesGitWriteCommands` |
| `rgt doctor` reports hook state, chain integrity, and queue depth | `TestDoctorReportsGitHookWiring` |

## Implementation order

1. Hook script generation with marker-based install/remove, added as a target in
   `wireAgents`; `disconnect` removal.
2. Hook-mode sync entry: budget, cooldown, and the one-line output contract (D7).
3. `doctor` checks.
4. Opt-out plumbing (flag, env).
5. `core.hooksPath` detection and guidance.
6. E2E suite per the acceptance contract.

Steps 1–3 are the shippable core; 4–6 can follow in a second PR if review prefers
smaller units.

## Open questions for review

1. **Does the D8 narrowing to push-only match the intent** of "each git update"? This
   is the one place the RFC knowingly does less than the issue's wording.
2. **After acceptance, does #31 become the implementation issue**, or is
   implementation split into child issues? The order above is plausibly more than
   one focused PR, and the workflow requires one issue per PR.
3. **Windows verification.** `pre-push` runs under `sh` on Git for Windows, so one
   POSIX script should suffice — needs confirmation on a real machine before the
   acceptance contract freezes.

## Consequences

- Outage recovery stops depending on someone remembering `rgt sync` in the one
  window agent turns do not cover.
- A `git push` gains at most the hook budget in latency, and typically nothing.
- Fresh clones still require one local wiring step — this RFC does not, and cannot,
  make Git transport hooks.
- The wiring surface grows by one target, inside the framework built for exactly
  that: one entry point, report only what was written, `doctor` checks it,
  `disconnect` reverses it.
- `.regent/config.toml` is unchanged, keeping RFC 0001's binding minimal.
