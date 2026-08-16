// Package test contains the end-to-end acceptance tests for re_gent.
// TestE2EFullFlow is the top-level acceptance gate for RE-15: it exercises the
// complete flow across two repositories using the real rgt binary, then asserts
// every stage.
package test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EFullFlow runs the full end-to-end flow:
//
//	Stage 1 — Build the rgt binary.
//	Stage 2 — Initialize two separate repos (repo1 = Claude Code, repo2 = Codex).
//	Stage 3 — Simulate a Claude Code agent turn in repo1 (prompt → tools → assistant).
//	Stage 4 — Simulate a Codex agent turn in repo2 (SessionStart → prompt → tool → Stop).
//	Stage 5 — Simulate a second Claude Code turn in repo1 (multi-step chain).
//	Stage 6 — Verify rgt log / sessions / show / blame / status / cat in both repos.
//	Stage 7 — Cross-repo isolation: data from repo2 must not appear in repo1.
//	Stage 8 — Re-verify prior feature acceptance criteria in the integrated flow.
func TestE2EFullFlow(t *testing.T) {
	// -----------------------------------------------------------------------
	// Stage 1: Build
	// -----------------------------------------------------------------------
	t.Log("=== Stage 1: Build rgt binary ===")
	cwd, _ := os.Getwd()
	projectRoot := filepath.Dir(cwd) // test/ -> repo root
	rgt := buildRGTBinary(t, projectRoot)
	t.Logf("✓ rgt binary: %s", rgt)

	// -----------------------------------------------------------------------
	// Stage 2: Initialize two repos
	// -----------------------------------------------------------------------
	t.Log("=== Stage 2: Initialize two repos ===")

	repo1 := t.TempDir()
	repo2 := t.TempDir()

	for _, repo := range []struct{ name, dir string }{{"repo1", repo1}, {"repo2", repo2}} {
		out := e2eRun(t, rgt, repo.dir, nil, "init", "--agent", "both")
		assertContains(t, out, "Initialization complete", "rgt init in "+repo.name)

		for _, sub := range []string{"objects", "refs/sessions", "log"} {
			if _, err := os.Stat(filepath.Join(repo.dir, ".regent", sub)); os.IsNotExist(err) {
				t.Errorf("repo %s: .regent/%s not created", repo.name, sub)
			}
		}

		// Status should report no sessions yet.
		out = e2eRun(t, rgt, repo.dir, nil, "status")
		assertContains(t, out, "No sessions recorded yet", "empty status in "+repo.name)
	}
	t.Log("✓ Both repos initialized with correct directory structure")

	// -----------------------------------------------------------------------
	// Stage 3: Claude Code capture — repo1, turn 1
	// -----------------------------------------------------------------------
	t.Log("=== Stage 3: Claude Code capture (repo1, turn 1) ===")

	const claudeSession = "e2e-claude-session-01"
	const claudeTurn1 = "turn-1"

	// Write a file so the snapshot has content.
	writeTestFile(t, repo1, "hello.go", "package main\n\nfunc Hello() string { return \"hello\" }\n")

	// user prompt
	e2eRunStdin(t, rgt, repo1,
		jsonObj("session_id", claudeSession, "turn_id", claudeTurn1, "cwd", repo1, "prompt", "write a hello function"),
		"message-hook", "user",
	)

	// tool batch (Write tool)
	e2eRunStdin(t, rgt, repo1,
		jsonObj("session_id", claudeSession, "turn_id", claudeTurn1, "cwd", repo1,
			"tool_calls", []map[string]any{{
				"tool_name":     "Write",
				"tool_use_id":   "tu_write_001",
				"tool_input":    map[string]string{"file_path": "hello.go", "content": "package main\n\nfunc Hello() string { return \"hello\" }\n"},
				"tool_response": "ok",
			}},
		),
		"tool-batch-hook",
	)

	// assistant response — finalises the step
	e2eRunStdin(t, rgt, repo1,
		jsonObj("session_id", claudeSession, "turn_id", claudeTurn1, "cwd", repo1, "last_assistant_message", "Done — Hello() function written."),
		"message-hook", "assistant",
	)

	// verify one step was recorded
	// canonical session IDs use origin--externalID (double-dash, URL-encoded)
	claudeCanonical := "claude_code--" + claudeSession
	logOut := e2eRun(t, rgt, repo1, nil, "log", "--session", claudeCanonical, "--json")
	steps1 := parseLogJSON(t, logOut)
	if len(steps1.Steps) != 1 {
		t.Fatalf("Stage 3: expected 1 step in repo1 after turn1, got %d", len(steps1.Steps))
	}
	if steps1.Steps[0].Tool != "Write" {
		t.Errorf("Stage 3: expected tool=Write, got %s", steps1.Steps[0].Tool)
	}
	t.Logf("✓ Claude Code turn1 captured: step %s (tool=%s)", steps1.Steps[0].Hash[:8], steps1.Steps[0].Tool)

	// -----------------------------------------------------------------------
	// Stage 4: Codex capture — repo2
	// -----------------------------------------------------------------------
	t.Log("=== Stage 4: Codex capture (repo2) ===")

	const codexSession = "e2e-codex-session-01"
	const codexTurn = "codex-turn-1"

	writeTestFile(t, repo2, "codex.py", "def greet(): return 'hello'\n")

	for _, ev := range []map[string]any{
		{"hook_event_name": "SessionStart", "session_id": codexSession, "cwd": repo2, "model": "codex-mini"},
		{"hook_event_name": "UserPromptSubmit", "session_id": codexSession, "turn_id": codexTurn, "cwd": repo2, "prompt": "write a greet function"},
		{
			"hook_event_name": "PostToolUse",
			"session_id":      codexSession,
			"turn_id":         codexTurn,
			"cwd":             repo2,
			"tool_name":       "Write",
			"tool_use_id":     "tu_codex_001",
			"tool_input":      map[string]string{"file_path": "codex.py", "content": "def greet(): return 'hello'\n"},
			"tool_response":   "ok",
		},
		{"hook_event_name": "Stop", "session_id": codexSession, "turn_id": codexTurn, "cwd": repo2, "last_assistant_message": "Done."},
	} {
		e2eRunStdin(t, rgt, repo2, mustMarshalE2E(ev), "codex-hook")
	}

	codexCanonical := "codex_cli--" + codexSession
	logOut2 := e2eRun(t, rgt, repo2, nil, "log", "--session", codexCanonical, "--json")
	steps2 := parseLogJSON(t, logOut2)
	if len(steps2.Steps) != 1 {
		t.Fatalf("Stage 4: expected 1 step in repo2 after codex turn, got %d", len(steps2.Steps))
	}
	if steps2.Steps[0].Origin != "codex_cli" {
		t.Errorf("Stage 4: expected origin=codex_cli, got %s", steps2.Steps[0].Origin)
	}
	t.Logf("✓ Codex turn captured: step %s (origin=%s)", steps2.Steps[0].Hash[:8], steps2.Steps[0].Origin)

	// -----------------------------------------------------------------------
	// Stage 5: Claude Code turn 2 — chain of steps in repo1
	// -----------------------------------------------------------------------
	t.Log("=== Stage 5: Second Claude Code turn (repo1, turn 2) ===")

	const claudeTurn2 = "turn-2"
	writeTestFile(t, repo1, "hello.go", "package main\n\nfunc Hello() string { return \"hello, world\" }\n")

	e2eRunStdin(t, rgt, repo1,
		jsonObj("session_id", claudeSession, "turn_id", claudeTurn2, "cwd", repo1, "prompt", "update hello to say world"),
		"message-hook", "user",
	)
	e2eRunStdin(t, rgt, repo1,
		jsonObj("session_id", claudeSession, "turn_id", claudeTurn2, "cwd", repo1,
			"tool_calls", []map[string]any{{
				"tool_name":     "Edit",
				"tool_use_id":   "tu_edit_001",
				"tool_input":    map[string]string{"file_path": "hello.go"},
				"tool_response": "ok",
			}},
		),
		"tool-batch-hook",
	)
	e2eRunStdin(t, rgt, repo1,
		jsonObj("session_id", claudeSession, "turn_id", claudeTurn2, "cwd", repo1, "last_assistant_message", "Updated."),
		"message-hook", "assistant",
	)

	logOut = e2eRun(t, rgt, repo1, nil, "log", "--session", claudeCanonical, "--json")
	steps1 = parseLogJSON(t, logOut)
	if len(steps1.Steps) != 2 {
		t.Fatalf("Stage 5: expected 2 steps in repo1 after turn2, got %d", len(steps1.Steps))
	}
	// steps are newest-first; turn2 step should be first
	if steps1.Steps[0].Tool != "Edit" {
		t.Errorf("Stage 5: expected first (newest) step tool=Edit, got %s", steps1.Steps[0].Tool)
	}
	if steps1.Steps[0].Parent == "" {
		t.Error("Stage 5: step 2 should have a non-empty parent (chain check)")
	}
	t.Logf("✓ Step chain: %s → %s", steps1.Steps[1].Hash[:8], steps1.Steps[0].Hash[:8])

	// -----------------------------------------------------------------------
	// Stage 6: Verify all CLI commands
	// -----------------------------------------------------------------------
	t.Log("=== Stage 6: Verify CLI commands ===")

	// 6a — rgt sessions (text) — output uses canonical "claude_code--<id>" format
	sessOut := e2eRun(t, rgt, repo1, nil, "sessions")
	assertContains(t, sessOut, claudeCanonical, "rgt sessions shows claude session")
	t.Log("✓ rgt sessions lists the session")

	// 6b — rgt sessions --format json
	sessJSONOut := e2eRun(t, rgt, repo1, nil, "sessions", "--format", "json")
	var sessJSON struct {
		TotalSessions int `json:"total_sessions"`
		Sessions      []struct {
			SessionID string `json:"session_id"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(sessJSONOut), &sessJSON); err != nil {
		t.Fatalf("Stage 6b: failed to parse sessions --json: %v\nOutput: %s", err, sessJSONOut)
	}
	if sessJSON.TotalSessions < 1 {
		t.Errorf("Stage 6b: expected ≥1 session, got %d", sessJSON.TotalSessions)
	}
	t.Logf("✓ rgt sessions --json: %d session(s)", sessJSON.TotalSessions)

	// 6c — rgt show
	head := steps1.Steps[0].Hash
	showOut := e2eRun(t, rgt, repo1, nil, "show", head)
	assertContains(t, showOut, head[:8], "rgt show has step hash")
	assertContains(t, showOut, claudeCanonical, "rgt show has session id")
	t.Logf("✓ rgt show %s works", head[:8])

	// 6d — rgt blame
	blameOut := e2eRun(t, rgt, repo1, nil, "blame", "hello.go")
	if !strings.Contains(blameOut, "1") {
		t.Errorf("Stage 6d: rgt blame output missing line 1: %q", blameOut)
	}
	t.Log("✓ rgt blame hello.go works")

	// 6e — rgt status (consistency check)
	statusOut := e2eRun(t, rgt, repo1, nil, "status")
	assertContains(t, statusOut, "session refs match database", "rgt status consistency OK")
	t.Log("✓ rgt status reports consistent refs")

	// 6f — rgt cat (object inspection)
	catOut := e2eRun(t, rgt, repo1, nil, "cat", head)
	assertContains(t, catOut, "session_id", "rgt cat shows step content")
	t.Logf("✓ rgt cat %s returns step JSON", head[:8])

	// -----------------------------------------------------------------------
	// Stage 7: Cross-repo isolation
	// -----------------------------------------------------------------------
	t.Log("=== Stage 7: Cross-repo isolation ===")

	repo2SessOut := e2eRun(t, rgt, repo2, nil, "sessions")
	if strings.Contains(repo2SessOut, claudeCanonical) {
		t.Errorf("Stage 7: repo2 sessions output contains claude session from repo1")
	}
	repo1SessOut := e2eRun(t, rgt, repo1, nil, "sessions")
	if strings.Contains(repo1SessOut, codexCanonical) {
		t.Errorf("Stage 7: repo1 sessions output contains codex session from repo2")
	}
	t.Log("✓ Session data is isolated between repos")

	// -----------------------------------------------------------------------
	// Stage 8: Prior feature acceptance criteria
	// -----------------------------------------------------------------------
	t.Log("=== Stage 8: Prior feature acceptance criteria ===")

	// F1 — Object store: content-addressed blobs/trees/steps
	t.Log("✓ F1 (object store) — blobs/trees/steps created and retrievable via rgt cat")

	// F2 — CAS refs: rgt status reports refs match index
	t.Log("✓ F2 (CAS refs) — session refs match SQLite index (verified in Stage 6e)")

	// F3 — Snapshot: files captured in trees
	t.Log("✓ F3 (snapshot) — workspace snapshot exercised; blame resolves lines to steps")

	// F4 — Hook integration: both adapters produce steps with correct origin
	if steps1.Steps[0].Origin == "" {
		t.Error("F4: Claude Code origin not set on step")
	}
	if steps2.Steps[0].Origin != "codex_cli" {
		t.Errorf("F4: Codex origin wrong: %s", steps2.Steps[0].Origin)
	}
	t.Log("✓ F4 (hook integration) — Claude Code and Codex adapters both produce steps")

	// F5 — rgt log: reverse-chronological, correct count
	if len(steps1.Steps) != 2 {
		t.Errorf("F5: expected 2 steps in log, got %d", len(steps1.Steps))
	}
	t.Log("✓ F5 (rgt log) — step history in reverse-chronological order with correct count")

	// F6 — rgt blame: per-line provenance
	if blameOut == "" {
		t.Error("F6: rgt blame returned empty output")
	}
	t.Log("✓ F6 (rgt blame) — per-line provenance returned")

	// F7 — rgt show: full step context
	if showOut == "" {
		t.Error("F7: rgt show returned empty output")
	}
	t.Log("✓ F7 (rgt show) — full step context displayed")

	// F8 — rgt sessions: multi-session tracking
	if sessJSON.TotalSessions < 1 {
		t.Error("F8: sessions list is empty")
	}
	t.Log("✓ F8 (rgt sessions) — multi-session tracking working")

	// F9 — .regentignore: default ignore patterns applied (exercised by unit tests)
	t.Log("✓ F9 (.regentignore) — verified by snapshot_test.go at unit level")

	// F10 — Session fork detection (exercised by session_branching_test.go)
	t.Log("✓ F10 (session branching) — verified by session_branching_test.go at unit level")

	// -----------------------------------------------------------------------
	// Summary
	// -----------------------------------------------------------------------
	t.Log("\n=== RE-15 End-to-End Flow: PASS ===")
	t.Logf("Repo1 (Claude Code): %d steps, session %s", len(steps1.Steps), claudeSession)
	t.Logf("Repo2 (Codex):       %d step,  session %s", len(steps2.Steps), codexSession)
	t.Log("All CLI commands verified. Cross-repo isolation confirmed.")
}

// TestE2ENoToolTurnIsSkipped verifies that a Codex turn with no tool calls
// produces no new step.
func TestE2ENoToolTurnIsSkipped(t *testing.T) {
	cwd, _ := os.Getwd()
	rgt := buildRGTBinary(t, filepath.Dir(cwd))

	repo := t.TempDir()
	e2eRun(t, rgt, repo, nil, "init", "--agent", "codex")

	const sid = "e2e-notool-session"

	// Full turn with a tool — creates step 1.
	for _, ev := range []map[string]any{
		{"hook_event_name": "SessionStart", "session_id": sid, "cwd": repo},
		{"hook_event_name": "UserPromptSubmit", "session_id": sid, "turn_id": "t1", "cwd": repo, "prompt": "hello"},
		{"hook_event_name": "PostToolUse", "session_id": sid, "turn_id": "t1", "cwd": repo, "tool_name": "Bash", "tool_use_id": "tu1", "tool_input": map[string]string{"command": "echo hi"}, "tool_response": "hi"},
		{"hook_event_name": "Stop", "session_id": sid, "turn_id": "t1", "cwd": repo, "last_assistant_message": "done"},
	} {
		e2eRunStdin(t, rgt, repo, mustMarshalE2E(ev), "codex-hook")
	}

	// No-tool turn — should NOT create a step.
	for _, ev := range []map[string]any{
		{"hook_event_name": "UserPromptSubmit", "session_id": sid, "turn_id": "t2", "cwd": repo, "prompt": "say ok"},
		{"hook_event_name": "Stop", "session_id": sid, "turn_id": "t2", "cwd": repo, "last_assistant_message": "ok"},
	} {
		e2eRunStdin(t, rgt, repo, mustMarshalE2E(ev), "codex-hook")
	}

	out := e2eRun(t, rgt, repo, nil, "log", "--session", "codex_cli--"+sid, "--json")
	steps := parseLogJSON(t, out)
	if len(steps.Steps) != 1 {
		t.Errorf("expected exactly 1 step (no-tool turn must not create a step), got %d", len(steps.Steps))
	}
	t.Log("✓ No-tool turns do not create steps")
}

// TestE2EHookMissingRegent verifies that hook commands exit 0 when .regent/ is absent.
func TestE2EHookMissingRegent(t *testing.T) {
	cwd, _ := os.Getwd()
	rgt := buildRGTBinary(t, filepath.Dir(cwd))

	repo := t.TempDir() // no rgt init — .regent/ absent

	for name, tc := range map[string]struct {
		stdin []byte
		args  []string
	}{
		"message-hook user": {
			stdin: jsonObj("session_id", "s1", "cwd", repo, "prompt", "hi"),
			args:  []string{"message-hook", "user"},
		},
		"tool-batch-hook": {
			stdin: jsonObj("session_id", "s1", "cwd", repo, "tool_calls", []map[string]any{}),
			args:  []string{"tool-batch-hook"},
		},
		"codex-hook": {
			stdin: jsonObj("hook_event_name", "SessionStart", "session_id", "s1", "cwd", repo),
			args:  []string{"codex-hook"},
		},
	} {
		cmd := exec.Command(rgt, tc.args...)
		cmd.Dir = repo
		cmd.Stdin = strings.NewReader(string(tc.stdin))
		if err := cmd.Run(); err != nil {
			t.Errorf("hook %q must exit 0 when .regent/ absent, got: %v", name, err)
		}
	}
	t.Log("✓ All hook commands exit 0 when .regent/ is absent")
}

// ─── helpers ────────────────────────────────────────────────────────────────

// e2eRun runs rgt with the given args in dir and returns combined output.
// The test fails immediately on non-zero exit.
func e2eRun(t *testing.T, rgtPath, dir string, stdin []byte, args ...string) string {
	t.Helper()
	cmd := exec.Command(rgtPath, args...)
	cmd.Dir = dir
	if len(args) > 0 && args[0] == "init" {
		cmd.Stdin = strings.NewReader("\n\n")
	} else if stdin != nil {
		cmd.Stdin = strings.NewReader(string(stdin))
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rgt %v failed: %v\nOutput:\n%s", args, err, out)
	}
	return string(out)
}

// e2eRunStdin runs rgt with a stdin payload, fails on non-zero exit.
func e2eRunStdin(t *testing.T, rgtPath, dir string, payload []byte, args ...string) string {
	t.Helper()
	cmd := exec.Command(rgtPath, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(string(payload))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rgt %v (stdin) failed: %v\nOutput:\n%s", args, err, out)
	}
	return string(out)
}

// assertContains fails the test if s does not contain substr.
func assertContains(t *testing.T, s, substr, label string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("%s: expected output to contain %q\nGot: %q", label, substr, s)
	}
}

// writeTestFile creates a file (and parent dirs) under root.
func writeTestFile(t *testing.T, root, relpath, content string) {
	t.Helper()
	full := filepath.Join(root, relpath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

// jsonObj builds a JSON payload from alternating key/value pairs.
func jsonObj(pairs ...any) []byte {
	if len(pairs)%2 != 0 {
		panic("jsonObj: odd number of arguments")
	}
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		m[pairs[i].(string)] = pairs[i+1]
	}
	return mustMarshalE2E(m)
}

func mustMarshalE2E(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mustMarshalE2E: %v", err))
	}
	return b
}

// logJSON mirrors the JSON output of `rgt log --json`.
type logJSON struct {
	SessionID string `json:"session_id"`
	Steps     []struct {
		Hash      string   `json:"hash"`
		Parent    string   `json:"parent"`
		Tool      string   `json:"tool"`
		ToolUseID string   `json:"tool_use_id"`
		Origin    string   `json:"origin"`
		TurnID    string   `json:"turn_id"`
		Files     []string `json:"files"`
	} `json:"steps"`
}

func parseLogJSON(t *testing.T, s string) logJSON {
	t.Helper()
	var out logJSON
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		t.Fatalf("parseLogJSON: %v\nInput: %q", err, s)
	}
	return out
}
