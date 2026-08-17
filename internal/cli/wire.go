package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/regent-vcs/regent/internal/capture"
	"github.com/regent-vcs/regent/internal/style"
)

// wireAgents installs hooks for every target it is given, with no prompting,
// and returns only the targets whose hooks were actually written.
//
// This is the single entry point for hook wiring. It exists because the two
// callers previously disagreed: init.go gated installation behind an
// interactive multi-select (unusable under `curl | sh`, in a devcontainer, or
// over SSH), while connect.go hardcoded Claude and silently ignored the other
// three agents.
//
// The returned slice is what callers must report to the user. Reporting the
// *requested* targets instead is what made `rgt init` claim success after
// installing nothing.
//
// Every requested agent is attempted, including the ones after a failure. A
// stale `.codex` file is a reason to not have Codex hooks; it is not a reason
// to not have Claude hooks. Returning early made the two indistinguishable and
// made which agent survived depend on the order resolveAgentTargets happened to
// produce. Failures are collected and returned together, so the caller can both
// report the agents that were written and fail the run.
func wireAgents(projectRoot string, targets []agentTarget) ([]agentTarget, error) {
	installed := make([]agentTarget, 0, len(targets))
	var failures []error

	for _, target := range targets {
		switch target {
		case agentClaude:
			result, err := installClaudeHook(projectRoot)
			if err != nil {
				failures = append(failures, fmt.Errorf("configure Claude Code hooks: %w", err))
				continue
			}
			printHookInstallWarning(result)
			reportWired("Claude Code", filepath.Join(projectRoot, ".claude", "settings.json"))
			reportClaudeSettingsScope(projectRoot)
			installed = append(installed, agentClaude)

		case agentCodex:
			result, err := installCodexHook(projectRoot)
			if err != nil {
				failures = append(failures, fmt.Errorf("configure Codex hooks: %w", err))
				continue
			}
			printHookInstallWarning(result)
			reportWired("Codex", filepath.Join(projectRoot, ".codex", "config.toml"))
			installed = append(installed, agentCodex)

		case agentOpenCode:
			if err := installOpenCodeHook(projectRoot); err != nil {
				failures = append(failures, fmt.Errorf("configure OpenCode plugin: %w", err))
				continue
			}
			reportWired("OpenCode", filepath.Join(projectRoot, "opencode.jsonc"))
			installed = append(installed, agentOpenCode)

		case agentPi:
			// installPiHook reports whether it found a Pi installation to
			// extend. A false return is not an error, but it is also not an
			// install, so it must not appear in the summary.
			if installPiHook(projectRoot) {
				reportWired("Pi", filepath.Join(projectRoot, ".pi", "settings.json"))
				installed = append(installed, agentPi)
			}

		default:
			failures = append(failures, fmt.Errorf("cannot wire unknown agent %q", target))
		}
	}

	return installed, errors.Join(failures...)
}

