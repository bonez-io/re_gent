---
description: Read captured conversations to learn how this person actually works, then propose skills tailored to them. Use to discover which repeated workflows are worth turning into skills.
allowed-tools: Bash(rgt log *), Bash(rgt sessions *), Bash(rgt show *)
argument-hint: "[--sessions N]"
---

Study the user's own captured history and propose skills that fit how they
actually work — not how a generic developer works.

## Gather

```bash
rgt sessions --format json
rgt log --json --session <session-id> -n 200
```

Read prompts across sessions, most recent first. Twenty or thirty sessions is
plenty; this is about recurring shape, not completeness.

## Look for

**Repeated intent.** The same request in different words, three or more times.
Phrasing varies; intent repeats. That repetition is the signal — a task done once
is a task, done repeatedly it is a workflow, and a workflow is a skill.

**Standing corrections.** Instructions given again and again ("use tabs", "don't
add comments", "run the tests first"). Every repeat is a preference the agent
keeps forgetting, and each one is a candidate skill or a project instruction.

**Expensive sequences.** Multi-step routines the user drives by hand each time —
check the log, find the step, read the diff, run the tests. Sequences are where a
skill saves the most.

**Vocabulary.** The words this person uses for their own domain. A good skill
speaks their language, not the framework's.

**Dead ends.** Requests that took several turns to land. A skill that encodes the
right approach up front removes the fumbling.

## Propose

Three to five skills, strongest first. For each:

```
Name           <kebab-case>
What it does   <one sentence>
Evidence       <N sessions; quote 2 real prompts, verbatim>
Why a skill    <what it saves: repetition, sequence length, forgotten preference>
Data used      <which rgt commands it would run>
```

Rank by evidence, not by how clever the skill sounds. A dull skill backed by nine
occurrences beats an elegant one backed by one.

Quote real prompts. The user should recognise themselves — that recognition is
what makes them trust the suggestion.

Say explicitly what you did **not** find enough evidence for. A short honest list
is worth more than five speculative skills.

## Then

Offer to build any of them with the **skill-factory** skill. Do not write the
files unless asked.
