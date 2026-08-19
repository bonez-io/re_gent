---
description: Discover and install re_gent skills. Use when the user asks what re_gent skills exist, wants to add one, or asks a question about their own history that no installed skill covers.
allowed-tools: Bash(rgt skill list *), Bash(rgt skill install *)
argument-hint: "[skill name, or what you are trying to do]"
---

Find the right re_gent skill and install it.

This is the skill that installs skills. It ships with `rgt init` so a project
that has no other skills can still find them: without it, the catalog exists but
nothing inside the agent knows to look.

## 1. See what is on offer

```bash
rgt skill list
```

The list comes from the project's re_gent server when one is configured, and
from this `rgt` binary otherwise. It names which, so say which you are reading —
a teammate's published skill only appears in the first case.

## 2. Choose by the question, not the name

Map what the user is actually trying to do:

| They are asking | Skill |
|---|---|
| "why is this line here", "what broke this" | `bug-blame` |
| "what usually changes with this file" | `file-coupling` |
| "what do I need to know before editing this" | `context-primer` |
| "which prompt wrote this line" | `blame` |
| "what did the agent do" | `log`, `show` |
| "what should I automate" | `style-factory` |
| "build me a skill that…" | `skill-factory` |

If nothing fits, say so and offer `skill-factory`, which writes a new one. Do not
install something approximate and hope.

## 3. Show the grant, then install

```bash
rgt skill install <name> [<name>...]
```

The command prints each skill's `allowed-tools` as it writes it. **Read that line
back to the user.** A `SKILL.md` is executable instruction, not documentation:
its grant decides what it may run, and a skill fetched from a server is exactly
the case where the user should be able to say no first.

Flags worth knowing:

- `--agent codex` writes to `.agents/skills/` instead of `.claude/skills/`. Use
  it when the user works in Codex, or the file lands where nothing loads it.
- `--force` replaces a skill the user has edited. Never pass it unprompted; the
  default refusal exists because their edit outranks the shipped copy.
- `--server <url>` installs from a specific registry.

## 4. Say what has to happen next

Agents load skills at startup, so a freshly installed skill does nothing until
the session restarts. Tell the user plainly. Do not claim the skill is usable
in the current conversation, because it is not.

## Report

What you installed, where each file went, the tool grant for each, and the
restart. If you installed nothing, say why — an unclear request is a reason to
ask, not to guess.