// resolveHookBinary decides what an agent hook should invoke.
//
// Hooks run inside the agent host's environment, not the shell the user
// installed from, so a bare "rgt" depends on PATH resolution that may simply
// not be there — capture then fails silently, which is the failure mode this
// whole area exists to remove. lefthook, which shipped the same Claude/Codex
// hook feature in July 2026, resolves the absolute path for the documented
// reason that "AI tools do not depend on lefthook being on PATH".
//
// The fallback matters as much as the happy path. Under `go test` the running
// executable is the test binary, and under `go run` it is a temporary build
// that will not exist tomorrow; writing either into a user's config would
// produce a hook that never fires. In those cases, and only those, keep the
// bare name and let PATH do its best.
//
// This used to fall back whenever the binary was not named "rgt", which quietly
// answered a different question than the one that matters. A build called
// rgt-dev is every bit as real and permanent as one called rgt; refusing to
// name it wrote a bare "rgt" into the config, and the hook then invoked
// whichever other build PATH happened to find — or none at all. Durability of
// the path is the question. The filename is the user's business.
func resolveHookBinary(exe string, lookupErr error) string {
	if lookupErr != nil || exe == "" {
		return "rgt"
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if isEphemeralBuild(exe) {
		return "rgt"
	}
	return exe
}

// isEphemeralBuild reports whether a path belongs to something the Go toolchain
// built to run once: a `go test` binary, or the temporary executable behind
// `go run`. Both live under a go-build directory, and both are gone by the time
// an agent host would try to invoke them.
func isEphemeralBuild(exe string) bool {
	if strings.HasSuffix(exe, ".test") {
		return true
	}
	for _, part := range strings.Split(filepath.ToSlash(exe), "/") {
		if strings.HasPrefix(part, "go-build") {
			return true
		}
	}
	return false
}

// regentHookVerbs are the subcommands re_gent writes into agent hook configs.
//
// These, not the filename, are what identify a hook as ours. A binary's name is
// the user's choice and they rename it for ordinary reasons — a dev build kept
// beside a release, a versioned copy — but nothing other than re_gent is ever
// invoked with `tool-batch-hook`. Derived from the constants the installer
// actually writes, so the reader and the writer cannot drift apart.
var regentHookVerbs = []string{
	firstWord(claudeUserHookArgs),
	firstWord(claudeAssistantHookArgs),
	firstWord(claudeToolBatchHookArgs),
	firstWord(codexHookArgs),
}

func firstWord(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// isRegentHookCommand reports whether a configured hook command is one re_gent
// wrote. Field 0 is deliberately not inspected: that is the binary path, and
// its name is exactly what this must not depend on.
//
// Callers pass both parsed JSON values and raw TOML lines, so each field is
// stripped of the punctuation those formats leave attached.
func isRegentHookCommand(command string) bool {
	fields := strings.Fields(command)
	if len(fields) < 2 {
		return false
	}
	for _, field := range fields[1:] {
		field = strings.Trim(field, `"'[],`)
		for _, verb := range regentHookVerbs {
			if field == verb {
				return true
			}
		}
	}

	// The legacy form, from before the hook verbs were split apart: `rgt hook`.
	// Here the binary's name is the right thing to match on, because that
	// installer wrote the bare name and nothing else — there is no rgt-dev
	// spelling of a command that predates rgt-dev being possible. Matching a
	// bare "hook" on its own would be too eager and would delete somebody's
	// `make hook` on the next re-init.
	return capture.IsRegentCommand(command) &&
		strings.Trim(fields[1], `"'[],`) == "hook"
}

// hookBinary is resolveHookBinary applied to the running process.
func hookBinary() string {
	exe, err := os.Executable()
	return resolveHookBinary(exe, err)
}

// hookCommandWith joins a binary and its arguments into the string an agent
// host will execute. Kept separate from hookBinary so the composition can be
// tested against paths this process will never have.
func hookCommandWith(binary, args string) string {
	return binary + " " + args
}

// sharedHookCommand is hookCommandWith for a config file that gets committed.
//
// When the binary is an absolute path the command tries that path first and
// falls back to whatever `rgt` PATH resolves. Both halves are load-bearing and
// neither is sufficient on its own:
//
//   - The absolute path is what makes the hook fire on the machine that ran the
//     install. Hooks run inside the agent host's environment, whose PATH is not
//     the installing shell's — see resolveHookBinary.
//   - The fallback is what makes the same file work after somebody else clones
//     it. `rgt connect` tells users to commit .claude/settings.json so teammates
//     are wired by cloning; without a fallback the committed hook names a path
//     that exists on exactly one machine, and the teammate's capture silently
//     never starts (#23). Agent hosts do not report a hook that failed to
//     launch, so nothing else would have said so.
//
// `exec` is what makes `||` mean "could not run it" rather than "it ran and
// exited non-zero". Without it, a hook that reported a problem would be run a
// second time through the fallback and the turn would be captured twice.
//
// Only .claude/settings.json is written this way, because it is the only agent
// config re_gent tells anyone to commit — see sharedFiles. Spelling the command
// as a shell expression assumes the host runs it through a shell, which Claude
// Code documents and the others have not been verified to do; guessing on their
// behalf would risk breaking capture that works today.
func sharedHookCommand(binary, args string) string {
	if !filepath.IsAbs(binary) {
		return hookCommandWith(binary, args)
	}
	quoted := shellQuote(binary)
	return fmt.Sprintf("[ -x %s ] && exec %s %s || exec rgt %s", quoted, quoted, args, args)
}

// shellQuote wraps a path so a POSIX shell reads it as one literal word.
//
// Single quotes rather than double, because a home directory is user-controlled
// text that reaches this file: a path holding `$`, a backtick or a backslash
// would be expanded inside double quotes and the hook would invoke something
// other than the binary that installed it.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// hookCommand builds a hook command for the running binary.
func hookCommand(args string) string {
	return hookCommandWith(hookBinary(), args)
}

// reportWired names the file that was actually written. Naming the path (rather
// than printing a bare "hooks configured") is what lets a user verify the claim
// without trusting it.
func reportWired(agent, path string) {
	fmt.Printf("  %s %s hooks configured -> %s\n", style.Success(""), agent, path)
}

// reportClaudeSettingsScope warns when the settings just written are not the
// ones the agent will read.
//
// Claude Code loads .claude/settings.json from the directory it was opened in.
// We write it at the project root, which is not necessarily that directory:
// open the agent at a workspace root above the project — a monorepo, a
// checkout holding several projects — and the hooks are never loaded, so
// capture silently never starts. Naming the written path was not enough,
// because the path looks right; what is wrong is which directory the agent is
// rooted at, and only the user knows that.
//
// Where the settings *should* live is deliberately not decided here. Writing
// them to the ancestor would impose re_gent's hooks on every other project
// underneath it, which is a bigger claim than a project-level init is entitled
// to make. So both directories are named and the choice stays with the user —
// but the recommendation is no longer neutral between them, because the two
// outcomes are not equivalent. See claudeShadowRemedy.
func reportClaudeSettingsScope(projectRoot string) {
	shadow := shadowingClaudeWorkspace(projectRoot)
	if !shadow.found() {
		return
	}
	fmt.Printf("  %s An agent opened at %s will not load them.\n", style.Warning(""), shadow.Dir)
	for _, line := range strings.Split(claudeShadowRemedy(projectRoot, shadow), "\n") {
		fmt.Printf("    %s\n", line)
	}
}

// shadowingClaudeWorkspace returns the nearest directory above projectRoot that
// an agent could be opened at and would then not load this project's hooks, or
// the zero value when there is no such directory.
//
// A .claude/ directory is the marker of a place an agent gets opened, so the
// nearest ancestor holding one is the candidate.
//
// Whether that ancestor is itself wired is reported, not used to dismiss it.
// This used to return "no shadow here" the moment the ancestor had a re_gent
// hook, on the reasoning that an already-wired ancestor "captures the work
// either way". It does capture — into the ancestor's own .regent/, because
// capture.Open resolves its store from the agent session's working directory
// (cwd/.regent, no upward walk). So the work lands one directory up, blended
// with every other project under it, this project's .regent/ stays empty, and
// doctor printed four green ticks over it (#27). "Captured somewhere" and "this
// project is fine" are two facts, and only the caller can decide what to say
// about the difference.
//
// The user's home directory is skipped: ~/.claude is user scope and Claude Code
// loads it wherever it is opened, so it can never be the reason a hook fails to
// fire. Almost every machine has one, and treating it as a shadow would make
// this warning fire on every project on the box.
func shadowingClaudeWorkspace(projectRoot string) claudeWorkspaceShadow {
	home, _ := os.UserHomeDir()
	dir := filepath.Dir(projectRoot)
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return claudeWorkspaceShadow{}
		}
		if dir != home && pathExists(filepath.Join(dir, ".claude")) {
			return claudeWorkspaceShadow{
				Dir:   dir,
				Wired: claudeSettingsFileHasRegentHook(filepath.Join(dir, ".claude", "settings.json")),
			}
		}
		dir = parent
	}
}

