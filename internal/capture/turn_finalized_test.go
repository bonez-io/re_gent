package capture

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bonez-io/re_gent/internal/store"
)

// The Stop hook tells the command edge about every finalized turn, with the
// step when the turn wrote one and without it when the agent only talked.
// An error from the edge is logged, never returned to the host.
func TestRecorder_OnTurnFinalized(t *testing.T) {
	root := t.TempDir()
	if _, err := store.Init(root); err != nil {
		t.Fatalf("init store: %v", err)
	}
	recorder, ok, err := Open(root)
	if err != nil || !ok {
		t.Fatalf("open recorder: ok=%v err=%v", ok, err)
	}
	defer func() { _ = recorder.Close() }()

	var seen []TurnFinalized
	recorder.OnTurnFinalized = func(turn TurnFinalized) error {
		seen = append(seen, turn)
		return errors.New("edge failed")
	}

	meta := SessionMetadata{SessionID: "sess-1", Origin: OriginClaudeCode}
	if err := recorder.RecordUserPrompt(UserPrompt{SessionMetadata: meta, TurnID: "talk", Prompt: "say hi"}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordAssistantAndFinalize(AssistantResponse{SessionMetadata: meta, TurnID: "talk", LastAssistantMessage: "hi"}); err != nil {
		t.Fatalf("finalize talk turn: %v", err)
	}

	if err := recorder.RecordUserPrompt(UserPrompt{SessionMetadata: meta, TurnID: "write", Prompt: "write a file"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordToolUse(ToolUse{
		SessionMetadata: meta, TurnID: "write", ToolName: "Write", ToolUseID: "tool-1",
		ToolInput: json.RawMessage(`{"file_path":"a.txt"}`), ToolResponse: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordAssistantAndFinalize(AssistantResponse{SessionMetadata: meta, TurnID: "write", LastAssistantMessage: "done"}); err != nil {
		t.Fatalf("finalize write turn: %v", err)
	}
	// A second Stop for the same turn (a recovered turn) reports the existing step.
	if err := recorder.RecordAssistantAndFinalize(AssistantResponse{SessionMetadata: meta, TurnID: "write", LastAssistantMessage: "done"}); err != nil {
		t.Fatalf("finalize write turn again: %v", err)
	}

	if len(seen) != 3 {
		t.Fatalf("expected 3 notifications, got %d: %#v", len(seen), seen)
	}
	if seen[0].TurnID != "talk" || seen[0].Step != "" {
		t.Fatalf("talk turn: %#v", seen[0])
	}
	if seen[1].TurnID != "write" || seen[1].Step == "" || seen[1].Origin != OriginClaudeCode {
		t.Fatalf("write turn: %#v", seen[1])
	}
	if seen[2].Step != seen[1].Step {
		t.Fatalf("recovered turn should report the same step: %#v vs %#v", seen[2], seen[1])
	}

	logged, err := os.ReadFile(filepath.Join(root, ".regent", "log", "hook-error.log"))
	if err != nil {
		t.Fatalf("edge error should be logged: %v", err)
	}
	if !strings.Contains(string(logged), "turn finalized hook: edge failed") {
		t.Fatalf("log:\n%s", logged)
	}
}
