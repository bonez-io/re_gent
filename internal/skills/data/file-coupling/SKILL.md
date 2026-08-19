---
description: Find files that habitually change together, from captured step history. Use before editing a file to learn what usually needs changing with it, or to find hidden structural coupling.
allowed-tools: Bash(rgt log *), Bash(rgt sessions *)
argument-hint: "[path] [--sessions N]"
---

Report which files co-occur in the same captured steps, and how often.

Git can only see files that landed in the same commit. re_gent sees every file an
agent touched in the same *turn* — a much sharper signal, because a turn is one
intention.

## Gather

List sessions, then read their steps as JSON:

```bash
rgt sessions --format json
rgt log --json --session <session-id> -n 200
```

Each step carries `files` (paths written that turn), `timestamp`, and `messages`
(the prompt behind it). Repeat per session so the sample spans the project, not
one conversation.

## Compute

For every step with two or more files, count each unordered pair. Then for each
pair report:

- **together** — steps containing both
- **coupling** — `together / steps containing either` (Jaccard). This is the
  number to sort by; raw counts just rank whatever changes most.

Ignore steps whose file list is enormous (a dependency install or a formatter run
touches everything and couples nothing). More than ~20 files in one step is
usually machine noise, not intent.

If the user named a path, report only pairs involving it.

## Report

A table sorted by coupling, strongest first:

| File | Couples with | Together | Coupling |
|---|---|---|---|

Then, for the top two or three pairs, quote the prompt from one step where they
moved together — from that step's `messages`. The number says *that* they are
coupled; the prompt says *why*, and only re_gent can answer the second.

Close with the actionable line: which files the user should expect to edit
alongside the one they asked about.
