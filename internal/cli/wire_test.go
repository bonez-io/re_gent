package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regent-vcs/regent/internal/capture"
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

// Hook commands embed the string an agent host will execute. Using the bare
// name "rgt" makes capture depend on PATH resolution inside whatever
// environment the agent runs in — which is not the shell the user installed
// from. lefthook, which shipped the same Claude/Codex hook feature in July
// 2026, resolves the absolute path for exactly this documented reason.
//
// resolveHookBinary is the decision, kept pure so it can be tested without
// depending on what os.Executable() happens to return under `go test`.
func TestResolveHookBinaryUsesTheAbsolutePathOfTheRunningBinary(t *testing.T) {
	got := resolveHookBinary("/usr/local/bin/rgt", nil)

	if got != "/usr/local/bin/rgt" {
		t.Errorf("resolveHookBinary = %q, want the absolute path so hooks do not depend on PATH", got)
	}
}

// Under `go test` the executable is the test binary; under `go run` it is a
// temporary build that will not exist tomorrow. Writing either into a user's
// hook config would produce a hook that silently never fires — the failure this
// whole branch exists to eliminate. Fall back to the bare name instead.
func TestResolveHookBinaryFallsBackWhenNotRunningAsRgt(t *testing.T) {
	for _, exe := range []string{
		"/tmp/go-build3862142002/b001/cli.test",
		"/var/folders/xy/T/go-build/exe/main",
	} {
		if got := resolveHookBinary(exe, nil); got != "rgt" {
			t.Errorf("resolveHookBinary(%q) = %q, want \"rgt\"; embedding this path would write a hook that never fires", exe, got)
		}
	}
}

func TestResolveHookBinaryFallsBackWhenLookupFails(t *testing.T) {
	if got := resolveHookBinary("", errors.New("no executable")); got != "rgt" {
		t.Errorf("resolveHookBinary = %q, want \"rgt\" when the path cannot be determined", got)
	}
}

// Found by running the installer against a real server, which no test did.
//
// connectWireHooks wired Claude and nothing else, so `rgt setup` left Codex,
// OpenCode and Pi unwired. That was survivable until the installer started
// verifying its own work: doctor correctly reports the missing Codex hook, the
// installer treats that as failure, and the paste dies on any machine where a
// second agent is installed. Partial wiring became a hard failure.
//
// The team path must wire every detected agent, exactly as init does.
func TestConnectWiresEveryDetectedAgentNotJustClaude(t *testing.T) {
	root := t.TempDir()
	// runConnect creates .regent/ before wiring; this test starts after that.
	mustMkdir(t, filepath.Join(root, ".regent"))
	// Both agents are present in this project.
	mustMkdir(t, filepath.Join(root, ".claude"))
	mustMkdir(t, filepath.Join(root, ".codex"))

	if err := connectWireHooks(root); err != nil {
		t.Fatalf("connectWireHooks: %v", err)
	}

	for _, rel := range []string{
		filepath.Join(".claude", "settings.json"),
		filepath.Join(".codex", "config.toml"),
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("%s not wired by connect: %v", rel, err)
		}
	}

	// The check the installer actually runs must agree.
	if findings := diagnose(root); !allOK(findings) {
		for _, f := range findings {
			if !f.OK {
				t.Errorf("doctor would fail the install: %s — %s", f.Name, f.Detail)
			}
		}
	}
}

// This is the guard for the risk that made the absolute-path change dangerous
// rather than trivial.
//
// capture.IsRegentCommand decides whether a command found in an agent's config
// is one of ours — it drives hook detection, rgt doctor, and the merge logic
// that avoids duplicating hooks. If switching to an absolute path stopped those
// commands being recognised, capture would keep exiting 0 while silently
// recording nothing: precisely the failure this branch exists to remove,
// re-introduced by the fix for it.
func TestHookCommandsStayRecognisableWithAnAbsoluteBinary(t *testing.T) {
	for _, binary := range []string{
		"rgt",
		"/usr/local/bin/rgt",
		"/Users/someone/.local/bin/rgt",
		"/opt/homebrew/bin/regent",
	} {
		for _, args := range []string{"message-hook user", "message-hook assistant", "tool-batch-hook", "codex-hook"} {
			cmd := hookCommandWith(binary, args)
			if !capture.IsRegentCommand(cmd) {
				t.Errorf("capture would not recognise %q as a re_gent hook; capture dies silently", cmd)
			}
		}
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

// One broken agent must not cost the user the others.
//
// wireAgents walks its targets in order and returns at the first failure, so a
// project carrying a stale `.codex` file gets no Claude hooks either — and the
// user is told only about Codex. Which agent survives depends on the order
// resolveAgentTargets happened to produce, which is not something a user can
// see or reason about.
//
// The dispatcher is meant to be the one place that decides what gets wired and
// what gets reported. Deciding "nothing further" on behalf of unrelated agents
// is not that. Every requested agent is attempted, the failures are surfaced
// together, and the summary lists exactly the ones that were written.
func TestWireAgentsContinuesPastAFailedAgent(t *testing.T) {
	root := t.TempDir()

	// Block Codex, and request it first, so a failure there is what stands
	// between the user and their Claude hooks.
	if err := os.WriteFile(filepath.Join(root, ".codex"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}

	installed, err := wireAgents(root, []agentTarget{agentCodex, agentClaude})
	if err == nil {
		t.Fatal("wireAgents returned nil error despite Codex being unwritable")
	}

	if !hasAgent(installed, agentClaude) {
		t.Errorf("installed = %v; Claude was requested and writable, and was skipped because an earlier agent failed", installed)
	}
	if hasAgent(installed, agentCodex) {
		t.Error("installed claims codex, but its write failed")
	}

	// The error has to name the agent that failed — a user reading it needs to
	// know which config to fix, not merely that something did not work.
	if !strings.Contains(err.Error(), "Codex") {
		t.Errorf("error does not name the failing agent: %v", err)
	}

	// And the wiring it did do must be real, not merely reported.
	if _, statErr := os.Stat(filepath.Join(root, ".claude", "settings.json")); statErr != nil {
		t.Errorf("Claude reported installed but settings.json is absent: %v", statErr)
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
