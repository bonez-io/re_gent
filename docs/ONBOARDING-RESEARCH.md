# Onboarding research — should `rgt init` be an interactive wizard?

**Question.** For tools that must wire themselves into someone else's tool before any data exists, does an interactive setup wizard improve activation — or has the field converged on a single non-interactive auto-detecting command?

**Method.** Primary sources only: project source code, official docs, changelogs, the actual PRs/issues where onboarding changed, and first-party maintainer writing. Plus a first-hand teardown of re_gent's own `internal/cli/init.go` and its shipped binary.

**Evidence labels.** Every substantive claim is tagged *(evidenced)* — verified against a cited source or reproduced locally; *(inferred)* — a reasoned conclusion drawn from evidenced facts, not itself stated by any source; *(asserted)* — my judgement, offered as a recommendation with no external backing.

---

## 1. Verdict

**Invert it. Make `rgt init` non-interactive and auto-detecting by default, and add `rgt doctor`.**

Four findings drive this, in descending order of force:

1. **The premise of the decision was wrong: the wizard is not unshipped.** The `huh` multi-select and the `Install skills? [Y/n]` prompt have been in every public release since **v1.0.0 on 2026-05-14** *(evidenced)*. The decision is therefore not "should we ship a wizard" but "the wizard has been live for three months — what has it done?"
2. **The one field report we have is a casualty report.** Issue #45 was filed **five days after v1.0.0** by a third-party integrator who copied re_gent's hook-writing code into his own project because `rgt init` demands a TTY he cannot provide *(evidenced)*.
3. **`rgt init` currently reports success while installing nothing.** Reproduced against the committed binary: with no TTY it exits 0, prints `✓ Initialization complete`, and creates no `.claude/` directory. `rgt status`, `rgt log`, and `rgt sessions` then all exit 0 without ever mentioning that no hooks are wired *(evidenced)*.
4. **The brief's central premise — "Sentry and Storybook disagree, and that disagreement is the spine of the verdict" — no longer holds. Sentry is abandoning the wizard.** In 2026 Sentry closed every open non-interactive/agentic wizard PR with *"the wizard will be superseded by `sentry init`"*, and its own VP of Engineering shipped documentation telling AI agents not to run the wizard because *"the wizard has no supported non-interactive mode"* *(evidenced — see §2.2)*. There is no live disagreement left to split.

