---
description: Create a new re_gent skill from a plain-language description, writing a valid SKILL.md. Use when the user wants a new skill, or to build one that style-factory proposed.
allowed-tools: Bash(rgt log *), Bash(rgt sessions *), Read, Write
argument-hint: "<description of what the skill should do>"
---

Turn a description into a working skill file.

A skill is a prompt plus a tool grant. The intelligence is the agent's, the data is
re_gent's, and the skill is the wiring between them — so writing one is mostly
deciding *what to ask for* and *what it is allowed to run*.

## 1. Settle the shape

Before writing, be able to answer in one sentence each:

- **Trigger** — when should an agent reach for this? This becomes `description`,
  and it is the single most important line in the file: it is what the agent
  matches against. Name the situation, not the mechanism.
- **Input** — a path, a step hash, a symptom, nothing? This becomes
  `argument-hint`.
- **Data** — which `rgt` commands supply the facts.
- **Output** — what the user should be holding at the end.

If the request is vague, ask one question. A skill built on a guess triggers at the
wrong moment and is worse than no skill.

## 2. Check it is not already there

```bash
ls .claude/skills/
```

Read any neighbour that looks close. Extending an existing skill beats adding a
near-duplicate — two skills with overlapping descriptions make the agent pick badly.

## 3. Write it

`.claude/skills/<kebab-case-name>/SKILL.md`:

```markdown
---
description: <what it does + when to use it, one sentence>
allowed-tools: Bash(rgt <command> *)
argument-hint: "<arg>"
---

<Instructions to the agent.>
```

Rules that matter:

- **Grant the narrowest tools that work.** `Bash(rgt log *)` — never bare `Bash`.
  A skill that only reads should never be able to write.
- **Write instructions, not prose.** The reader is an agent about to act.
- **Say what to do with the data**, not just how to fetch it. The gathering is the
  easy half; the analysis is the skill.
- **Prefer real commands you have verified exist.** Check with `rgt <cmd> --help`
  before referencing a flag. A skill that cites a flag that does not exist fails at
  the moment it is trusted.
- **Name sibling skills** the agent should chain to, so skills compose.

## 4. Verify before claiming it works

```bash
rgt <each command in allowed-tools> --help
```

Confirm every command and flag exists. Then state plainly what you could not test —
a skill's real behaviour only shows when an agent runs it against real history.

## Report

Path written, the `description` line verbatim (so the user can judge when it will
trigger), the commands it may run, and one example invocation.
