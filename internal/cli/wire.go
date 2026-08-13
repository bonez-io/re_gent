package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
// produce a hook that never fires. When the running binary is not recognisably
// rgt, keep the bare name and let PATH do its best.
func resolveHookBinary(exe string, lookupErr error) string {
	if lookupErr != nil || exe == "" {
		return "rgt"
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if !capture.IsRegentCommand(exe) {
		return "rgt"
	}
	return exe
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

// hookOptions selects how configureHooks behaves. The zero value is the
// default: wire everything detected, ask nothing.
type hookOptions struct {
	skip        bool // --skip-hook: install nothing
	interactive bool // --interactive: present the multi-select
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

// configureHooks is the decision layer above wireAgents. Prompting is opt-in
// rather than the default because the default path has to survive `curl | sh`,
// devcontainers, CI, and SSH — none of which can answer a question.
func configureHooks(projectRoot string, targets []agentTarget, opts hookOptions) (hookOutcome, error) {
	if opts.skip {
		fmt.Printf("  %s Hook configuration skipped\n", style.DimText("-"))
		printManualInstructions(targets)
		return hookOutcome{}, nil
	}

	if opts.interactive {
		selected, err := offerHookInstall(projectRoot, targets, nil)
		return hookOutcome{installed: selected}, err
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
