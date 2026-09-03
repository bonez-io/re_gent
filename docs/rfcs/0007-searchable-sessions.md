# RFC 0007: Searchable sessions

- Status: Draft, decisions locked; S1, S3, S4, the CLI half of S5 (`rgt
  search`, `rgt work`) and server mode landed; `rgt related`, capture-time
  scrub (S2), the skill (S6) and Cursor (S7) are open
- Owners: re_gent maintainers
- Last updated: 2026-09-03
- Builds on: [RFC 0004](./0004-managed-service-identity-and-enrollment.md)
  "Privacy gate for public capture" for the detector set and the rewrite
  semantics; `internal/redact` and `internal/publicgate` as they exist today
- Lives in: the public module. Nothing here needs the managed composition.
- Tracking: Linear project `re_gent`, umbrella
  [1SI-1051](https://linear.app/bonez/issue/1SI-1051); streams S1–S7 are
  1SI-1052, 1SI-1053, 1SI-1054, 1SI-1055, 1SI-1056, 1SI-1058, 1SI-1057

re_gent records what an agent did. It does not yet let an agent — or a
person — ask *"have we worked on this before?"* This document adds that: every
session is divided into **work items**, each with a goal, the way it went, and
how it ended; work items are tied to the files and steps that built them, and
to whatever **entities** — tickets, pull requests, symbols, errors, decisions,
anything — the agent that read the session judged worth finding again.

## The promise

An agent starts a task. Before it edits, it asks re_gent what it already
knows: work that touched the same file or symbol, work that was about the
same ticket or PR, work that was *about* the same thing even when it touched
nothing in common, and work that was left unfinished. It gets back work
items, ranked, each with a goal, an outcome, the entities that matched, and
step hashes to `rgt show`.

Nothing about capture changes for a person who never turns this on. When it
is on, nothing runs inside an agent turn.

## Locked decisions

1. **This is part of re_gent**, not a separate tool. The ingestion and
   storage it needs already exist here; the new work is a derived layer on
   top of `index.db` and the object store.
2. **Nothing runs in the hook's synchronous path.** The `Stop` hook enqueues
   one job row and returns. A detached worker does the rest. A worker failure
   is logged to `.regent/log/` and retried; it never surfaces to the agent.
3. **Off by default. One toggle**: `[insight] enabled` in the repository's
   `.regent/config.toml`.
4. **Scrubbing is a first-class setting** and reuses `internal/redact`.
   Content that leaves the machine is always scrubbed with the full detector
   set, regardless of any setting. Scrubbing at capture time is opt-in.
5. **The user chooses the model and the agent** that runs the reading, and
   separately the embedding model. Both may be remote (API) or local.
   Credentials are named by environment variable only, and provider
   configuration lives in the per-user file, never in the committed per-repo
   file.
6. **The unit of search is a work item**: goal, approach, outcome, status.
   A session has one or more. Work items link to steps (and through them to
   files and blame) deterministically, and to entities by the model's
   judgment with evidence.
7. **Entities are open-ended.** There is no fixed taxonomy and no pattern
   configuration. The model names the type. re_gent only normalizes and
   dedupes. Two things are entities without any model: every URL, and every
   commit or branch a `git` command reported.
8. **The searchability guarantee is layered.** Anything literally present in
   a session is findable by full-text search with no model. Every URL is an
   entity with no model. Everything else is the model's judgment, and every
   model-made link records the step where the evidence is, so `rgt show` can
   confirm it.
9. **Search never calls a model** except to embed the query, and works with
   embeddings disabled (full-text only).
10. **Derived, rebuildable, model-tagged.** Work items, entities, and
    embeddings live in `index.db`, keyed by the provider and model that
    produced them. They are not canonical objects. `rgt insight rebuild`
    regenerates them. Canonical steps, trees, blobs, refs, and blame maps are
    untouched.

## Vocabulary

| Term | Meaning |
|---|---|
| **Work item** | A contiguous run of turns within one session in pursuit of one goal. Carries `goal`, `approach` (the way there: what was tried, decided, reversed), `outcome` (what is true at the end), and `status`. The unit of search. |
| **Status** | `wip`, `done`, `failed`, `abandoned`, `superseded`. The one closed enum in this design: it is a state, not a category. |
| **Entity** | Something a person or agent would search for. `type` is a free string the model chooses (`ticket`, `pull_request`, `symbol`, `error`, `dependency`, `decision`, `concept`, `person`, …), `name` is display text, `ref` is a canonical identifier when one exists (a URL, `owner/repo#95`, `path::Symbol`, a commit hash). |
| **Link** | `work_item → entity` with a `role` (`goal`, `touched`, `produced`, `referenced`, `blocked_by`), a confidence, and an `evidence_step_id`. |
| **Worker** | The detached `rgt insight run` process that drains the job queue. Single-flight per repository via a lock file. |
| **Scrub** | Rewriting bytes with `internal/redact` plus user-supplied patterns, before storage (opt-in) and before egress (always). |
| **Provider** | Where a model call goes: `anthropic`, `openai-compatible`, or `command`. |

## What a person configures

Two files, two concerns. The committed `.regent/config.toml` carries policy
that should travel with the repository. The private `~/.regent/config.toml`
(mode 0600, already holds auth) carries providers and endpoints.

Per repository, `.regent/config.toml`:

```toml
[insight]
enabled = true
# how long a session may be silent before its open work item is closed as
# wip without a model call
work_item_idle = "2h"

[insight.scrub]
# what capture stores. "off" stores raw bytes as today.
# "secrets" rewrites tool I/O and messages through redact.Detect/Redact.
# "secrets+paths" also runs redact.HomePaths. Files are never rewritten
# (RFC 0004): a file containing a secret is dropped from the payload sent
# to a provider, and stored exactly as today.
capture = "off"
# additional patterns scrubbed at both capture (when on) and egress (always)
patterns = ["ACME Corp", "client-\\w+"]
```

There is deliberately nothing here about what to extract. The session says
what it is about; the worker passes the repository's git remotes so the model
can canonicalize `#95` to a URL, and nothing else.

Per user, `~/.regent/config.toml`:

```toml
[insight.model]
provider = "anthropic"            # anthropic | openai-compatible | command
model = "claude-haiku-4-5-20251001"
api_key_env = "ANTHROPIC_API_KEY" # the variable's name, never its value
base_url = ""                     # openai-compatible only; ollama is http://localhost:11434/v1
command = []                      # command only, e.g. ["claude", "-p", "--output-format", "json"]
max_input_tokens = 24000          # per call, after scrub; the worker truncates oldest-first

[insight.embedding]
provider = "openai-compatible"    # openai-compatible | command
model = "nomic-embed-text"        # local via ollama, or text-embedding-3-small, voyage-code-3 via a proxy
base_url = "http://localhost:11434/v1"
api_key_env = ""
dimensions = 768
```

The `command` provider runs a program with the request on stdin and reads
JSON on stdout. This is how "choose the agent" is satisfied without re_gent
knowing every agent's SDK: `command = ["claude", "-p", "--output-format",
"json"]` or `["codex", "exec", "--json"]`. Output must parse against the
schema in Appendix A; one retry on parse failure, then the job fails.

Anthropic has no embeddings endpoint; the two embedding shapes above cover
OpenAI, Ollama, LM Studio, vLLM, Jina, and OpenRouter, and `command` covers
everything else.

A per-repository `[insight.model] provider = "..."` override is allowed for
the name only, so a public repo can pin "local" without carrying a URL.

## Pipeline

```
Stop hook ──► RecordAssistantAndFinalize ──► INSERT insight_jobs ──► spawn worker (detached)
                                                                          │
