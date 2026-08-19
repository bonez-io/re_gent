---
description: Load everything captured history knows about a file or area before work starts, so the agent begins warm instead of from zero. Use at the start of a task, before editing unfamiliar code.
allowed-tools: Bash(rgt blame *), Bash(rgt log *), Bash(rgt show *), Bash(rgt sessions *)
argument-hint: "<path> or <area description>"
---

Brief the agent on a file *before* it edits it.

An agent starts every session knowing nothing about how this code came to be. It
re-derives decisions that were already made, and re-makes mistakes that were
already corrected. re_gent has that history. This skill loads it.

## Gather

```bash
rgt blame <path>                       # which steps own which lines
rgt log --json --session <id> -n 100   # per session, the steps and prompts
rgt show <step-hash>                   # full context for the important ones
```

From `rgt blame`, take the distinct step hashes owning lines in the file — those
are the turns that built it. Read each with `rgt show`, oldest first. Pull the
prompt, the assistant's stated reasoning, and the files that moved alongside.

Cap it: the ten or so steps that own the most lines are enough. This is a briefing,
not an archive.

## Distil

Produce a briefing, not a log. Chronological, but only what changes how someone
would edit this file today:

**Purpose** — what this file is for, inferred from the prompts that built it, not
from its code.

**Decisions already made** — choices someone made deliberately, with the prompt
that drove them. These are the things not to relitigate.

**Corrections** — where a later step reversed an earlier one. Quote both prompts.
This is the highest-value section: it is the record of what was already tried and
rejected, and it is exactly what a cold agent repeats.

**Constraints in force** — anything a prompt asked for that still binds ("keep the
API stable", "no new dependencies").

**Usual company** — files that move with this one (see the **file-coupling**
skill). Editing here probably means editing there.

**Open threads** — anything a prompt asked for that no later step appears to have
finished.

## Report

Lead with three sentences a person could read aloud: what this file is, the one
decision most likely to be accidentally undone, and what else to expect to touch.

Then the sections above. Cite step hashes so any claim can be checked with
`rgt show`. Where history is thin, say so plainly rather than inferring from the
code — the value here is knowing *why*, and guessing defeats it.
