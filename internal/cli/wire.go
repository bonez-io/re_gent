package cli

import (
	"fmt"
	"path/filepath"

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
func wireAgents(projectRoot string, targets []agentTarget) ([]agentTarget, error) {
	installed := make([]agentTarget, 0, len(targets))

	for _, target := range targets {
		switch target {
		case agentClaude:
			result, err := installClaudeHook(projectRoot)
			if err != nil {
				return installed, fmt.Errorf("configure Claude Code hooks: %w", err)
			}
			printHookInstallWarning(result)
			reportWired("Claude Code", filepath.Join(projectRoot, ".claude", "settings.json"))
			installed = append(installed, agentClaude)

		case agentCodex:
			result, err := installCodexHook(projectRoot)
			if err != nil {
				return installed, fmt.Errorf("configure Codex hooks: %w", err)
			}
			printHookInstallWarning(result)
			reportWired("Codex", filepath.Join(projectRoot, ".codex", "config.toml"))
			installed = append(installed, agentCodex)

		case agentOpenCode:
			if err := installOpenCodeHook(projectRoot); err != nil {
				return installed, fmt.Errorf("configure OpenCode plugin: %w", err)
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
			return installed, fmt.Errorf("cannot wire unknown agent %q", target)
		}
	}

	return installed, nil
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

// configureHooks is the decision layer above wireAgents. Prompting is opt-in
// rather than the default because the default path has to survive `curl | sh`,
// devcontainers, CI, and SSH — none of which can answer a question.
func configureHooks(projectRoot string, targets []agentTarget, opts hookOptions) ([]agentTarget, error) {
	if opts.skip {
		fmt.Printf("  %s Hook configuration skipped\n", style.DimText("-"))
		printManualInstructions(targets)
		return nil, nil
	}

	if opts.interactive {
		return offerHookInstall(projectRoot, targets, nil)
	}

	return wireAgents(projectRoot, targets)
}

// summaryStatus derives the closing headline from what was actually installed.
//
// This is the fix for the shipped bug: printSummary was handed the *detected*
// targets, so a run that installed nothing still printed
// "Initialization complete". The boolean is the caller's exit code.
func summaryStatus(installed []agentTarget) (string, bool) {
	if len(installed) == 0 {
		return "Initialization incomplete - no agent hooks were configured", false
	}
	return "Initialization complete", true
}