worker: lock ─► drain ─► gather ─► scrub ─► read (model) ─► link ─► embed ─► commit
```

**Enqueue.** `RecordAssistantAndFinalize` reports the finished turn to the
command edge (`Recorder.OnTurnFinalized`), which inserts `(kind='turn',
session_id, step_id, turn_id)` — step empty for a turn that only talked — and,
if no live process holds `.regent/insight.lock`, starts `rgt insight run` as a
detached process (`Setsid`, output to `.regent/log/insight.log`). The insert
and the spawn are both best-effort; either failing is a line in
`hook-error.log`, not an error return. The edge installs the callback only on
the local-mode path, which is how server mode stays out by construction. A
turn queued twice (a recovered Stop) is one job; a worker that dies mid-job
leaves it `running`, and the next worker returns it to the queue. Retries go
behind fresh jobs, three attempts then `failed`, so one job that keeps
failing never blocks the rest.

**Gather.** The worker loads the session's open work item (if any) and the
turns since its last processed step: user prompts, assistant text, reasoning
blocks, tool names, `step_files` paths, and diff hunks from
`internal/treediff` bounded to `max_input_tokens`. Full file contents never
go to a provider; hunks do. It also loads, by entity overlap and recency, up
to five `wip` work items from earlier sessions, so the model can say this
session is continuing one of them.

**Deterministic entities.** Before any model call: every URL in the turns
becomes an entity (type inferred from the URL's shape where it is
unambiguous — a GitHub `/pull/` path is `pull_request`, `/issues/` is
`issue`, otherwise `link`), and every commit hash or branch name reported by
a `git` command becomes a `commit` or `branch` entity. Each carries the step
it was seen in. These exist whether or not the model ever runs.

**Scrub.** The payload is passed through `redact.Detect/Redact` and
`redact.HomePaths` plus `[insight.scrub].patterns`, unconditionally, because
the next step leaves the machine when the provider is remote. When
`scrub.capture` is on, the same rewrite was already applied at capture time
through a `publicgate.Checker` built from the repository policy; the egress
pass is then a no-op that still runs.

**Read.** One structured call, Appendix A. The model returns: whether the new
turns continue the open work item, start a new one, or continue a `wip` item
from an earlier session (and where the boundary is); for each affected work
item, `goal`, a rolling `approach` (the previous text plus the new turns —
not from scratch, so cost is one call per `Stop` per open work item),
`outcome`, and `status`; and entity links. A session idle longer than
`work_item_idle = "2h"` has its open item closed as `wip` without a model
call.

**Link.** The model's links are merged with the deterministic ones. It may
add any entity of any type; it may set a role and confidence on a
deterministic entity; it may not remove one. Every link it adds must name an
`evidence_step_id` from the turns it was shown, or the link is dropped and
the drop is logged. Entities are deduped on `(type, ref)` when `ref` is
present, else on `(type, lower(name))`.

**Embed.** `goal + approach + outcome + entity names` is embedded once per
`(provider, model)`. Switching models adds a row; it does not delete one.

**Commit.** All rows for one job land in one transaction. A crash mid-job is
re-runnable from the queue; a work item is only ever written whole.

## Storage

Additions to `index.db`, migrated in `migrateSchema` like the existing tables:

```sql
work_items(id TEXT PK, session_id, origin, start_step_id, end_step_id,
           start_ts, end_ts,
           goal TEXT, approach TEXT, outcome TEXT,
           status TEXT,                      -- wip | done | failed | abandoned | superseded
           continues_work_item_id TEXT,      -- earlier wip item this one resumed, or NULL
           model_provider, model_name, prompt_version, updated_at)