5. **The closest analogue in the field faced this exact choice six weeks ago and chose non-interactive.** lefthook shipped hook installation into `.claude/settings.json` and `.codex/hooks.json` in [PR #1448](https://github.com/evilmartians/lefthook/pull/1448) (merged 2026-07-08). Its design issue [#1433](https://github.com/evilmartians/lefthook/issues/1433) explicitly weighed *"Option 2 – Ask user… Asks if you prefer to use Claude or Codex… check existing `.claude/` and `.codex/` dirs"* — which is `offerHookInstall` almost line for line — and shipped the declarative option instead *(evidenced; §7.1, spot-verified)*.

The field has **not** converged on "no prompts ever." It has converged on something narrower: **prompts that gate a mechanically detectable fact get deleted; every remaining prompt must have a non-interactive equivalent; and the tool must never claim success it has not achieved.** re_gent's one prompt gates a fact it already detects correctly, has no non-interactive equivalent, and precedes a false success banner. It fails all three.

### 1.1 What `rgt init` should be

Written against the real code in `internal/cli/init.go`.

**Default behaviour — `rgt init` with no flags:**

1. Create `.regent/` exactly as today (lines 87–107). Unchanged.
2. Call `resolveAgentTargets(cwd, agentAuto)` (lines 862–879). **Unchanged — this is already good auto-detection** and it already works; it checks both the marker directory and `commandExists` on PATH *(evidenced: reproduced — a bare temp dir correctly detected Claude Code and Pi)*.
3. **Install hooks for every detected target, with no prompt.** Call `installClaudeHook` / `installCodexHook` / `installOpenCodeHook` / `installPiHook` directly, the way `connectWireHooks` in `connect.go:139` already does.
   **Write the absolute path of the running binary, not the bare string `rgt`.** `init.go:36-39` embeds `rgt message-hook user`, which depends on PATH resolution inside the agent host's environment. lefthook — which shipped this exact feature for Claude Code and Codex in July 2026 — resolves the binary via `os.Executable()` for the documented reason *"so AI tools do not depend on `lefthook` being on `PATH`"* ([docs/configuration/ai.md](https://github.com/evilmartians/lefthook/blob/master/docs/configuration/ai.md), [install_ai.go `resolveLefthookBin`](https://github.com/evilmartians/lefthook/blob/master/internal/command/install_ai.go)) *(evidenced — I verified the file, the function, and the quoted line)*. Keep a config override, as lefthook does.
4. **Print what was done, per file touched** — the pattern `pre-commit` uses (`pre-commit installed at {hook_path}`) and that `claude mcp add` uses (an `Added ...` line). Name the actual paths written.
5. **Print the restart warning — and make it conditional on create-vs-modify.** `connect.go:148-152` already carries a blanket version, with a code comment calling it *"the single most common 'why wasn't my change captured?' cause"*; `rgt init` does not print it at all. But the real rule is sharper than either. Claude Code's settings watcher **only watches directories that already contained a settings file when the session started**, so *modifying* an existing `.claude/settings.json` hot-reloads and the hook is live immediately, while **creating one for the first time mid-session produces a correctly-written, completely inert hook** until `/hooks` is opened or the session restarts *(evidenced — see §2.5)*. That is exactly re_gent's first-run case. `installClaudeHook` already knows which case it is in (`os.ReadFile` at line 269 succeeds or does not); it should warn loudly only on create, and stay quiet on modify *(asserted)*.
6. Install skills by default; no prompt.
7. End with one verification instruction: `run rgt doctor`.

**Which settings file to write.** Keep writing `.claude/settings.json` (project scope), which is what `installClaudeHook` does today. Two supporting facts *(evidenced)*: hook arrays **merge across settings levels — "concatenated and deduplicated, not replaced"** — so re_gent cannot clobber another tool's hooks and cannot be clobbered by one; and Claude Code auto-adds `settings.local.json` to git excludes **only when Claude itself writes it**, so a programmatic write to `settings.local.json` would silently leak into the user's repo unless re_gent added the ignore entry itself. Project scope is also what `.regent/.gitignore` already assumes teammates will inherit (`init.go:728-733`).

**Flags:**

| Flag | Behaviour |
|---|---|
| `--agent` | Keep as-is. Already correct. |
| `--skip-hook`, `--skip-skills` | Keep. These are the opt-outs. |
| `--interactive` | **New.** The *only* way to reach the `huh` multi-select. Keeps the built work available without it being in the default path. |
| `--json` | **New, recommended.** What issue #45's author actually needs to integrate without screen-scraping *(inferred)*. |

**Exit codes and honesty — the part that matters most:**

- `printSummary(cwd, targets)` at line 134 passes the **detected** list, not `installedTargets`. It must pass `installedTargets` *(evidenced: this is why the non-TTY run prints "Agent skills: log, blame, show" having installed no skills)*.
- If hook installation **fails** (as opposed to being skipped via `--skip-hook`), do not print `✓ Initialization complete`, and **exit non-zero**. Precedent: `pre-commit` returns 1 with `Cowardly refusing to install hooks with core.hooksPath set` rather than complete a install that would silently never fire *(evidenced)*.
- If `--interactive` is used and the user selects nothing, exit cleanly with a pointer to docs — do **not** continue to a success banner. Precedent: Storybook PR #23201, which fixed this exact bug in 2023 *(evidenced)*.

**When `--interactive` is genuinely warranted:** only on real ambiguity. `resolveAgentTargets` has one such case — when nothing is detected it falls back to `[claude, codex]` (line 876-878), a guess. That is the one place a prompt earns its keep, and only when a TTY exists *(asserted)*.

**Do not use `huh` on the default path.** The repo already has the right primitive: `interactive()` in `discover.go:111`, whose comment states the rationale — prompting under `curl ... | sh` *"would swallow the rest of the script or block forever"* *(evidenced)*.

### 1.2 Cost

Small. Every hook-writing function already exists and is unit-tested; `connect.go` already calls one of them with no prompt; TTY detection already exists in the same package. The change is deleting a gate, not building a feature *(inferred)*.

---

## 2. Pattern catalog

### Pattern A — One non-interactive auto-detecting command

**Who ships it:** Storybook, pre-commit, husky, lefthook, `claude mcp add`.

- **Storybook.** `npm create storybook@latest`. Auto-detects the package manager (nearest lockfile, then `npm_config_user_agent`), the project type (ordered match over `package.json` deps, React last), the builder (config files or deps), and the language. `--type` is the escape when detection fails. ([ProjectTypeService.ts](https://github.com/storybookjs/storybook/blob/next/code/lib/create-storybook/src/services/ProjectTypeService.ts), [JsPackageManagerFactory.ts](https://github.com/storybookjs/storybook/blob/next/code/core/src/common/js-package-manager/JsPackageManagerFactory.ts)) *(evidenced)*
  **The single most directly copyable line in this study** is `UserPreferencesCommand.ts:62-63`, which suppresses *every* prompt when `!process.stdout.isTTY || isCI() || --yes || --agent` *(evidenced)*. Storybook detects a non-TTY and proceeds with defaults; re_gent detects a non-TTY and aborts hook installation while printing success. Storybook also ships an explicit **`--agent`** mode that suppresses prompts — the field is now designing for agents as first-class callers.
- **pre-commit.** `pre-commit install`. Zero prompts — a code search for `input(` across the repo returns nothing. Prints `pre-commit installed at {hook_path}`. ([install_uninstall.py](https://github.com/pre-commit/pre-commit/blob/main/pre_commit/commands/install_uninstall.py)) *(evidenced)*
- **husky.** `npx husky init`. The entire installer is ~30 lines with no prompt of any kind; `init` writes the `prepare` script, runs the installer, and writes a starter `.husky/pre-commit`. ([index.js](https://github.com/typicode/husky/blob/main/index.js), [bin.js](https://github.com/typicode/husky/blob/main/bin.js)) *(evidenced)*
- **lefthook.** `lefthook install`. Flags only (`--force`, `--reset-hooks-path`, `--verbose`); the only stdin prompt in the codebase is `self-update`'s `[Y/n]`. There is no `lefthook init`. ([internal/command/install.go](https://github.com/evilmartians/lefthook/blob/master/internal/command/install.go)) *(evidenced)*
- **Claude Code — the host re_gent hooks into.** `claude mcp add --transport http notion https://mcp.notion.com/mcp` is entirely flag-driven, plus `claude mcp add-json`. ([code.claude.com/docs/en/mcp](https://code.claude.com/docs/en/mcp)) *(evidenced)*

**The organizing principle, and the sharpest finding in the study.** Across Storybook, Sentry, ESLint and shadcn, prompt history sorts cleanly into two classes *(evidenced; the generalization across projects is inferred)*:

> **Prompts that gate a mechanically detectable fact get replaced by detection or deleted. Prompts that harvest user intent or product signal survive — and even those get pruned toward binary.**

Detectable facts deleted: Storybook's project type (shipped ~7 years, deleted in [PR #32717](https://github.com/storybookjs/storybook/pull/32717), 10.1.0, 2025-11-26) and its ESLint prompt (given a `--yes` bypass in 2023, then deleted outright in [#25289](https://github.com/storybookjs/storybook/pull/25289), 8.0.0); shadcn's TypeScript, CSS-location, alias, RSC and base-color prompts (10 prompts → 1 conditional, 2023-12 → 2026-03); Sentry's TypeScript detection ([#580](https://github.com/getsentry/sentry-wizard/pull/580)); ESLint's entire style micro-quiz. Intent prompts that survive: Storybook's "New to Storybook?", Sentry's "create an example page?", ESLint's "which framework".

**This decides re_gent's case on its own.** "Which agent hosts are configured in this directory" is a **mechanically detectable fact** — and `resolveAgentTargets` already detects it correctly, which I verified by running it. It is precisely the class of question the entire field has spent five years deleting *(inferred)*.

**Maps onto `rgt init`:** this is the recommendation. `resolveAgentTargets` is already a working auto-detector; it is simply gated behind a prompt.

**Counter-evidence, reported.** Storybook's project-type deletion was not costless: users filed [#35045](https://github.com/storybookjs/storybook/issues/35045) (2026-06-03) when init began hard-failing on undetected frameworks, and two community PRs to reinstate an interactive choice have not landed *(evidenced)*. The lesson is not "don't delete the prompt" — Storybook has held the line for nine months — but **"when detection fails, fail loudly with tips rather than silently guessing."** re_gent's `resolveAgentTargets` currently does the opposite: on total detection failure it silently guesses `[claude, codex]` (`init.go:876-878`) *(evidenced)*. That fallback should become an explicit, visible statement of what it assumed *(asserted)*.

### Pattern B — Interactive wizard, with a non-interactive story bolted on later (and badly)

**Who ships it:** Sentry, ESLint.

#### 2.1 Sentry's flag surface promises more than it delivers

The [`@sentry/wizard` README](https://github.com/getsentry/sentry-wizard) advertises a flag and an environment variable for most prompts — `--quiet` / `SENTRY_WIZARD_QUIET` (*"Do not fallback to prompting user asking questions"*), `-i/--integration`, `--org`, `--project`, `--saas`, `-u/--url`, `--skip-connect`, `--ignore-git-changes` *(evidenced)*.

**That table is misleading, and I initially misread it as a complete bypass.** Against the source *(evidenced)*:

- **`--quiet` and `--skip-connect` are dead code for every modern integration.** `src/run.ts` threads `quiet` only into `legacyRun` for `cordova` and `electron`; the sole consumers are the inquirer-era `lib/Helper/Wizard.ts` and `lib/Steps/PromptForParameters.ts`. Next.js, Nuxt, Remix, SvelteKit, React Native, Android, Flutter, Angular and sourcemaps all ignore it. The README documents it without this caveat.
- **`process.stdout.isTTY` appears nowhere in the repo.** There is no CI detection — `process.env.CI` is used only for cosmetic glyphs and inside generated user code.
- `--org`/`--project` narrow the project picker but **do not skip the browser OAuth login**, which opens a browser and polls with a 180-second timeout.
- The only genuine auth bypass is **nine hidden, undocumented `--preSelectedProject.*` flags** used by Sentry's own in-product onboarding.
- A real `--non-interactive` flag was added on **2026-07-01** ([PR #1297](https://github.com/getsentry/sentry-wizard/pull/1297), v6.13.0) — but it is wired to **exactly one** integration, Apple Snapshots, which is the one wizard with no authentication step. It is still absent from the README.

#### 2.2 Sentry's own maintainers say the wizard cannot be automated — and are replacing it

This is the strongest external evidence in the memo, and it points the same way as everything else *(all evidenced)*:

- **2023-11-03.** The repo's only CI complaint ([#488](https://github.com/getsentry/sentry-wizard/issues/488)) — a user hit a blocking prompt under `--quiet --skip-connect` — was answered by maintainer Lms24: *"running a wizard/scaffolding tool was always a local matter."* Six days later he opened [#491](https://github.com/getsentry/sentry-wizard/issues/491) proposing to **delete `--quiet`** because *"wizards are usually executed locally by users."*
- **2026-03-02.** Daniel Griesser (Sentry VP Engineering) merged [sentry-for-ai PR #18](https://github.com/getsentry/sentry-for-ai/pull/18), *"mark wizard as user-run-only, add manual setup fallback"*, stating: *"The wizard has no supported non-interactive mode"* and *"Hidden undocumented flags exist but are incomplete and unreliable."*
- The resulting text is still live in Sentry's shipping AI plugin ([src/references/sdks/nextjs/index.md](https://github.com/getsentry/sentry-for-ai/blob/main/src/references/sdks/nextjs/index.md)): **"You need to run this yourself — the wizard opens a browser for login and requires interactive input that the agent can't handle."**
- **2026-07-24 / 2026-08-03.** Every open agentic/non-interactive wizard PR was closed unmerged — [#1183](https://github.com/getsentry/sentry-wizard/pull/1183) (open 7 months with active review), [#1187](https://github.com/getsentry/sentry-wizard/pull/1187), [#997](https://github.com/getsentry/sentry-wizard/pull/997) — with *"the wizard will be superseded by `sentry init`"* and *"we de-prioritized all feature work related to the wizard in favour of the AI-based `sentry init` SDK setup"*.
- **The replacement has what the wizard never got.** [getsentry/cli](https://github.com/getsentry/cli)'s `sentry init` ships `--yes`, `--dry-run`, `--features`, `--no-tui`, agent detection via process-tree walking, and a real TTY guard: `if (!(options.yes || options.dryRun || process.stdin.isTTY))` → *"Interactive mode requires a terminal. Use --yes for non-interactive mode."*

#### 2.3 Sentry's prompt history: removals dominate

*(evidenced, dated, from the [sentry-wizard CHANGELOG](https://github.com/getsentry/sentry-wizard/blob/master/CHANGELOG.md) and linked PRs)*

- **2024-06** [#580](https://github.com/getsentry/sentry-wizard/pull/580) — detect TypeScript instead of asking.
- **2024-10-16** [#690](https://github.com/getsentry/sentry-wizard/pull/690) — stop asking for the package manager twice.
- **2025-02-26** [#817](https://github.com/getsentry/sentry-wizard/pull/817) — skip the account question when `--org`/`--project` are given.
- **2025-03-17** [#858](https://github.com/getsentry/sentry-wizard/pull/858) — **remove the `reactComponentAnnotation` prompt entirely**, eight months after [#634](https://github.com/getsentry/sentry-wizard/pull/634) added it. A prompt added and reverted.
- **2025-03-24** [#819](https://github.com/getsentry/sentry-wizard/issues/819) — `--ignore-git-changes`, filed by Sentry's own engineer because the prompt annoyed *him*.
- **2025-10** [#1052](https://github.com/getsentry/sentry-wizard/pull/1052)–[#1060](https://github.com/getsentry/sentry-wizard/pull/1060) — `sendDefaultPii: true` **defaulted instead of asked**.

Against these, the notable addition is the 2025 MCP prompt ([#1063](https://github.com/getsentry/sentry-wizard/pull/1063)), later expanded into a multi-select with a "What is MCP?" branch that re-asks the question. Net direction: **prompts removed, defaults chosen, and the whole tool superseded.**

**Maps onto `rgt init`:** this is the pattern re_gent currently sits closest to, minus the flags. It is the pattern its own author is walking away from. re_gent has no reason to adopt in 2026 the design Sentry is retiring in 2026 *(asserted)*.

**Why Sentry could justify prompts at all, and re_gent cannot** *(inferred)*: the Sentry wizard authenticates you, picks a cloud org and project, and injects a DSN — none of which is discoverable from the filesystem, which is exactly why its browser-login step defeats automation. re_gent needs no account, no key, and never calls an LLM. Its only question is "which agent hosts are in this directory", and `resolveAgentTargets` already answers it correctly without asking *(evidenced)*.

#### 2.4 ESLint — the same pattern, same direction

*(evidenced, from the [create-config CHANGELOG](https://github.com/eslint/create-config/blob/main/CHANGELOG.md))*:
- `feat: move eslint --init (#1)` — the wizard was **moved out of the core `eslint` binary** into a separate `@eslint/create-config` package (ESLint PR #15150).
- `feat: remove style guides (#108)` and `feat: Remove Google style guide (#82)` — prompts deleted.
- `feat: support --config (#38)` — a non-interactive escape added.

Prompts removed, flags added, wizard demoted out of the main binary. No movement the other way.

**Maps onto `rgt init`:** this is the fallback design if the wizard is kept. It would require adding a flag for the agent selection (`--agent` — already exists) *and* honouring it without a TTY (currently impossible). Pattern A is strictly less work.

### 2.5 The host ecosystem — where the evidence pushes back

This is the comparison closest to re_gent, and it is the one place where the "one non-interactive command" thesis does **not** cleanly win. Reported in full because it cuts against the verdict.

**The host itself is flag-driven** *(evidenced, [code.claude.com/docs/en/mcp](https://code.claude.com/docs/en/mcp), [plugins-reference](https://code.claude.com/docs/en/plugins-reference))*:
- `claude mcp add [--transport] [--scope] [--env] [--header] <name> <commandOrUrl>` — no wizard. `add-json`, `get`, `list` likewise. The only interactive MCP surfaces are the Claude-Desktop *import* dialog and the `/mcp` management panel, which cannot add a server.
- *"Claude Code provides CLI commands for non-interactive plugin management, useful for scripting and automation."*
- **`/hooks` is read-only**: *"The `/hooks` menu is read-only. To add, modify, or remove hooks, edit your settings JSON directly or ask Claude to make the change."* There is no official hook-creation wizard to imitate.

**But the third-party ecosystem leans the other way** *(evidenced)*. Of ten verified tools that touch Claude Code config, only four write `settings.json` programmatically at all: two are wizard-primary with non-interactive flags as an escape hatch (`claude-code-templates` `--hook X --yes`; `claudekit setup --yes --hooks a,b`), two are wizard-only (`ccstatusline`, `vibe-log-cli`), four are manual copy-paste, and two ship as plugins and write nothing. **There is not one pure `npx X install` one-shot with no interactive mode in the sample.** *(Caveat: the sample skews toward statusline/usage tools and GitHub code search rate-limited during collection, so treat this as a floor, not a census.)*

**How I read that** *(inferred)*: it is evidence that a wizard is *normal* here, not that it is *effective* — none of these tools publishes activation data either. And two structural facts blunt it. All four programmatic installers **merge** rather than overwrite, so merging is already a settled norm and not the thing under debate. And the host's own resolution of wizard-vs-flags is neither: **one code path with two front-ends.** `claude plugin install --config key=value` stores values *"via the same path as the interactive `/plugin configure` flow"*, and `claude import` pairs an interactive picker with `--dry-run` and `--yes=<digest>`, where the digest binds a headless approval to the exact preview the user saw *(evidenced)*.

That is precisely the §1.1 recommendation: keep `offerHookInstall` behind `--interactive`, and have both front-ends call the same already-tested `installClaudeHook`. re_gent's problem is not that it has a wizard; it is that the wizard is the **only** path.

**Two host facts that affect re_gent's roadmap regardless of this decision** *(evidenced)*:
- **`--bare`** *"skips hooks, LSP, plugin sync…"* and the docs state it *"will become the default for `-p` in a future release."* If that lands, hooks are off by default in headless mode — a distribution risk for any hook-based capture tool.
- **Plugins are the sanctioned distribution path.** Hooks can ship in a plugin's `hooks/hooks.json`, activated by `/plugin install`, with no settings.json write at all; `SuperClaude`, `tdd-guard` and others have migrated. Anthropic even ships a `plugin-hints` protocol letting a CLI advertise its plugin from inside a session, with the hard limit *"Claude Code never installs a plugin automatically. The user always confirms."* Worth evaluating separately as a second distribution channel for re_gent — out of scope here.

### Pattern C — Print the wiring, let the user install it

**Who ships it:** direnv, OpenTelemetry's Python bootstrap, and re_gent's own `printManualInstructions` (`init.go:690`).

- **direnv refuses to edit your shell rc.** `cmd_hook.go`'s only output sink is `hookTemplate.Execute(os.Stdout, ctx)`; there is no `os.Create`/`os.WriteFile`/`os.OpenFile` in it at all. The docs say *"Add the following line at the end of the `~/.zshrc` file"*, and even `install.sh` declines, printing *"The last step is to configure your shell to use it…"*. There is no prompt anywhere in the codebase. A `direnv init` wizard was requested in [#1202](https://github.com/direnv/direnv/issues/1202) (2023-11-29) and remains open and unimplemented. ([cmd_hook.go](https://github.com/direnv/direnv/blob/master/internal/cmd/cmd_hook.go), [docs/hook.md](https://github.com/direnv/direnv/blob/master/docs/hook.md)) *(evidenced)*
- **OpenTelemetry's `opentelemetry-bootstrap` defaults to printing, not installing.** `-a/--action` defaults to `requirements`, which prints to stdout — *"Action can be piped and appended to a requirements.txt file"*. Installing requires an explicit `-a install`. ([bootstrap.py](https://github.com/open-telemetry/opentelemetry-python-contrib/blob/main/opentelemetry-instrumentation/src/opentelemetry/instrumentation/bootstrap.py)) *(evidenced)*

**Maps onto `rgt init`:** already present as the failure fallback (`printManualInstructions`). Keep it as a fallback. It is not a viable default for re_gent, because unlike a shell rc line, re_gent's hook config is a nested JSON/TOML merge into a file that may already have other tools' hooks in it — which is exactly why `installClaudeHook` does structured merging rather than emitting a snippet *(inferred)*.

### Pattern D — Auto-heal on drift

**Who ships it:** lefthook, alone in the comparison set.

lefthook writes `.git/info/lefthook.checksum` containing an md5 of the *merged* config, and re-verifies it on **every hook invocation** — `internal/command/run.go:92-103` calls `syncHooks` when the checksum has drifted, so editing `lefthook.yml` self-heals on the next git operation. It guarantees a trigger point by always installing a silent "ghost" `prepare-commit-msg` hook even when the config declares none. ([install.go](https://github.com/evilmartians/lefthook/blob/master/internal/command/install.go), [run.go](https://github.com/evilmartians/lefthook/blob/master/internal/command/run.go)) *(evidenced)*

**Maps onto `rgt init`:** genuinely interesting for re_gent, whose hooks live in a file the user may edit or a teammate may overwrite. Out of scope for this decision, but the closest thing in the field to making "wired but stale" structurally impossible *(asserted)*. Worth a follow-up.

### Pattern E — Deprecate the imperative subcommands entirely

**husky v9**, 2024-01-25: `husky add`, `husky set`, and `husky uninstall` were **removed** — `bin.js` exits 1 with a deprecation notice — and `husky install` was deprecated in favour of `husky init`. The package shrank from ~53 kB unpacked (v4) to ~3.6 kB (v9). ([bin.js](https://github.com/typicode/husky/blob/main/bin.js)) *(evidenced)*

**Maps onto `rgt init`:** the direction of travel in this family is fewer commands and fewer questions, not more.

---

## 3. The falsifier — reported result

### 3.1 Primary falsifier: "interactivity is itself the liability"

**It fired. Decisively — and the strongest evidence came from inside re_gent's own repository.**

I went looking for the opposite. I specifically searched for projects that *added* interactivity over time, that defended prompts as an activation win, or that reverted a non-interactive default. Here is what I searched and what I found.

**Searched for: a project that added prompts over time.** Found the reverse in every case with a documented history. ESLint moved its wizard out of core and deleted two prompt options. husky removed three subcommands. Storybook added `--yes`, `--features` ("skipping the prompt"), and `--type`. pre-commit's 2,456-line changelog contains **zero** occurrences of "interactive" or "prompt" — the wizard was never there to remove *(evidenced)*. **No counter-example found.**

**Searched for: maintainers defending interactivity.** The best candidate was Sentry, and it collapsed on inspection. The only sustained defence I found is Lms24's 2023 *"running a wizard/scaffolding tool was always a local matter"* ([#488](https://github.com/getsentry/sentry-wizard/issues/488)) — an assumption that held about two and a half years before Sentry's VP of Engineering documented the wizard as unusable by agents and the team replaced it with `sentry init` *(evidenced)*. The single most-cited product blog post, ["Install Sentry with a Single Command"](https://blog.sentry.io/install-sentry-with-a-single-command/) (2023-01-30), argues convenience — *"now you can skip all the clicks"* — and contains **no activation data, no time-to-first-event claim, and no defence of interactivity** *(evidenced)*. **No RFC on wizard/onboarding/activation exists** in [getsentry/rfcs](https://github.com/getsentry/rfcs/tree/main/text) *(evidenced: all 71 entries enumerated)*. Note the irony available here: Sentry has 100%-sampled telemetry tagging **every individual prompt decision** in the wizard, so it can see the drop-off at each question — and what it built with that visibility was a replacement CLI with `--yes`. Reported strictly as evidence about Sentry; see §5 for why it is not a recommendation.

**Searched for: evidence that prompts break integrators.** Found it twice, in two different ecosystems, with near-identical shape:

| | re_gent #45 | Storybook #22623 |
|---|---|---|
| Date | 2026-05-19 | 2023-05-18 |
| Reporter | third-party integrator (biomelab) | third-party integrator (svelte-scaffold) |
| Problem | *"rgt's interactive installer needs a TTY biomelab can't provide"* | init hangs on a prompt despite `--yes` |
| Workaround | **copied re_gent's hook code into his own project** | blocked |
| Resolution | **still open, 15 months** | fixed in 4 days ([PR #22651](https://github.com/storybookjs/storybook/pull/22651), merged 2023-05-20) |

*(evidenced)*. Note the Storybook maintainer's own note on that PR: their **own** sandbox tooling was hanging on the hidden prompt — *"Cryptic!"* The prompt broke the maintainers' automation, not just users' *(evidenced)*.

**Searched for: the "declined the prompt, tool claims success" bug elsewhere.** Found it, already diagnosed and fixed by Storybook in 2023. [PR #23201](https://github.com/storybookjs/storybook/pull/23201) (merged 2023-07-11): *"if the user selects 'no', the process will continue and will get into a failure"* — fixed by exiting cleanly with a docs link *(evidenced)*. **re_gent has this exact bug today**, and worse: because `huh.NewOption` returns an option with `selected` false and `init.go` never calls `.Selected(true)`, **the default answer to the only question that matters is "install nothing"** — a user who presses Enter gets a success banner and zero hooks *(evidenced: verified against `huh@v1.0.0/option.go` and by grep of `init.go`)*.

**The strongest single piece of evidence is internal.** re_gent's own newer code already implements the inverted design, with the rationale written in the comments:

- `discover.go:104-113` — an `interactive()` isatty helper whose comment explains that prompting under `curl ... | sh` *"would swallow the rest of the script or block forever"*, and notes it is *"a real isatty check, not a character-device test"* because `/dev/null` is a character device too.
- `discover.go:129-131` — *"canPrompt is passed in rather than read from os.Stdin here so the picker is testable without a pty."*
- `connect.go:139-157` — `connectWireHooks` installs Claude hooks with **no prompt at all**, prints what it did, and prints the restart warning.

*(evidenced)*. The project has already fought this battle and won it — in `connect`, `discover`, and `setup`. `init.go` is simply the oldest code and never got the fix *(inferred)*.

**Confirmation-risk check.** The brief warned that re_gent's own artifacts already lean this way and that I must not just agree with them. Testing them:

- **`regent-viewer/docs/TEAM-ONBOARDING.md` ("zero-step or one paste") is weak evidence and I am discounting it.** It is about the *team-server* onboarding path, which the brief puts out of scope, and its "zero steps" claim rests on devcontainers, which solve a different problem (provisioning a machine) than `rgt init` (wiring a host config) *(evidenced — the document's own framing)*. It should not carry weight here.
- **Issue #45 is much *stronger* than the brief credited.** The brief describes it as a feature request. It is a defect report with a fork attached: the reporter did not ask and wait, he copied the hook-creation code out of re_gent under Apache-2.0 and shipped it himself *(evidenced)*. That is revealed preference in the strictest sense.

So one of the two internal artifacts got weaker under scrutiny and one got stronger. The verdict does not rest on either — it rests on the external record plus the reproduced behaviour of the binary.

### 3.2 Secondary falsifier: "the wizard optimizes a non-bottleneck"

**It also fired, and it is the more actionable of the two.**

The claim was that real activation loss happens *after* setup — hooks silently never fire, output is empty, user concludes the tool is broken. I reproduced that entire chain against the committed `rgt` binary in a clean temp directory *(evidenced)*:

```
$ rgt init < /dev/null
  ⚠ Could not configure hooks: agent selection: huh: could not open a new TTY
  ⚠ Could not install skills: read skill confirmation: EOF
  ✓ Initialization complete
Next steps:
  - Run: rgt log
  - Agent skills: log, blame, show      <- none were installed
$ echo $?
0
$ ls .claude
ls: .claude: No such file or directory

$ rgt status   -> "No sessions recorded yet."   exit 0
$ rgt log      -> "No sessions found."          exit 0
$ rgt sessions -> "No sessions recorded yet."   exit 0
```

Four commands, four shrugs, no mention of hooks. And `rgt init --agent claude < /dev/null` behaves identically — **there is no flag combination that installs hooks without a TTY** *(evidenced: tested)*.

Even with a TTY, pressing Enter at the multi-select produces the same end state, because nothing is pre-selected *(evidenced)*.

**Therefore the wizard is not merely optimizing a non-bottleneck — it is the direct cause of the post-setup failure it fails to detect** *(inferred)*. The two falsifiers are the same bug seen from two ends.

### 3.3 What would have flipped this verdict

Stated so this document is not a sales pitch. I would have recommended shipping the wizard as-is if I had found any of:

- A comparable that added interactivity to a previously non-interactive installer, with maintainer reasoning. **Not found.**
- A maintainer arguing that prompts measurably improved activation. **Not found** — including at Sentry, which has the telemetry to make such an argument and did not make it.
- Evidence that the hook-installing subfamily (pre-commit / husky / lefthook) uses wizards. **The opposite: none of the three has any interactive setup at all** *(evidenced)*.
- A non-TTY path already working in `rgt init`. **Tested; does not exist.**

**What genuinely complicates the verdict — stated so this is not one-sided.** Two things.

First, **husky's v5 removal cuts against the simple reading.** husky removed *automatic* installation in v5.0.0 (2020-11-16) and made users run an explicit command. Typicode's stated reason was not minimalism but **observability**: package managers cache installs so it silently didn't run, and they hide `postinstall` output, so *"There's no more confirmation that hooks have been installed or info messages to help users debug problems"* — adding that this matters *"especially for a tool with a big side effect (changing Git hooks)"* ([blog.typicode.com, 2021-04-02](https://blog.typicode.com/posts/husky-git-hooks-autoinstall/)) *(evidenced)*. The lesson is **not** "less automation is better"; it is that a setup step the user cannot see is worse than one they run deliberately. Non-interactive-and-loud satisfies that; non-interactive-and-silent does not. This is why §1.1 puts so much weight on printing what was written *(inferred)*.

Second, **lefthook took the opposite bet and it seems to work** — it kept npm `postinstall` auto-install and added checksum-based self-healing (Pattern D) *(evidenced)*. So the field is not unanimous about *automatic* installation. It is unanimous about *interactive* installation: none of the three hook managers has a wizard, and the one canonical wizard is being retired.

---

## 4. "Prove capture is working"

Under a permanent no-telemetry promise (`docs/FAQ.md`: *"No. Everything stays local. No telemetry, no cloud."*), a local self-check is one of the few feedback channels that exists at all. Here is what the field built, and what re_gent should.

### 4.1 What comparables built

| Project | Mechanism | Evidence |
|---|---|---|
| **Storybook** | `storybook doctor` — checks version detection, main-config load, missing `storybook` dep, incompatible packages, mismatched versions, duplicated deps; enables log-file writing when issues are found | [doctor/index.ts](https://github.com/storybookjs/storybook/blob/next/code/lib/cli-storybook/src/doctor/index.ts) *(evidenced)* |
| **Storybook** | Writes real example stories (`Button`, `Header`, `Page` + `Configure.mdx`) into `src/stories/`, plus `.storybook/` config and a `storybook` npm script — so there is something to look at immediately | [helpers.ts `copyTemplateFiles`](https://github.com/storybookjs/storybook/blob/next/code/core/src/cli/helpers.ts) *(evidenced)* |
| **Storybook** | **Auto-launching the dev server was added in 2023 and removed in 2026** — [PR #34526](https://github.com/storybookjs/storybook/pull/34526) *"Made `--no-dev` the new default: no more dev server"* (merged 2026-04-13, shipped 10.4.0 / 2026-05-14). Init now prints `To run Storybook, run <command>` instead | *(evidenced — this resolves the docs/source conflict flagged in my draft: the source is current, the published CLI docs are stale)* |
| **Storybook** | On init failure, writes `debug-storybook.log` and prints its path (`--logfile` / `--loglevel` / `--debug`) | [initiate.ts](https://github.com/storybookjs/storybook/blob/next/code/lib/create-storybook/src/initiate.ts) *(evidenced)* |
| **Sentry** | Generates a per-framework example page/route that throws a real test error, so a real event appears | [src/nuxt/sdk-example.ts](https://github.com/getsentry/sentry-wizard/blob/master/src/nuxt/sdk-example.ts), [src/nextjs/templates.ts](https://github.com/getsentry/sentry-wizard/blob/master/src/nextjs/templates.ts) *(evidenced)* |
| **Sentry** | `supportsExamplePage()` **refuses to create the example** when it cannot guarantee the user can reach it — *"We currently only support creating an example page if users can reliably access it without having to add code changes themselves"* | same file *(evidenced)* |
| **Sentry** | The generated page calls `Sentry.diagnoseSdkConnectivity()` on mount and **disables the test button** with an ad-blocker warning if Sentry is unreachable — it distinguishes "not wired" from "wired but blocked" before the user clicks | [src/nextjs/templates.ts](https://github.com/getsentry/sentry-wizard/blob/master/src/nextjs/templates.ts); added 2025-07 via [#1010](https://github.com/getsentry/sentry-wizard/pull/1010) *(evidenced)* |
| **Sentry (`sentry init`)** | The successor CLI **runs your app for you** and waits on two signals — dev-server stdout without fatal errors, and a real SDK envelope received by an embedded Spotlight sidecar (15s timeout). No example page, no human clicking anything | [verify-setup.ts](https://github.com/getsentry/cli/blob/main/packages/cli/src/lib/init/verify-setup.ts) *(evidenced)* |
| **pre-commit** | Refuses to install at all when `core.hooksPath` is set: `Cowardly refusing to install hooks with core.hooksPath set` + `exit 1`. Filed by the maintainer himself as issue #663 (2017-11-27) after diagnosing that pre-commit would be *"silently skipped"*; shipped v1.7.0 (2018-03-04) | [install_uninstall.py](https://github.com/pre-commit/pre-commit/blob/main/pre_commit/commands/install_uninstall.py), [#663](https://github.com/pre-commit/pre-commit/issues/663) *(evidenced)* |
| **pre-commit** | The installed hook script exits 1 — not 0 — when the binary is missing: `` `pre-commit` not found.  Did you forget to activate your virtualenv? `` | [resources/hook-tmpl](https://github.com/pre-commit/pre-commit/blob/main/pre_commit/resources/hook-tmpl) *(evidenced)* |
| **lefthook** | `lefthook check-install` — explicit gate, exit 0 installed / 1 stale-or-missing; plus checksum auto-heal (Pattern D) | [cmd/check_install.go](https://github.com/evilmartians/lefthook/blob/master/cmd/check_install.go) *(evidenced)* |
| **husky** | **Nothing.** No doctor, no verify command. A missing hook file exits 0 silently. Its "proof" is that `husky init` writes a *working* `pre-commit` that runs your tests, so it fires on the very next commit | [husky shell script](https://github.com/typicode/husky/blob/main/husky), [bin.js](https://github.com/typicode/husky/blob/main/bin.js) *(evidenced)* |
| **Claude Code** | `claude mcp add` *"confirms a successful add by printing an `Added ...` line, which means the configuration was written"*; `claude mcp list` then shows live health per server — `✔ Connected`, `! Needs authentication`, `✘ Failed to connect` | [code.claude.com/docs/en/mcp](https://code.claude.com/docs/en/mcp) *(evidenced)* |

**Two things nobody in the field has built, and re_gent should** *(evidenced that the primitives exist and are unused; the recommendation is asserted)*:

Claude Code ships two hook primitives designed for exactly the "is it firing?" problem — a `statusMessage` field that shows a live spinner with custom text while a hook runs, and exit-0 JSON `{"systemMessage": "…"}` which prints a visible, non-blocking line into the session. **Neither appears in any third-party tool checked.** Nearly the whole ecosystem stops at reading `settings.json` back and calls that verification, which proves *configuration*, not *execution* — and one of the two shipped `--health-check` implementations is outright broken against the real schema (it iterates `settings.hooks` as a flat array when the real shape is an object keyed by event name).

The docs are explicit that this gap is by design: *"When the hook succeeds, Claude Code shows nothing in the conversation"*, and successful hook stdout *"goes to the debug log only."* So a re_gent hook that emits a `systemMessage` on its **first successful capture in a session** would make capture self-evidencing at the exact moment the user is wondering whether it worked — with no telemetry, no network, and nothing sent anywhere. It is a one-line change to the hook's stdout.

**The cross-cutting lesson** *(inferred)*: the field separates two jobs that `rgt init` currently conflates. **Install** prints what it wrote and exits non-zero if it could not. **Verify** is a separate command you can run any time, including long after install, including to paste into a bug report. Claude Code's `add` / `list` split is the cleanest example, and it is the host re_gent already targets.

### 4.2 What re_gent should build

**`rgt doctor` — local-only, sends nothing anywhere.** Most of it already exists.

1. **Are hooks wired?** `printExistingHooks` (`init.go:609`) already reads all four host configs and detects re_gent's own commands via `capture.IsRegentCommand`. **Extract it from `init`'s reinit path and make it the core of `doctor`** *(evidenced: the function exists and works)*.
2. **Is `rgt` reachable from where the agent will call it?** Hooks are written as the bare string `rgt message-hook user` (`init.go:36-39`), so they depend entirely on PATH resolution inside the agent host's environment — a GUI-launched agent may not have `~/go/bin` on PATH. Check `exec.LookPath("rgt")` and report the resolved path *(evidenced that the dependency exists; the failure mode is inferred)*.
3. **Has anything ever been captured?** Report last-captured timestamp per session from `idx.ListAllSessions()`. "Hooks wired, nothing captured yet, restart your agent" is a completely different message from "hooks not wired" — and today both render as `No sessions recorded yet.` *(evidenced)*.
4. **Did hooks run and fail?** `.regent/log/hook-error.log` already exists and is written by `capture.LogHookError` — errors are deliberately swallowed so as not to break the agent. `doctor` should surface the tail of it. Today nothing points the user at this file except a consistency-check failure in `status` *(evidenced)*.
5. **Print the restart warning**, as `connect.go:148` does.
6. **Exit non-zero when hooks are not wired**, so it composes in scripts — following `lefthook check-install` and `pre-commit`'s refusal *(asserted)*.

**Also fix `rgt status` / `rgt log`.** When zero sessions exist, they should say whether hooks are wired, not just `No sessions found.` The empty state is the single highest-traffic diagnostic moment the tool has *(asserted)*.

**Explicitly excluded by the hard constraint:** no install pings, no anonymised counters, no opt-in reporting, no first-run beacon. Nothing in this section sends anything anywhere; `rgt doctor` is output for the user's terminal and for pasting into an issue by hand.

---

## 5. Confidence and limitations

### 5.1 What this research cannot tell you

- **It cannot prove impact.** No comparable publishes activation numbers tied to an onboarding change. Nothing here demonstrates that a wizard does or does not move activation. What it shows is what the field *does*, what maintainers *say*, and — in two cases — what integrators did when blocked. That is revealed preference, not measurement.
- **Consensus is not proof.** Five projects choosing non-interactive installs is evidence about norms and about what breaks in practice; it is not an experiment. I have tried to label it as such throughout rather than let it read as data.
- **Activation is unmeasurable for re_gent and always will be**, absent a policy change that this memo does not recommend. Borrowed evidence is not a stopgap here — it is the only evidence there will be.
- **Comprehension is out of scope.** This covers getting hooks wired and data flowing. It does not cover whether `rgt log` is legible once it fills up. An empty log and an incomprehensible log fail identically from the user's side. Named gap, not an oversight.
- **No first-hand installs of comparables.** I read their source and docs; I did not run Storybook's or Sentry's installers.

### 5.2 Where evidence is thin or missing

- **direnv, OpenTelemetry, `npm init playwright`, `shadcn init`, Langfuse/Continue: not researched.** Budget went to the anchors and the hook cluster. Pattern C rests on a single unverified example and should be treated as unsupported.
- **Sentry's RFC record: confirmed absent, not merely unfound.** All 71 entries in [getsentry/rfcs/text](https://github.com/getsentry/rfcs/tree/main/text) were enumerated; there is no RFC on wizard, onboarding, activation, or agentic setup. So Sentry's interactive-by-default choice appears never to have been written up as a design decision at all *(evidenced)*.
- **Sentry's in-product "waiting for first event" screen: not documented.** [docs.sentry.io/product/onboarding/](https://docs.sentry.io/product/onboarding/) contains no such language. Treat as "not documented", not "does not exist".
- **`husky init`'s and `pre-commit install`'s exact terminal output: not verified.** I read the source; I did not run them.
- **Published activation metrics: confirmed absent, at two companies that measure it.** Storybook instruments per-prompt abandonment (a `canceled` telemetry event naming which prompt was abandoned, wired into every prompt call site) and Sentry tags every individual wizard decision at 100% sampling. **Neither has published a single onboarding metric** — no abandonment rate, no time-to-first-event, no time-to-first-story. Every "setup is easier now" claim in their launch posts is qualitative *(evidenced)*. This is the strongest available justification for the brief's stance that no verdict here may be gated on numbers: the numbers exist and are not public.
- **Claude Code third-party sample is a floor, not a census.** GitHub code search rate-limited during collection, and the candidate list skewed toward statusline/usage tools. The 4-of-10 figure in §2.5 should be read as directional.
- **Claude Code hook events are a moving target.** The shipped 2.1.228 binary carries 31 hook events, roughly one added every few minor versions; `PostToolBatch` is confirmed real and documented, but its introduction version is **not found** in the public changelog. Any hardcoded event list will drift *(evidenced)*.
- **re_gent issue #45's single comment: not read in full.** I read the issue body and metadata.
- **One draft error, corrected.** My first pass asserted that Sentry provides "a flag and an env var for every prompt," reading the README's options table at face value. Checking the source showed `--quiet` and `--skip-connect` are dead code for every modern integration. The README overstates the wizard's non-interactive support, and I repeated the overstatement before verifying. Flagged because it is exactly the kind of error a documentation-only reading produces.

### 5.3 Claims I am most and least confident in

**Most confident** *(all evidenced, most reproduced locally)*: the wizard shipped in v1.0.0; no flag combination installs hooks without a TTY; `rgt init` exits 0 claiming success having installed nothing; the multi-select defaults to nothing selected; `connect.go` already does the non-interactive thing; none of pre-commit/husky/lefthook has an interactive setup.

**Least confident** *(inferred or asserted)*: that inverting will improve activation — this is the one claim the whole exercise cannot establish, and I want it stated plainly rather than buried. What the evidence supports is narrower and still sufficient: the current design has a reproducible failure mode that silently produces zero capture, the field has converged on avoiding exactly that, and the fix is small and uses code the project has already written. The case for inverting rests on **removing a known defect**, not on a predicted activation lift *(asserted)*.

---

## 6. Teardown appendix

### 6.1 re_gent — first-hand, reproduced

| Fact | Source |
|---|---|
| Interactive multi-select + `Install skills? [Y/n]` present since v1.0.0 | `git show e489d5c:internal/cli/init.go`; `CHANGELOG.md` 1.0.0: *"OpenCode integration with interactive agent selection during `rgt init`"* |
| `huh` added 2026-05-14 | commit `7f30a80` *"feat: add OpenCode integration with interactive agent selection"* |
| Live in v1.1.0 (2026-06-01) | commit `226de39`; `gh release list --repo bonez-io/re_gent` |
| Options never pre-selected | `init.go:185-206` has no `.Selected(`; `huh@v1.0.0/option.go:25-27` returns `Option{Key, Value}` |
| No non-interactive hook path | `offerHookInstall` (line 181) is the sole caller of `installClaudeHook` in `init.go`; flags are only `--skip-hook`, `--skip-skills`, `--agent` |
| Non-TTY: exit 0, no hooks, success banner | reproduced in temp dir with the committed `rgt` binary |
| `--agent claude` also fails without TTY | reproduced |
| Summary uses detected, not installed, targets | `init.go:134` — `printSummary(cwd, targets)` |
| `status`/`log`/`sessions` silent about hooks | reproduced; `status.go:36-39` |
| `connect` wires hooks with no prompt | `connect.go:139-157` |
| Restart warning exists only in `connect` | `connect.go:148-152` |
| TTY detection already in repo | `discover.go:104-113`, `setup.go:104-118` |
| Hook-writing funcs tested; interactive layer untested | `init_test.go` — 7 tests, all on `installClaudeHook`/`installCodexHook`/`installSkills`; none on `offerHookInstall` |
| Issue #45 | [bonez-io/re_gent#45](https://github.com/bonez-io/re_gent/issues/45), opened 2026-05-19, still open |
| No telemetry | `docs/FAQ.md` |

### 6.2 Storybook

- Install + auto-detection: [storybook.js.org/docs/get-started/install](https://storybook.js.org/docs/get-started/install)
- Flags (`-y/--yes`, `--type`, `--builder`, `--package-manager`, `--skip-install`, `--features`, `--no-dev`, `--disable-telemetry`): [storybook.js.org/docs/api/cli-options](https://storybook.js.org/docs/api/cli-options)
- `--yes` didn't suppress an ESLint prompt, blocking a third-party scaffolder: [#22623](https://github.com/storybookjs/storybook/issues/22623) (2023-05-18 → closed 2023-05-22)
- Fix: [PR #22651](https://github.com/storybookjs/storybook/pull/22651) *"CLI: Skip prompting for eslint plugin with --yes flag"* (merged 2023-05-20)
- Declined-prompt-then-fail bug: [PR #23201](https://github.com/storybookjs/storybook/pull/23201) (merged 2023-07-11)
- The later, harder removal — ESLint prompt deleted outright: [PR #25289](https://github.com/storybookjs/storybook/pull/25289) (8.0.0)
- **TTY/CI/agent prompt suppression**: `UserPreferencesCommand.ts:62-63` — `!process.stdout.isTTY || isCI() || --yes || --agent`
- Project-type prompt shipped ~7 years then deleted: [PR #32717](https://github.com/storybookjs/storybook/pull/32717) (merged 2025-11-19, shipped 10.1.0 / 2025-11-26); pushback [#35045](https://github.com/storybookjs/storybook/issues/35045) (open); reinstatement PRs not landed
- Auto-dev-server added 2023 ([#22871](https://github.com/storybookjs/storybook/issues/22871)), removed 2026 ([PR #34526](https://github.com/storybookjs/storybook/pull/34526), 10.4.0 / 2026-05-14)
- Features multi-select added 8.6.0 (2025-02-25, [#30202](https://github.com/storybookjs/storybook/pull/30202)), collapsed to two binary questions by 9.0.0 (2025-05-28)
- `--extensive` merged and reverted 80 minutes later: [#34730](https://github.com/storybookjs/storybook/pull/34730) → [#34740](https://github.com/storybookjs/storybook/pull/34740) (2026-05-07)
- Example stories written by `copyTemplateFiles`: [code/core/src/cli/helpers.ts](https://github.com/storybookjs/storybook/blob/next/code/core/src/cli/helpers.ts)
- `storybook doctor` — [PR #22236](https://github.com/storybookjs/storybook/pull/22236) by yannbf, shipped 7.6.0 (2023-11-28); [doctor/index.ts](https://github.com/storybookjs/storybook/blob/next/code/lib/cli-storybook/src/doctor/index.ts). **Not run after `init`** — only as part of `upgrade`
- Zero-config thesis: ["Zero-config Storybook"](https://storybook.js.org/blog/zero-config-storybook/), Shilman, 2020-07-31
- Per-prompt abandonment telemetry, no published metrics: `prompt-cancel.ts`, `code/core/src/telemetry/types.ts`

### 6.3 Sentry

- Full flag/env table (**overstates non-interactive support** — see §2.1): [getsentry/sentry-wizard README](https://github.com/getsentry/sentry-wizard)
- `--quiet`/`--skip-connect` legacy-only: `src/run.ts`, `lib/Helper/Wizard.ts`, `lib/Steps/PromptForParameters.ts`
- `--non-interactive` wired to Apple Snapshots only: [PR #1297](https://github.com/getsentry/sentry-wizard/pull/1297) (2026-07-01, v6.13.0)
- Example-page generation and the `supportsExamplePage` bail-out: [src/nuxt/sdk-example.ts](https://github.com/getsentry/sentry-wizard/blob/master/src/nuxt/sdk-example.ts); per-framework siblings under `src/{sveltekit,remix,react-router,angular}/`; `diagnoseSdkConnectivity` in [src/nextjs/templates.ts](https://github.com/getsentry/sentry-wizard/blob/master/src/nextjs/templates.ts)
- "Wizard is user-run-only": [sentry-for-ai PR #18](https://github.com/getsentry/sentry-for-ai/pull/18) (merged 2026-03-02), live text in [src/references/sdks/nextjs/index.md](https://github.com/getsentry/sentry-for-ai/blob/main/src/references/sdks/nextjs/index.md)
- Wizard superseded: [#491](https://github.com/getsentry/sentry-wizard/issues/491), [#1183](https://github.com/getsentry/sentry-wizard/pull/1183), [#1187](https://github.com/getsentry/sentry-wizard/pull/1187), [#997](https://github.com/getsentry/sentry-wizard/pull/997) (closed 2026-07-24 / 2026-08-03)
- Successor CLI with `--yes`, `--dry-run`, TTY guard, agent detection: [getsentry/cli](https://github.com/getsentry/cli); verification via Spotlight sidecar in [verify-setup.ts](https://github.com/getsentry/cli/blob/main/packages/cli/src/lib/init/verify-setup.ts)
- "Wizards are a local matter": [#488](https://github.com/getsentry/sentry-wizard/issues/488) (2023-11-03)
- No RFC on wizard/onboarding/activation: [getsentry/rfcs/text](https://github.com/getsentry/rfcs/tree/main/text) (all 71 entries enumerated)

### 6.3b Claude Code host ecosystem

- `claude mcp add` flags, `add-json`, `--scope`, `list` health states, `Added ...` confirmation: [code.claude.com/docs/en/mcp](https://code.claude.com/docs/en/mcp)
- `/hooks` is read-only; hook creation is "edit your settings JSON directly": [hooks-guide](https://code.claude.com/docs/en/hooks-guide), [hooks](https://code.claude.com/docs/en/hooks)
- Non-interactive plugin management, and `--config` sharing the interactive flow's storage path: [plugins-reference](https://code.claude.com/docs/en/plugins-reference), [discover-plugins](https://code.claude.com/docs/en/discover-plugins)
- Settings precedence and **array merge ("concatenated and deduplicated, not replaced")**; `settings.local.json` gitignore caveat: [settings](https://code.claude.com/docs/en/settings)
- Silent success by design — *"When the hook succeeds, Claude Code shows nothing in the conversation"*; `statusMessage` and exit-0 `systemMessage`; `--debug` / `--debug-file`: [hooks](https://code.claude.com/docs/en/hooks), [hooks-guide](https://code.claude.com/docs/en/hooks-guide)
- `--bare` *"will become the default for `-p` in a future release"*: [headless](https://code.claude.com/docs/en/headless)
- Plugin-hints protocol, *"Claude Code never installs a plugin automatically"*: [plugin-hints](https://code.claude.com/docs/en/plugin-hints)
- **Watcher create-vs-modify rule** (§1.1 step 5): sourced from Anthropic's bundled `update-config` skill inside the shipped 2.1.228 binary — *"it only watches directories that had a settings file when this session started."* **Not stated in public docs**, which say only that changes are "normally" picked up. Lower-confidence than a docs citation; verify before relying on it.

### 6.3c Tier 2

- **shadcn** 10 prompts → 1 conditional: [get-project-info.ts](https://github.com/shadcn-ui/ui/blob/main/packages/shadcn/src/utils/get-project-info.ts), [init.ts](https://github.com/shadcn-ui/ui/blob/main/packages/shadcn/src/commands/init.ts); `-y` default flipped false→true in 2.0.0 (2024-08-30); base-color prompt removed in 4.0.0 (2026-03-06); 4-stage named preflight in [preflight-init.ts](https://github.com/shadcn-ui/ui/blob/main/packages/shadcn/src/preflights/preflight-init.ts)
- **direnv** prints, never writes: [cmd_hook.go](https://github.com/direnv/direnv/blob/master/internal/cmd/cmd_hook.go), [docs/hook.md](https://github.com/direnv/direnv/blob/master/docs/hook.md); `direnv init` wizard requested [#1202](https://github.com/direnv/direnv/issues/1202) (2023-11-29, open); **shell auto-detection via `$0` withdrawn** in 2.1.0 (2013-11-10, "Fixes #64")
- **OpenTelemetry** `bootstrap` defaults to printing requirements: [bootstrap.py](https://github.com/open-telemetry/opentelemetry-python-contrib/blob/main/opentelemetry-instrumentation/src/opentelemetry/instrumentation/bootstrap.py); "zero-code" IA rename [#4427](https://github.com/open-telemetry/opentelemetry.io/issues/4427) (2024-05-06); `otelcol validate`
- **create-playwright** — genuine counter-evidence: prompt loss treated as a **regression and restored**, [PR #167](https://github.com/microsoft/create-playwright/pull/167) (2025-10-08); repo is `microsoft/create-playwright`, not the Playwright monorepo
- **Langfuse** has no wizard (verified negative across npm and the org); prove-it is an eyeball check backed by a troubleshooting page enumerating eight silent-failure modes
- **Directly on re_gent's shape:** [langfuse/claude-observability-plugin#30](https://github.com/langfuse/claude-observability-plugin/issues/30) (2026-07-17 → 2026-08-10) — wizard-collected config stored at machine scope silently overrode per-project env vars, causing cross-project trace leakage; fixed by **demoting the wizard value below the env var**. A wizard writes to whatever scope the host offers; if that is coarser than the user's mental model, data is silently misrouted.

### 6.4 Hook cluster

- pre-commit no-prompt install, legacy-hook preservation, `core.hooksPath` refusal: [install_uninstall.py](https://github.com/pre-commit/pre-commit/blob/main/pre_commit/commands/install_uninstall.py); origin [#663](https://github.com/pre-commit/pre-commit/issues/663) (2017-11-27) → [v1.7.0](https://github.com/pre-commit/pre-commit/releases/tag/v1.7.0) (2018-03-04)
- pre-commit hook script exits 1 on missing binary: [resources/hook-tmpl](https://github.com/pre-commit/pre-commit/blob/main/pre_commit/resources/hook-tmpl)
- pre-commit never had a wizard: CHANGELOG contains no "interactive"/"prompt"
- husky full source: [index.js](https://github.com/typicode/husky/blob/main/index.js), [bin.js](https://github.com/typicode/husky/blob/main/bin.js); `add`/`set`/`uninstall` exit 1, `install` deprecated
- husky removed auto-install in [v5.0.0](https://github.com/typicode/husky/releases/tag/v5.0.0) (2020-11-16); reasoning [blog.typicode.com, 2021-04-02](https://blog.typicode.com/posts/husky-git-hooks-autoinstall/) — *"There's no more confirmation that hooks have been installed"*
- husky v9 (2024-01-25) shrank to ~3 kB: [v9.0.1 release](https://github.com/typicode/husky/releases/tag/v9.0.1)
- husky tried auto-removing deprecated boilerplate in [v9.1.0](https://github.com/typicode/husky/releases/tag/v9.1.0) (2024-07-17) and reverted in [v9.1.2](https://github.com/typicode/husky/releases/tag/v9.1.2) (2024-07-25)
- lefthook install/checksum/auto-heal: [install.go](https://github.com/evilmartians/lefthook/blob/master/internal/command/install.go), [run.go](https://github.com/evilmartians/lefthook/blob/master/internal/command/run.go); [check_install.go](https://github.com/evilmartians/lefthook/blob/master/cmd/check_install.go)

### 6.5 ESLint

- Wizard moved out of core: [create-config CHANGELOG](https://github.com/eslint/create-config/blob/main/CHANGELOG.md) `feat: move eslint --init (#1)`; ESLint commit [0d2b9a6](https://github.com/eslint/eslint/commit/0d2b9a6dfa544f7ab084425eafc90a90aa14bcae) (PR #15150)
- Prompts removed: `feat: remove style guides (#108)`, `feat: Remove Google style guide (#82)`
- Non-interactive escape added: `feat: support --config (#38)`
- Current usage: [eslint/create-config README](https://github.com/eslint/create-config)

### 6.6 Claude Code host

- `claude mcp add` flag-driven syntax, `add-json`, `--scope`, `list`/`get` health states, and the `Added ...` confirmation line: [code.claude.com/docs/en/mcp](https://code.claude.com/docs/en/mcp)

---

## 7. Second research pass — closing the named gaps

This section was added by a later pass working from the same brief. It does not revise the
verdict in §1; it supplies primary sources for four things §5.2 listed as missing, plus one
comparable that the first pass did not reach and that turns out to be the closest analogue in
the entire field. Where it disagrees with §1–§6 it says so explicitly.

**Provenance note.** §7 was written by a different pass than §1–§6, so I spot-verified its
load-bearing claims rather than accept them: `internal/command/install_ai.go` exists (9,496 bytes);
[issue #1433](https://github.com/evilmartians/lefthook/issues/1433) "Add LLM hooks management
support" was opened 2026-05-29 and is closed; [PR #1448](https://github.com/evilmartians/lefthook/pull/1448)
"feat: AI coding agents integration" merged **2026-07-08**; `docs/configuration/ai.md` exists and
contains the quoted `os.Executable()` / PATH sentence verbatim; and `resolveLefthookBin` is at
`install_ai.go:43`. Those check out. The remaining §7 subsections are **not** independently
verified by me and carry their own labels — treat them at the confidence they claim.

### 7.1 The closest analogue: lefthook wired itself into Claude Code and Codex, and refused the wizard in writing

This is the finding that most deserves to be at the top of the memo. In July 2026 — six weeks
ago — lefthook, a mature Git-hooks manager, implemented **exactly re_gent's feature**: installing
hooks into `.claude/settings.json`, `.codex/hooks.json`, `.cursor/hooks.json`, and
`.github/hooks/lefthook.json`. The design discussion is public and names both options.

**lefthook issue [#1433](https://github.com/evilmartians/lefthook/issues/1433), "Add LLM hooks management support"** *(evidenced)*. Verbatim, the two options considered:

- *Option 1 – Strictly configurable* — a declarative `ai:` key in `lefthook.yml` mapping provider
  event names to lefthook hook names; `lefthook install` generates the provider file.
- *Option 2 – Ask user* — *"When you run `lefthook install`: 1. Detects you have configured hooks
  for LLM (by name) 2. Asks if you prefer to use Claude or Codex 2.1. Another option is to check
  existing `.claude/` and `.codex/` dirs 4. Installs the hook."*

**Option 2 is `offerHookInstall` in `init.go`**, arrived at independently by a different team for
a different tool. They did not ship it.

**What shipped is Option 1** — [PR #1448](https://github.com/evilmartians/lefthook/pull/1448),
merged 2026-07-08, released in v2.1.10 (`CHANGELOG.md`: *"feat: AI coding agents integration"*)
*(evidenced)*. The author's stated reasoning, verbatim from the PR body: *"we're following the
first path ('Strictly configurable'). I think that since provider-specific event sets are not
equal, it'd be better to delegate configuration to user. I also don't think it's worth letting
`lefthook` know [about] specific events."*

Note precisely what that reasoning is and is not *(inferred)*. It is **not** an argument that
prompts are bad. It is an argument that the tool should not hard-code knowledge of each host's
event names — and the consequence is that there is nothing left to ask about, because the user
already declared it in config. Interactivity did not lose a debate; it was designed out of
existence. That is a different and stronger outcome than "we decided not to prompt."

**Mechanics worth copying, from `internal/command/install_ai.go`** *(evidenced — full file read)*:

| lefthook behaviour | re_gent equivalent today |
|---|---|
| No new command. AI hook generation is folded into `lefthook install`, gated on `cfg.AI != nil` (`install.go`, after `installHooks`). | `rgt init` — same shape, but gated on a prompt. |
| `validateAIHooks` runs **before** writing anything and hard-fails on a dangling reference. Its code comment: *"This catches typos early instead of silently writing a `lefthook run <typo>` command that fails when the agent event fires."* | No pre-write validation. |
| `stripLefthookEntries` / `stripFlatLefthookEntries` remove lefthook-owned entries, then re-append — user-authored entries survive; `lefthook uninstall` strips cleanly. | `filterRegentHookCommands` + `mergeHookCommand` (`init.go:510-513`) do the same thing. **Genuine convergent design; re_gent's merge logic is already correct** *(evidenced)*. |
| **Writes the absolute path of the binary that ran install** (`resolveLefthookBin` → `os.Executable()`), documented as *"so AI tools do not depend on `lefthook` being on `PATH`."* ([docs/configuration/ai.md](https://github.com/evilmartians/lefthook/blob/master/docs/configuration/ai.md)) | `init.go:36-39` writes the bare string `rgt message-hook user`. **This is a real, evidenced defect**, and it independently confirms the PATH concern §4.2 raised as *inferred*. It is now *evidenced*: another project hit it and fixed it deliberately. |

**Also relevant:** lefthook's `go.mod` contains **no prompt library at all** — `lipgloss` for
colour, `spinner`, `progressbar`, `go-tty`, and nothing else in that family *(evidenced)*. There is
no `lefthook init`; `lefthook install` is the whole story.

**Action item this adds to §1.1:** step 3 of the spec should write `os.Executable()`'s cleaned
absolute path into the hook command, not `rgt`, with the config-override escape lefthook provides
*(asserted, on evidenced precedent)*.

### 7.2 Storybook: init now detects that an AI agent is driving it, and turns interactivity **off**

§2 credited Storybook with `--yes`. The current code goes considerably further, and this is the
single most on-the-nose finding for a tool whose users are, by construction, sitting inside an AI
agent *(evidenced — [`code/lib/create-storybook/src/bin/run.ts`](https://github.com/storybookjs/storybook/blob/next/code/lib/create-storybook/src/bin/run.ts), branch `next`)*:

```js
import { isAgent, detectAgent } from 'std-env';
...
.option('--agent',    'Force agent mode (non-interactive, logs AI setup instructions)')
.option('--no-agent', 'Force disable agent mode even when an AI agent is detected')
...
const resolvedAgent = options.agent ?? isAgent;   // <- default is auto-detect
if (resolvedAgent) {
  logger.log(`This command is running via an AI agent: ${agentName}. Proceeding with agentic installation flow.`);
}
```

and in [`initiate.ts`](https://github.com/storybookjs/storybook/blob/next/code/lib/create-storybook/src/initiate.ts), the first two lines of `doInitiate`:

```js
if (options.agent) {
  options.yes = true;
}
```

Storybook treats "an AI agent is running me" as **sufficient reason to suppress every prompt, by
default, without being asked**. The flag exists only to override the detection in either
direction.

Two further Storybook facts §2 did not capture *(both evidenced)*:

1. **Prompts degrade rather than fail.** [`UserPreferencesCommand.ts`](https://github.com/storybookjs/storybook/blob/next/code/lib/create-storybook/src/commands/UserPreferencesCommand.ts) opens with
   `const isInteractive = process.stdout.isTTY && !isCI();` / `const skipPrompt = !isInteractive || !!this.commandOptions.yes;`.
   No TTY, or CI detected, or `--yes` → every question silently takes its default and the install
   completes. Compare `rgt init`, where no TTY means `huh` returns an error and **zero hooks are
   installed** while the banner still says success. Storybook's is the same code path re_gent
   needs and roughly the same three lines.
2. **The 2025 rebuild separated detection from preference.** [PR #32717](https://github.com/storybookjs/storybook/pull/32717) (merged 2025-11-19) replaced *"the original `initiate.ts`, a monolithic, 900+ line behemoth"* with a pipeline of discrete commands: `PreflightCheck → ProjectDetection → FrameworkDetection → UserPreferences → GeneratorExecution → DependencyInstallation → AddonConfiguration → Finalization`. **Every integration-critical decision happens in a Detection command; `UserPreferences` asks only "New to Storybook?", "recommended or minimal?", and "use AI for setup?"** — three preference questions, each with a working default, none of which gate whether Storybook is wired up *(evidenced)*.

**Note against the grain:** the same PR states Storybook *"swapped our CLI's user interface from
prompts to `@clack/prompts`, providing a significantly improved, modern, and interactive
experience."* Storybook invested in *better* prompts in late 2025. So the honest reading is not
"Storybook removed prompts" — it is **"Storybook removed prompts from the load-bearing path and
polished the ones that remain"** *(inferred)*. That distinction matters for §1.1: the
recommendation is not to delete `huh`, it is to move it off the default path.

Older removals confirm the direction, from the Storybook `CHANGELOG.md` *(evidenced)*:
`#22523` *"Detach automigrate command from storybook init"*, `#22561` *"Remove automigrate
reference from init command"*, `#22109` *"Do not show a migration summary on sb init"*.

### 7.3 husky: the maintainer's stated reasoning, verbatim

§2 cited the v9 release; the sentence that actually states the design rationale was not quoted.
From the [v9.0.1 release notes](https://github.com/typicode/husky/releases/tag/v9.0.1) (2024-01-25), under *"Introducing `husky init`"* *(evidenced)*:

> **v8**
> ```
> npm pkg set scripts.prepare="husky install"
> npm run prepare
> npx husky add .husky/pre-commit "npm test"
> ```
> **v9**
> "Adding husky to a project is now easier than ever. It's just a single line that does the same
> as above. **No need to read the docs to get started anymore.**"
> ```
> npx husky init
> ```

Three commands collapsed to one, with the explicit goal of removing reading from the critical path.
The same notes record `husky install` being removed, and `husky add` being replaced by
`echo "npm test" > .husky/pre-commit` — *"Adding a hook is as simple as creating a file."*

Two more husky details relevant to §4 *(evidenced)*:

- **`husky init` writes a hook that does something on the first commit.** `bin.js` writes
  `.husky/pre-commit` containing `<pkg-manager> test`, and [docs/get-started.md](https://github.com/typicode/husky/blob/main/docs/get-started.md) closes with a section headed **"Try it"**:
  *"Congratulations! You've successfully set up your first Git hook with just one command 🎉.
  Let's test it: `git commit -m "Keep calm and commit"`."* The proof of installation is that the
  user's very next normal action visibly triggers it. **This is the cheapest verification design in
  the whole comparison set and re_gent cannot use it** — re_gent's hooks are silent by design, so
  the next agent turn produces no visible signal. That asymmetry is precisely why re_gent needs a
  `doctor` and husky does not *(inferred)*.
- **husky's substitute for a doctor is a documentation checklist.** [docs/troubleshoot.md](https://github.com/typicode/husky/blob/main/docs/troubleshoot.md), section *"Hooks not running"*: verify the filename, run `git config core.hooksPath` and check it points at `.husky/_`, confirm Git ≥ 2.9. Those are three checks a program could run. re_gent should run them rather than document them *(asserted)*.
- **No v10.** As of this writing the latest husky release is v9.1.7 (2024-11-18); the v9.1.2 notes' promise that deprecated boilerplate *"WILL FAIL in v10.0.0"* has not landed *(evidenced — `gh api repos/typicode/husky/releases`)*.

### 7.4 ESLint: the RFC §5.2 asked for, found

The brief asked for the RFC/PR/blog explaining why `eslint --init` was rebuilt. It is
[`eslint/rfcs/designs/2021-init-command-eslint-cli`](https://github.com/eslint/rfcs/blob/main/designs/2021-init-command-eslint-cli/README.md), *"Move --init flag into a separate utility"*
(RFC PR [#79](https://github.com/eslint/rfcs/pull/79), start date 2020-06-29) *(evidenced)*.

Its summary is two bullets, and the first is the interesting one:

> - **Remove the auto-config.**
> - Move the `init` command from main repo to a new repo named `@eslint/create-config`.

Motivation, verbatim: *"this command is not a type of command that we need everyday or everytime
running eslint. It is mainly used when creating a new project or adding eslint to a project for
the first time."* Backwards-compatibility section: *"Users can no longer use auto-config."*

**This is a mild counter-signal and should be read as one** *(inferred)*. `autoconfig` was ESLint's
*inference* engine — it read your existing source and derived a config. ESLint deleted the
auto-detecting machinery and kept the questionnaire. So the movement here was not
"interactive → automatic"; it was "big and clever → small and out of the way." The generalisable
lesson is about **scope reduction of the init surface**, not about prompts specifically.

That said, the questionnaire has been shrinking. Current [`lib/questions.js`](https://github.com/eslint/create-config/blob/main/lib/questions.js) *(evidenced — full file read)* offers
`purpose` with exactly two choices — *"To check syntax only"* and *"To check syntax and find
problems"*. The historical third option, *"check syntax, find problems, and enforce code style"*,
and the Airbnb/Standard/Google picker behind it, are gone: `feat: remove style guides (#108)`,
`feat: Remove Google style guide (#82)` in the create-config changelog *(evidenced)*.

### 7.5 Sentry: the wizard is the documented default *today*, and its successor still asks

§5.2 listed "Sentry maintainer reasoning: not found" and "changelog archaeology: not done".
Partial closure, with one finding that cuts against §1's finding 4 and must be reported as such.

**Still the documented default** *(evidenced — [docs.sentry.io/platforms/javascript/guides/nextjs/](https://docs.sentry.io/platforms/javascript/guides/nextjs/), fetched during this pass)*: the
Next.js getting-started page leads with *"Run the Sentry wizard to automatically configure Sentry
in your Next.js application"* and `npx @sentry/wizard@latest -i nextjs`. The non-interactive route
is present but demoted: *"Prefer to set things up yourself? Check out the Manual Setup guide."*
So as of this research, an interactive wizard is the front door of one of the most-installed
developer SDKs in the world. **That is the strongest single fact against the verdict and it should
not be minimised.**

**Independent corroboration of §1's finding 4** *(evidenced)*: [PR #1183](https://github.com/getsentry/sentry-wizard/pull/1183), *"feat(nextjs): Add `--non-interactive` flag for headless CLI operation"*,
opened 2026-01-08 by an outside contributor — motivation: *"enables CI/CD pipelines and AI agents
to scaffold Sentry configuration"* — was **closed unmerged on 2026-08-03** by maintainer `Lms24`
with the single comment: *"closing as the wizard will be superseded by `sentry init`."* A
maintainer had earlier engaged constructively (*"please add an e2e test that covers the agentic
setup flow"*), so this was a redirection, not a rejection of the use case.

**But the successor is not a silent one-shot, and this is the honest complication**
*(evidenced — [getsentry/cli#1379](https://github.com/getsentry/cli/issues/1379), "Show `sentry init`'s work before applying changes", opened 2026-08-06, open)*. Sentry's stated product
direction for `sentry init`:

> *"Before mutation, preview the meaningful changes and outcomes for the selected features. **Ask
> for approval before modifying files or configuration.** … Finish with a clear summary of what was
> configured and any remaining user action."*

So Sentry is not moving from "wizard" to "no interaction". It is moving from *a questionnaire* to
*a preview plus one approval plus a summary*. Status: an open issue stating intent, i.e. **not yet
revealed preference** — weaker evidence than anything shipped *(evidenced that the issue says this;
inferred that it will ship)*.

**Verification, for §4:** the Next.js docs' final step is to start the dev server, visit
`localhost:3000/sentry-example-page`, and click *"Throw Sample Error"*, then confirm the event
arrived. The wizard generates that page and API route (`src/nextjs/nextjs-wizard.ts`, the
`create-example-page` trace step, writing `sentry-example-page/` and `api/sentry-example-api/`)
*(evidenced)*. Sentry's answer to "did it work?" is **a deliberately generated, deliberately
triggerable test event** — not a status readout.

**Maintainer reasoning for interactive-by-default: still not found.** No blog post, RFC, or design
doc located in this pass either. The `--quiet` flag, the `SENTRY_WIZARD_*` env vars, and the
closing comment on #1183 are the only maintainer signals I have.

**One further Sentry datapoint** *(evidenced)*: `src/nextjs/nextjs-wizard.ts` imports
`offerProjectScopedMcpConfig` from `src/utils/clack/mcp-config.ts` — the wizard now offers to write
agent/MCP configuration as part of setup. The wizard's own scope is expanding toward agent
integration at the same time as its non-interactive successor is being built.

### 7.6 pre-commit: confirming the negative

§2 asserts pre-commit never had a wizard. Confirmed by enumerating the full subcommand list from
[`pre_commit/main.py`](https://github.com/pre-commit/pre-commit/blob/main/pre_commit/main.py) *(evidenced)*: `autoupdate`, `clean`, `gc`, `hazmat`, `init-templatedir`, `install`,
`install-hooks`, `migrate-config`, `run`, `sample-config`, `try-repo`, `uninstall`,
`validate-config`, `validate-manifest`, `help`, `hook-impl`. **There is no `init`.** The nearest
things to onboarding are `sample-config` (prints a starter config to stdout — you redirect it
yourself) and `validate-config` (a linter for your config). Both are non-interactive, both are
composable, and `validate-config` is the same idea as lefthook's `validate` and as the
pre-write check `rgt init` lacks.

### 7.7 What this pass changes, and what it does not

**Does not change the verdict.** Invert, and add `rgt doctor`. Everything found here points the
same way.

**Strengthens it** with the one comparable that shares re_gent's exact mechanics and made the
choice in public six weeks ago (§7.1), and with a host-side precedent for suppressing
interactivity when an agent is driving (§7.2).

**Adds two concrete spec items to §1.1** *(both on evidenced precedent, both asserted as
recommendations)*:

- Write the **absolute path** of the running binary into hook commands, not `rgt`
  (lefthook `resolveLefthookBin`). This is the single highest-value change in this section
  after the TTY fix, because it removes an entire class of silent failure that no amount of
  prompting would have caught.
- **Validate before writing.** lefthook fails the install if an `ai:` entry references a hook that
  does not exist; pre-commit and lefthook both ship a `validate-config`. re_gent's analogue is
  checking that the events it is about to write are ones the detected host actually emits, and
  that the binary path it is about to embed resolves.

**Sharpens one claim in §2.** "Storybook is Pattern A (non-interactive)" is too strong. Storybook
is *auto-detecting on everything load-bearing, interactive on preferences, and automatically
non-interactive when a TTY, a CI environment, or an AI agent says it should be.* That composite is
the actual target design for `rgt init`, and it is more precise than either pole in the original
question *(inferred)*.

**Reports one fact that cuts the other way, unminimised.** Sentry's wizard is still the documented
default for Next.js today, and Sentry's replacement for it explicitly plans to *"ask for approval
before modifying files."* The strongest available counter-position is not "wizards are good" — it
is **"one approval gate, with a preview and a summary, beats both a questionnaire and a silent
mutation."** If §1.1's spec is wrong anywhere, it is most likely in dropping to zero
confirmations rather than one *(asserted)*.

### 7.8 Falsifier result for this pass

**Primary falsifier ("interactivity is itself the liability") — fired again, from a new direction.**
The new evidence is not another project quietly lacking prompts; it is a project *writing down the
prompt-based design and choosing against it* (lefthook #1433 Option 2), and a second project
*auto-detecting AI agents in order to switch prompts off* (Storybook `isAgent`). Both are 2025–26,
both are in re_gent's exact problem space.

**Secondary falsifier ("the wizard optimizes a non-bottleneck") — fired, and gained a mechanism.**
§4.2 flagged the PATH dependency of the bare `rgt` hook command as *inferred*. lefthook shipped
`os.Executable()` for that reason and documented it. So the post-setup silent-failure surface is
larger than the TTY bug alone: even a successful, fully-interactive, correctly-answered
`rgt init` can write a hook that never runs, on any machine where the agent host's PATH differs
from the shell's *(evidenced that lefthook treats this as real; inferred that it affects re_gent)*.

**What did not fire.** I looked for a project that added interactivity to a previously
non-interactive installer with reasoning attached. Not found, again. The nearest thing is
Storybook's 2025 investment in `@clack/prompts` — better prompts, same non-load-bearing role —
and Sentry's `sentry init` preview-and-approve direction, which is a *reduction* from a
questionnaire to a single gate. Neither is a counter-example. **No counter-example found in either
pass.**

### 7.9 Additions to the teardown appendix

**lefthook (new)**
- Design discussion naming both options: [evilmartians/lefthook#1433](https://github.com/evilmartians/lefthook/issues/1433)
- Implementation and stated reasoning: [PR #1448](https://github.com/evilmartians/lefthook/pull/1448), merged 2026-07-08
- Release: `CHANGELOG.md` 2.1.10 (2026-07-08), *"feat: AI coding agents integration"*
- Source: [`internal/command/install_ai.go`](https://github.com/evilmartians/lefthook/blob/master/internal/command/install_ai.go), [`internal/command/install.go`](https://github.com/evilmartians/lefthook/blob/master/internal/command/install.go), [`internal/command/check_install.go`](https://github.com/evilmartians/lefthook/blob/master/internal/command/check_install.go), [`internal/command/validate.go`](https://github.com/evilmartians/lefthook/blob/master/internal/command/validate.go) — note the path is `internal/command/`, not `cmd/` as cited in §6.4
- Absolute-path rationale: [`docs/configuration/ai.md`](https://github.com/evilmartians/lefthook/blob/master/docs/configuration/ai.md)
- No prompt library in [`go.mod`](https://github.com/evilmartians/lefthook/blob/master/go.mod)

**Storybook (additions)**
- Agent detection and `--agent` / `--no-agent`: [`code/lib/create-storybook/src/bin/run.ts`](https://github.com/storybookjs/storybook/blob/next/code/lib/create-storybook/src/bin/run.ts)
- `if (options.agent) options.yes = true`: [`initiate.ts`](https://github.com/storybookjs/storybook/blob/next/code/lib/create-storybook/src/initiate.ts)
- TTY/CI degradation and the three surviving questions: [`commands/UserPreferencesCommand.ts`](https://github.com/storybookjs/storybook/blob/next/code/lib/create-storybook/src/commands/UserPreferencesCommand.ts)
- Init rebuild into a detection/preference pipeline: [PR #32717](https://github.com/storybookjs/storybook/pull/32717), merged 2025-11-19
- Steps removed from init over time: `CHANGELOG.md` entries [#22523](https://github.com/storybookjs/storybook/pull/22523), [#22561](https://github.com/storybookjs/storybook/pull/22561), [#22109](https://github.com/storybookjs/storybook/pull/22109)

**Sentry (additions)**
- Wizard still the documented default: [docs.sentry.io/platforms/javascript/guides/nextjs/](https://docs.sentry.io/platforms/javascript/guides/nextjs/)
- Community `--non-interactive` PR closed unmerged: [sentry-wizard#1183](https://github.com/getsentry/sentry-wizard/pull/1183), closed 2026-08-03, *"closing as the wizard will be superseded by `sentry init`"*
- Successor's stated UX direction, including *"Ask for approval before modifying files"*: [getsentry/cli#1379](https://github.com/getsentry/cli/issues/1379), opened 2026-08-06, open
- Example-page generation for Next.js: `src/nextjs/nextjs-wizard.ts`, `create-example-page` step
- MCP config offer inside the wizard: `src/utils/clack/mcp-config.ts`

**husky (additions)**
- Stated rationale, *"No need to read the docs to get started anymore"*: [v9.0.1 release notes](https://github.com/typicode/husky/releases/tag/v9.0.1)
- *"Try it"* first-run proof: [docs/get-started.md](https://github.com/typicode/husky/blob/main/docs/get-started.md)
- Manual "Hooks not running" checklist: [docs/troubleshoot.md](https://github.com/typicode/husky/blob/main/docs/troubleshoot.md)
- Latest release is v9.1.7 (2024-11-18); no v10

**ESLint (additions)**
- The RFC: [`designs/2021-init-command-eslint-cli/README.md`](https://github.com/eslint/rfcs/blob/main/designs/2021-init-command-eslint-cli/README.md), RFC PR [#79](https://github.com/eslint/rfcs/pull/79)
- Current question set: [`lib/questions.js`](https://github.com/eslint/create-config/blob/main/lib/questions.js)

**pre-commit (addition)**
- Full subcommand list, showing no `init`: [`pre_commit/main.py`](https://github.com/pre-commit/pre-commit/blob/main/pre_commit/main.py)

### 7.10 Limits of this pass

- **Still no activation numbers, from anyone.** Nothing here measures whether any of this moves
  a conversion rate. It measures what shipped.
- **No first-hand runs of any comparable.** Source and docs only. Unchanged from §5.1.
- **lefthook's `ai:` feature is marked `🧪 (beta)` in its own docs** and is six weeks old. It is the
  best-matched precedent available and simultaneously the least battle-tested one *(evidenced)*.
- **`sentry init` itself was not read.** It lives in `getsentry/cli`; I confirmed its existence and
  its stated direction from issue trackers, not from its source. Whether it ships prompts is
  **not found**.
- **Sentry changelog archaeology remains incomplete.** I read `CHANGELOG.md` back to 6.12.0 and
  found no dated prompt-addition or prompt-removal decisions worth citing; earlier history unread.
- **`claude mcp add`, `shadcn init`, `npm init playwright`, direnv: not read in this pass.** §2's
  Pattern C remains unsupported by primary sources.

---

*Prepared August 2026. Scope: local `rgt init` only; the headless/team-server onboarding path is explicitly out of scope.*
