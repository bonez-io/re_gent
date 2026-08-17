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
// to make. So both directories are named and the choice stays with the user.
func reportClaudeSettingsScope(projectRoot string) {
	shadow := shadowingClaudeWorkspace(projectRoot)
	if shadow == "" {
		return
	}
	fmt.Printf("  %s An agent opened at %s will not load them.\n", style.Warning(""), shadow)
	fmt.Printf("    Claude Code reads .claude/settings.json from the directory it was opened in,\n")
	fmt.Printf("    and that one has its own .claude/ with no re_gent hook. Either open the agent\n")
	fmt.Printf("    in this project, or wire that directory too:\n")
	fmt.Printf("      cd %s && rgt init --agent claude\n", shadow)
}

// shadowingClaudeWorkspace returns the nearest directory above projectRoot that
// an agent could be opened at and would then not load this project's hooks, or
// "" when there is no such directory.
//
// A .claude/ directory is the marker of a place an agent gets opened, so the
// nearest ancestor holding one is the candidate. It is only a problem when that
// directory has no re_gent hook of its own — an ancestor that is already wired
// captures the work either way, and warning about it would be noise.
//
// The user's home directory is skipped: ~/.claude is user scope and Claude Code
// loads it wherever it is opened, so it can never be the reason a hook fails to
// fire. Almost every machine has one, and treating it as a shadow would make
// this warning fire on every project on the box.
func shadowingClaudeWorkspace(projectRoot string) string {
	home, _ := os.UserHomeDir()
	dir := filepath.Dir(projectRoot)
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		if dir != home && pathExists(filepath.Join(dir, ".claude")) {
			if claudeSettingsFileHasRegentHook(filepath.Join(dir, ".claude", "settings.json")) {
				return ""
			}
			return dir
		}
		dir = parent
	}
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
func claudeUserHook() string      { return hookCommand(claudeUserHookArgs) }
func claudeAssistantHook() string { return hookCommand(claudeAssistantHookArgs) }
func claudeToolBatchHook() string { return hookCommand(claudeToolBatchHookArgs) }
func codexHookCommand() string    { return hookCommand(codexHookArgs) }