work_item_steps(work_item_id, step_id, PRIMARY KEY (work_item_id, step_id))
                                             -- derived; files and blame come through steps
entities(id TEXT PK, type TEXT, name TEXT, ref TEXT,
         UNIQUE (type, ref), INDEX (type, name))
work_item_entities(work_item_id, entity_id, role, confidence, source,
                   evidence_step_id,          -- source: deterministic | model
                   PRIMARY KEY (work_item_id, entity_id, role))
embeddings(owner_kind, owner_id, provider, model, dim, vector BLOB, updated_at,
           PRIMARY KEY (owner_kind, owner_id, provider, model))
insight_jobs(id INTEGER PK, kind, session_id, step_id, turn_id, state, attempts,
             last_error, created_at, updated_at)   -- kind: turn | session
insight_meta(key TEXT PK, value)                   -- enabled_at, fts_rebuilt_at, counters
messages_fts    -- FTS5 external-content table over messages.content_text
work_items_fts  -- FTS5 over goal, approach, outcome
entities_fts    -- FTS5 over name, ref
```

The FTS tables are kept current by triggers from the moment the schema
exists, so a message is findable as soon as it is recorded, insight enabled
or not. Rows recorded before the schema existed are reached by `rgt insight
enable` and `rgt insight rebuild`, both of which rebuild the three indexes;
`rgt insight status` reports "N of M messages" so the gap is visible rather
than silent. External-content FTS is keyed by rowid, which a `VACUUM` may
renumber; `rebuild` is the remedy, and nothing in re_gent runs `VACUUM`.

Files are not entities. A work item's files are the paths its steps changed
against their parents, stored in `work_item_files(work_item_id, path)` at
write time. (`step_files` holds each step's whole tree, not its changes, so
the join the first draft proposed would have named every file in the
workspace.) This is exact, needs no model, and is what `rgt blame` already
keys on. A `symbol` entity is the model saying "this symbol mattered here";
`rgt related --symbol` uses blame first and that entity second.

Vectors are float32 little-endian blobs. Similarity is brute-force cosine in
process: at 768 dimensions, fifty thousand work items scan in well under a
second, which is more than a repository will have for a long time.
`sqlite-vec` or any ANN index is deferred until a real repository shows the
scan on a profile.

In server mode the same tables live in the server's per-project index, filled
by mirroring pushed refs; nothing derived is pushed from a client.

## Search

```
rgt search "<query>" [--file <path>] [--entity <name> | <type>:<name> | <ref>]
                     [--status wip|done|…] [--session <id>] [--since <dur>] [--limit N] [--json]