// claudeWorkspaceShadow names an ancestor directory an agent could be opened at
// instead of this project, and says whether that directory is itself wired.
//
// The two fields carry the two outcomes apart, because they are not the same
// loss. Unwired, a session opened there captures nothing anywhere. Wired, it
// captures — somewhere else. The first is a failure and the second a warning,
// and collapsing them is what produced both halves of #27.
type claudeWorkspaceShadow struct {
	Dir   string
	Wired bool
}

// found reports whether there is such an ancestor at all.
func (s claudeWorkspaceShadow) found() bool { return s.Dir != "" }

// claudeShadowRemedy is the advice `rgt init`, `rgt connect` and `rgt doctor`
// all print about a shadowing ancestor. One builder, because three commands
// describing the same situation in three ways is how the wrong one survived.
//
// It leads with opening the agent inside the project, which is the layout the
// whole model is built for: hook loaded from the project, cwd inside it,
// history in its own .regent/. The previous advice led with
// `cd <ancestor> && rgt init --agent claude` and called it the fix. It is not:
// capture resolves a project from the session's working directory, so wiring
// the ancestor makes every project underneath record into one blended history
// at the top, where `rgt log` in the project shows nothing and `rgt blame` on
// one of its files reads a session that also touched four other repos. A real
// user followed that advice and landed in a worse state than they started (#27).
//
// The ancestor-wide alternative is still named — it is a legitimate choice for
// someone who genuinely wants one history across a workspace — but with its
// consequence attached, and only while it is still a choice. Once the ancestor
// is wired, the consequence has already happened and is described in the past
// tense by shadowConsequence instead.
func claudeShadowRemedy(projectRoot string, shadow claudeWorkspaceShadow) string {
	lines := []string{
		shadowConsequence(shadow),
		fmt.Sprintf("open the agent inside this project — cd %s && claude — and its work is recorded in %s",
			projectRoot, filepath.Join(projectRoot, ".regent")),
	}
	if !shadow.Wired {
		lines = append(lines, fmt.Sprintf(
			"wiring %s instead (rgt init --agent claude there) also captures, but into a single history at %s shared by every project under it — rgt log and rgt blame in this project would still show nothing",
			shadow.Dir, filepath.Join(shadow.Dir, ".regent")))
	}
	return strings.Join(lines, "\n")
}

