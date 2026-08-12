package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// wireAgents is the single non-interactive entry point for installing agent
// hooks. Before it existed, init.go gated every install behind a huh
// multi-select and connect.go hardcoded Claude-only, so there was no way to
// wire hooks without a TTY. These tests pin the no-prompt contract.

func TestWireAgentsInstallsEveryTargetWithoutPrompting(t *testing.T) {
	root := t.TempDir()

	installed, err := wireAgents(root, []agentTarget{agentClaude, agentCodex})
	if err != nil {
		t.Fatalf("wireAgents: %v", err)
	}

	if len(installed) != 2 {
		t.Fatalf("installed = %v, want claude and codex", installed)
	}

	for _, rel := range []string{
		filepath.Join(".claude", "settings.json"),
		filepath.Join(".codex", "config.toml"),
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("%s not written: %v", rel, err)
		}
	}
}

// The bug this pins: rgt init printed "Agent skills: log, blame, show" after
// installing nothing, because printSummary was handed the *detected* targets
// rather than the installed ones. wireAgents must never report a target it did
// not actually write.
func TestWireAgentsReportsOnlyWhatItWrote(t *testing.T) {
	root := t.TempDir()

	installed, err := wireAgents(root, []agentTarget{agentClaude})
	if err != nil {
		t.Fatalf("wireAgents: %v", err)
	}

	if len(installed) != 1 || installed[0] != agentClaude {
		t.Fatalf("installed = %v, want [claude]", installed)
	}

	if _, err := os.Stat(filepath.Join(root, ".codex", "config.toml")); err == nil {
		t.Error("codex config written for a target that was never requested")
	}
}

func TestWireAgentsIsIdempotent(t *testing.T) {
	root := t.TempDir()

	if _, err := wireAgents(root, []agentTarget{agentClaude}); err != nil {
		t.Fatalf("first wireAgents: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	if _, err := wireAgents(root, []agentTarget{agentClaude}); err != nil {
		t.Fatalf("second wireAgents: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings again: %v", err)
	}

	if string(first) != string(second) {
		t.Errorf("re-running wireAgents changed settings.json:\nfirst:  %s\nsecond: %s", first, second)
	}
}

// configureHooks is the decision layer above wireAgents: it chooses between
// skipping, prompting, and the non-interactive default. The default must never
// prompt, because `rgt init` runs under `curl | sh` and inside devcontainers.
func TestConfigureHooksDefaultPathWiresWithoutPrompting(t *testing.T) {
	root := t.TempDir()

	outcome, err := configureHooks(root, []agentTarget{agentClaude}, hookOptions{})
	if err != nil {
		t.Fatalf("configureHooks: %v", err)
	}
	if len(outcome.installed) != 1 || outcome.installed[0] != agentClaude {
		t.Fatalf("installed = %v, want [claude]", outcome.installed)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "settings.json")); err != nil {
		t.Errorf("settings.json not written: %v", err)
	}
}

func TestConfigureHooksSkipWiresNothingAndReportsNothing(t *testing.T) {
	root := t.TempDir()

	outcome, err := configureHooks(root, []agentTarget{agentClaude}, hookOptions{skip: true})
	if err != nil {
		t.Fatalf("configureHooks: %v", err)
	}
	if len(outcome.installed) != 0 {
		t.Errorf("installed = %v, want none when skipping", outcome.installed)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude")); err == nil {
		t.Error(".claude created despite --skip-hook")
	}
}

// The shipped bug: with no TTY, rgt init printed "Initialization complete"
// having installed nothing. The headline must follow what was installed, and
// the caller must be able to exit non-zero.
func TestSummaryStatusReportsIncompleteWhenNothingWasWired(t *testing.T) {
	headline, ok := summaryStatus(hookOutcome{})
	if ok {
		t.Error("summaryStatus reported success with no agents wired")
	}
	if headline == "Initialization complete" {
		t.Errorf("headline = %q, must not claim completion", headline)
	}

	headline, ok = summaryStatus(hookOutcome{installed: []agentTarget{agentClaude}})
	if !ok {
		t.Error("summaryStatus reported failure after wiring claude")
	}
	if headline != "Initialization complete" {
		t.Errorf("headline = %q, want %q", headline, "Initialization complete")
	}
}

// Found by mutation testing: reverting init.go to printSummary(cwd, targets)
// — the original bug — left every test green, because the requested and
// installed sets were both []agentTarget and the call site had no test seam.
//
// hookOutcome now makes that call fail to compile. This test pins the
// remaining behavioural half: when a requested agent is not installed, the
// outcome must not contain it.
func TestHookOutcomeCarriesInstalledNotRequested(t *testing.T) {
	root := t.TempDir()

	// Request two agents; block Codex by making its directory a regular file
	// so only Claude can actually be written.
	if err := os.WriteFile(filepath.Join(root, ".codex"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}

	requested := []agentTarget{agentClaude, agentCodex}
	outcome, err := configureHooks(root, requested, hookOptions{})
	if err == nil {
		t.Fatal("configureHooks returned nil error despite Codex being unwritable")
	}

	if len(outcome.installed) == len(requested) {
		t.Fatalf("outcome reports %d installed for %d requested; it is echoing the request",
			len(outcome.installed), len(requested))
	}
	for _, target := range outcome.installed {
		if target == agentCodex {
			t.Error("outcome claims codex was installed, but its write failed")
		}
	}
}

// A teammate pasting an install command must never be told hooks are wired when
// the write failed. wireAgents surfaces the error rather than swallowing it,
// and reports the targets it managed before the failure.
func TestWireAgentsReturnsErrorWhenWriteFails(t *testing.T) {
	root := t.TempDir()

	// Make .claude a regular file so the hook installer cannot create the
	// directory it needs.
	if err := os.WriteFile(filepath.Join(root, ".claude"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}

	installed, err := wireAgents(root, []agentTarget{agentClaude})
	if err == nil {
		t.Fatal("wireAgents returned nil error when the hook write could not succeed")
	}
	if len(installed) != 0 {
		t.Errorf("installed = %v, want none", installed)
	}
}