rgt related --file <path> [--symbol <name>] [--lines a-b] [--task "<what I'm doing>"] [--json]
rgt work [list [--status wip] [--session <id>]] | show <work-item-id>
rgt insight status | run [--detach] | rebuild [--embeddings-only] | enable | disable
```

**`rgt search`** is hybrid: BM25 over `work_items_fts` ∪ `entities_fts` ∪
`messages_fts`, and cosine over `embeddings` for the configured `(provider,
model)`, fused with reciprocal rank fusion, then filtered. `--entity` matches
`ref` exactly, else `name` by prefix, else full-text. A hit on
`messages_fts` for a session with no work item yet is still returned, marked
*not yet indexed* with the matching step — this is the guarantee in decision
8 made visible. If the embedding provider is unreachable, the result is
full-text only and says so on stderr. Nothing is ever omitted for lack of a
summary.

**`rgt related`** is the blame-shaped query. It runs `rgt blame` on the file
(restricted to `--lines` when given), collects the distinct owning steps,
maps them through `work_item_steps` — no model, no embedding — and ranks
those first, marked `owns lines`. It then runs `rgt search` with the file
basename, the symbol, and `--task` as the query and appends semantic matches
marked `related`. `wip` items rank above `done` at equal score, because an
unfinished attempt at the same thing is the most useful thing to know.

**`rgt work show`** prints the three parts and the status, the entities with
their roles and evidence steps, the files, and the step range — the shape an
agent reads before deciding whether to `rgt show` further.

Every summary in every output carries `provider`, `model`, `prompt_version`,
and `updated_at`, so a reader can tell a stale reading from a fresh one.

## The skill

`internal/skills/data/related-work/SKILL.md`, installed by `rgt init` like
the others, allowed tools `rgt search *`, `rgt related *`, `rgt work *`,
`rgt show *`. It instructs the agent to call `rgt related` with the file it
is about to edit and `--task` set to the user's request, read the top items
with `rgt work show`, lead with `wip` items (someone already started this),
then decisions already made and corrections already applied — the same
distillation `context-primer` asks for. `context-primer` gains one line: run
`rgt related` first, and fall back to walking blame by hand when insight is
disabled.

Because search is a CLI, the same skill text works for Claude Code, Codex,
OpenCode, and Pi. Cursor is a reader, not a hook host; see below.

## What is out of v1

- ~~**Server mode.**~~ In. The server keeps a per-project `index.db` beside
  its objects and refs, mirrors every pushed session ref into it
  (`internal/insight/mirror`: new steps indexed, messages rebuilt from each
  step's conversation blob), runs the same worker in-process on ingest with
  providers from `<data dir>/insight.toml` (`[model]`, `[embedding]`, same
  shape as the per-user tables; `REGENT_INSIGHT_CONFIG` overrides the path),
  and serves `GET insight/status`, `POST insight/settings`, `POST
  insight/run`, `POST insight/rebuild`, `GET search`, `GET work`, `GET
  work/{id}` under `/{project}/api/`. The per-project switch lives in that
  index (`rgt insight enable` in a server-mode repository sets it on the
  server; the committed `[insight]` table is not consulted). `rgt search`,
  `rgt work`, and `rgt insight` call those routes when the repository is
  connected. One limit: a turn that used no tools writes no step and is not
  pushed, so the server cannot read it; locally it is.
- **Entity-to-entity relations** (this PR closes that ticket). Both link to
  the same work item; that is enough to find them together. A graph comes
  when a query needs it.
- **UI search.** The read API and the web UI get `/search` and `/related`
  after the CLI shape settles.
- **Tree-sitter symbols.** Model-read symbols from hunks first; a parser when
  the model's recall on real repositories is measured and found wanting.
- **Model-based PII scrubbing.** Deciding what not to send to a model by
  sending it to a model is circular unless the model is local. Detector set
  plus user patterns is the v1 answer.
- **ANN indexes, cross-repository search, a `rgt insight cost` report.**

## Cursor

Cursor has no hook API. Its conversations live in a local SQLite file
(`state.vscdb` under the workspace storage directory), which several
open-source viewers already read. A `cursor` origin is an *importer*, not a
hook adapter: `rgt import cursor` (and the worker, on a timer) reads new
conversations, writes them as sessions and messages, and enqueues insight
jobs. It cannot produce steps with trees, because Cursor gives no
turn-aligned filesystem snapshot; those sessions get work items and entities
and are searchable, but have no `work_item_steps`, so no files and no blame.
This is separable from everything above and should ship as its own PR.

## Effort

Solo, senior, with the reuse credits below. Streams after S1 are independent
and suit the delegated-worker pattern (disjoint file allowlists).

| Stream | Work | Reuses | Days |
|---|---|---|---|
| S1 Schema, queue, worker, config — **landed** | tables + migration, `insight_jobs`, lock file, `rgt insight` verbs, `[insight]` tables in both config files, key-by-env; `internal/insight` with the `Processor` seam S4 plugs into | `migrateSchema`, `config`, `store.RepoConfig`, `LogHookError` | 3 |
| S2 Scrub — egress half **landed** in `pipeline.Scrubber` | policy that composes `redact` + user patterns; capture-time hook through a repository `publicgate.Checker` (open); egress pass in the worker | `internal/redact`, `internal/publicgate` | 2 |
| S3 Providers — **landed** (`internal/insight/provider`) | `anthropic` messages, `openai-compatible` chat + embeddings, `command` runner; timeouts, retry, token budget | — | 3 |
| S4 Read: work items and entities — **landed** (`internal/insight/pipeline`) | Appendix A prompt, boundary and continuation, rolling `approach`, deterministic URL/git entities, evidence enforcement, dedupe, fixtures from recorded sessions | `treediff.LineDiff`, `step_files`, `messages` | 5–6 |
| S5 Search — `rgt search` and `rgt work` **landed**; `rgt related` open | three FTS5 tables, cosine + RRF, `rgt search`, `rgt related` via blame join, `rgt work`, not-yet-indexed results | `rgt blame` | 3–4 |
| S6 Skill and docs | `related-work`, `context-primer` update, README/FAQ, `rgt insight status` output | skills embed | 2 |
| S7 Cursor importer | `state.vscdb` reader, `cursor` origin, no trees | capture `Recorder` | 2 |

**18–20 days without Cursor, 20–22 with.** Four weeks solo; about three
with S3, S4, and S5 delegated in parallel after S1 lands. S4 is the stream
that needs the most judgment and the most iteration on real sessions; it
should not be the one delegated blind.

## Risks

- **Cost creep.** One model call per `Stop` per open work item, bounded by
  `max_input_tokens`, is cheap on a Haiku-class model and free on a local
  one, but a busy repository with a large remote model will notice. `rgt
  insight status` reports calls and tokens since enable; a `budget` setting
  is a one-line follow-up once the number is real.
- **Entity sprawl.** An open taxonomy can produce `ticket`, `Ticket`, and
  `linear_issue` for the same thing. Types are lowercased and snake_cased on
  write; the prompt carries the types already present in this repository so
  the model reuses them; dedupe is on `ref` when there is one. If sprawl
  shows up in practice, a `rgt insight entities --merge` verb is a small
  follow-up. This is a better problem than the one it replaces, where a
  ticket format nobody configured was simply invisible.
- **Scrub changes hashes.** Capture-time scrub rewrites message and tool
  payload bytes, so their blob hashes differ from an unscrubbed capture of the
  same turn. Same trade RFC 0004 already made for public projects. Files are
  never rewritten, so trees and blame are unaffected.
- **Boundaries are a judgment.** A wrong work-item boundary makes a goal
  blurrier, not wrong; search still finds the session through messages. The
  evidence step on every link is what keeps a wrong boundary from becoming a
  wrong claim.
- **The hook must stay fast.** The enqueue is one insert; the spawn is
  `exec` with `Setsid`. Both are measured in the hook benchmark before merge.

## Open questions

1. ~~Should `[insight] enabled = true` in a committed config enable it for
   every contributor who has providers configured, or should each user also
   opt in locally?~~ Settled in S1 as proposed: the repository enables, the
   user's `[insight.model]` is the opt-in (`Settings.Active()`). No provider,
   nothing queued, nothing sent.
2. Default `work_item_idle`. Two hours is a guess; S1 ships it as the default
   and reads `[insight] work_item_idle` when set.
3. Whether `command` providers get the scrubbed request on stdin or a path to
   a temp file. Stdin, unless a real agent CLI cannot take it.
4. Whether `wip` items older than some age should be auto-closed as
   `abandoned`, or left `wip` forever so they keep surfacing. Proposal: leave
   them; surfacing is the point.

## Appendix A: the read call

Request (one JSON object on the wire, or on stdin for `command`):

```json
{
  "prompt_version": 1,
  "repository": { "remotes": ["git@github.com:bonez-io/re_gent.git"],
                  "entity_types_in_use": ["ticket", "pull_request", "symbol", "decision"] },
  "open_work_item": { "id": "…", "goal": "…", "approach": "…", "outcome": "…", "status": "wip",
                      "entities": [ … ] },
  "resumable": [ { "id": "…", "session_id": "…", "goal": "…", "status": "wip",
                   "entities": [ … ], "ended_at": "…" } ],
  "turns": [
    { "step": "…", "user": "…", "assistant": "…", "reasoning": "…",
      "tools": ["Edit", "Bash"], "files": ["internal/index/index.go"],
      "hunks": [ { "path": "…", "diff": "…" } ] }
  ],
  "deterministic_entities": [
    { "type": "pull_request", "name": "#95", "ref": "https://github.com/bonez-io/re_gent/pull/95",
      "evidence_step_id": "…" }
  ]
}
```

Response:

```json
{
  "work_items": [
    { "id": "…" | null,
      "continues_work_item_id": "…" | null,
      "starts_at_step": "…",
      "goal": "…", "approach": "…", "outcome": "…",
      "status": "wip" | "done" | "failed" | "abandoned" | "superseded",
      "entities": [
        { "type": "ticket", "name": "1SI-300", "ref": "https://linear.app/…/1SI-300",
          "role": "goal", "confidence": 0.95, "evidence_step_id": "…" },
        { "type": "decision", "name": "SQLite over Postgres for self-hosted",
          "ref": null, "role": "produced", "confidence": 0.8, "evidence_step_id": "…" }
      ] }
  ]
}
```

The worker validates the response against this shape, refuses a response
that drops a deterministic entity or omits an `evidence_step_id`, and bumps
`prompt_version` whenever the prompt text changes so `rebuild` knows which
work items are stale.
