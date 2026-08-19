---
description: Trace a bug or incident back through captured history to the change that caused it, the prompt behind that change, and its blast radius. Use when something broke and you need to know what changed and why.
allowed-tools: Bash(rgt blame *), Bash(rgt log *), Bash(rgt show *), Bash(rgt sessions *)
argument-hint: "<path>[:<line>] or a description of the symptom"
---

Answer four questions about a defect, in order: **what changed, when, why, and what
else moved with it.**

`git blame` answers the first two. Only re_gent answers the third — the prompt that
caused the edit — and the fourth is what turns a fix into a safe fix.

## 1. What changed

If given a path or `path:line`, start there:

```bash
rgt blame <path>[:<line>]
```

Each line carries the step hash that last wrote it. Take the step for the suspect
line. If given only a symptom, find candidate files first with
`rgt log --json -n 50` and match against the described behaviour.

## 2. When, and in what company

```bash
rgt show <step-hash>
```

This gives the full step: timestamp, tool calls with their arguments and results,
every file the turn touched, and the surrounding conversation.

## 3. Why — the prompt

Read the `user` message on that step. This is the instruction the agent was
following. State it verbatim. Very often the bug is not a coding mistake but a
faithful execution of an ambiguous or wrong instruction, and that distinction
changes the fix: rewrite the code, or rewrite the expectation.

Check the assistant message too. If the agent explained a tradeoff or flagged an
assumption, the defect may be a known compromise rather than an accident.

## 4. Blast radius

The step's `files` list is the immediate radius — everything that turn changed
under one intention. Widen it:

- Other steps in the same session, before and after (`rgt log --session <id>`),
  since a multi-turn task spreads one intention across several steps.
- Files that habitually travel with the suspect file — invoke the
  **file-coupling** skill. A file coupled to the culprit but *not* touched in this
  step is the highest-value thing to check: it usually should have changed and
  did not.

## Report

```
Symptom     <what the user described>
Cause       step <hash> · <timestamp> · <tool>
Prompt      "<the user instruction, verbatim>"
Reasoning   <what the agent said it was doing, if relevant>
Changed     <files in that step>
Also check  <coupled files that did NOT change — likely gaps>
Session     <session id>, step N of M
```

Finish with a recommended next action: fix forward, or `rgt rewind <step>` to
restore the workspace to before the change. Name the rewind target explicitly and
say that rewind snapshots the current state first, so it is reversible.