// shadowConsequence states what happens today, which is the fact the user is
// actually missing. Naming the directory the work lands in is what turns an
// empty `rgt log` from a mystery into an explanation.
func shadowConsequence(shadow claudeWorkspaceShadow) string {
	if shadow.Wired {
		return fmt.Sprintf(
			"an agent opened at %s loads that directory's .claude/settings.json, not this project's. It is wired, so the work is recorded — in %s, blended with every other project below it, while this project's own .regent/ stays empty",
			shadow.Dir, filepath.Join(shadow.Dir, ".regent"))
	}
	// Conditional, not a verdict. Whether this bites depends on where the user
	// opens their agent, which nothing here can see — stating it flatly told
	// someone who opens the agent inside the project that nothing would be
	// captured, while it was being captured correctly.
	return fmt.Sprintf(
		"if you open your agent at %s, it loads that directory's .claude/settings.json rather than this project's, and that one has no re_gent hook — nothing would be captured, there or here",
		shadow.Dir)
}

// hookOptions selects how configureHooks behaves. The zero value is the
// default: wire everything detected, ask nothing.
type hookOptions struct {
	skip bool // --skip-hook: install nothing
}

// hookOutcome carries what was actually installed.
//
// It is a struct rather than a bare []agentTarget on purpose. The original bug
// was printSummary(cwd, targets) — handing the reporting code the *requested*
// agents instead of the *installed* ones. Both were []agentTarget, so the
// compiler could not tell them apart and the mistake looked correct.
//
// Wrapping the result makes that specific mistake fail to compile. A test can
// only catch a bug someone already wrote; a type stops it being written.
type hookOutcome struct {
	installed []agentTarget
}

// configureHooks is the decision layer above wireAgents, and the only decision
// left to make is whether to wire at all.
//
// There used to be a second path here: --interactive presented a full-screen
// multi-select, then ran its own copy of the install switch and returned the
// agents the user *selected* rather than the ones it managed to write. That is
// how `rgt init` came to report success for a Pi extension it had not
// installed. Two dispatchers meant two answers to "what got wired", and only
// one of them was true.
//
// It is gone rather than fixed. A prompt cannot run under `curl | sh`, in a
// devcontainer, in CI, or over SSH, so it could never be the default; a
// non-default path that duplicates the default one earns its bugs. Choosing
// agents is what --agent is for, and that works everywhere.
func configureHooks(projectRoot string, targets []agentTarget, opts hookOptions) (hookOutcome, error) {
	if opts.skip {
		fmt.Printf("  %s Hook configuration skipped\n", style.DimText("-"))
		printManualInstructions(targets)
		return hookOutcome{}, nil
	}

	installed, err := wireAgents(projectRoot, targets)
	return hookOutcome{installed: installed}, err
}

// summaryStatus derives the closing headline from what was actually installed.
//
// This is the fix for the shipped bug: printSummary was handed the *detected*
// targets, so a run that installed nothing still printed
// "Initialization complete". The boolean is the caller's exit code.
func summaryStatus(outcome hookOutcome) (string, bool) {
	if len(outcome.installed) == 0 {
		return "Initialization incomplete - no agent hooks were configured", false
	}
	return "Initialization complete", true
}

// The hook commands written into agent configs. These are functions rather
// than constants because the binary path is resolved at run time; see
// resolveHookBinary for why the bare name is not good enough.
//
// The Claude ones go through sharedHookCommand — the same builder the installer
// uses — so that a caller comparing against them cannot be told a command
// re_gent does not actually write.
func claudeUserHook() string      { return sharedHookCommand(hookBinary(), claudeUserHookArgs) }
func claudeAssistantHook() string { return sharedHookCommand(hookBinary(), claudeAssistantHookArgs) }
func claudeToolBatchHook() string { return sharedHookCommand(hookBinary(), claudeToolBatchHookArgs) }
func codexHookCommand() string    { return hookCommand(codexHookArgs) }
