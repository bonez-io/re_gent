package capture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/jsonl"
	"github.com/bonez-io/re_gent/internal/store"
)

// assistantLine builds a Claude Code transcript record carrying one thinking
// block followed by one text block.
func assistantLine(uuid, thinking, text string) string {
	return `{"uuid":"` + uuid + `","type":"assistant","message":{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"` + thinking + `"},` +
		`{"type":"text","text":"` + text + `"},` +
		`{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}]}}`
}

func newTestRecorder(t *testing.T) (*Recorder, string) {
	t.Helper()

	root := t.TempDir()
	if _, err := store.Init(root); err != nil {
		t.Fatalf("init store: %v", err)
	}
	recorder, ok, err := Open(root)
	if err != nil {
		t.Fatalf("open recorder: %v", err)
	}
	if !ok {
		t.Fatal("expected initialized recorder")
	}
	t.Cleanup(func() { _ = recorder.Close() })

	return recorder, root
}

// runAssistantTurn drives one full turn: prompt, a tool call, then Stop.
func runAssistantTurn(t *testing.T, recorder *Recorder, meta SessionMetadata, turnID, prompt, toolUseID, finalMessage string) {
	t.Helper()

	if err := recorder.RecordUserPrompt(UserPrompt{SessionMetadata: meta, TurnID: turnID, Prompt: prompt}); err != nil {
		t.Fatalf("record prompt: %v", err)
	}
	if err := recorder.RecordToolUse(ToolUse{
		SessionMetadata: meta,
		TurnID:          turnID,
		ToolName:        "Bash",
		ToolUseID:       toolUseID,
		ToolInput:       json.RawMessage(`{"command":"ls"}`),
		ToolResponse:    json.RawMessage(`"ok"`),
	}); err != nil {
		t.Fatalf("record tool: %v", err)
	}
	if err := recorder.RecordAssistantAndFinalize(AssistantResponse{
		SessionMetadata:      meta,
		TurnID:               turnID,
		LastAssistantMessage: finalMessage,
	}); err != nil {
		t.Fatalf("finalize: %v", err)
	}
}

// sessionMessages returns every message linked to a step of the session, oldest
// step first, preserving each step's own message order.
func sessionMessages(t *testing.T, recorder *Recorder, sessionID string) []index.Message {
	t.Helper()

	steps, err := recorder.Index.ListSteps(sessionID, 100)
	if err != nil {
		t.Fatalf("list steps: %v", err)
	}

	var messages []index.Message
	for i := len(steps) - 1; i >= 0; i-- {
		stepMessages, err := recorder.Index.GetMessagesForStep(steps[i].Hash)
		if err != nil {
			t.Fatalf("read messages for step: %v", err)
		}
		messages = append(messages, stepMessages...)
	}
	return messages
}

func textsOfType(messages []index.Message, messageType string) []string {
	var texts []string
	for _, msg := range messages {
		if msg.MessageType == messageType {
			texts = append(texts, msg.ContentText)
		}
	}
	return texts
}

func countText(texts []string, want string) int {
	count := 0
	for _, text := range texts {
		if text == want {
			count++
		}
	}
	return count
}

func TestRecorder_CapturesAssistantReasoningFromTranscript(t *testing.T) {
	recorder, root := newTestRecorder(t)

	transcriptPath := filepath.Join(t.TempDir(), "session.jsonl")
	writeTranscriptFile(t, transcriptPath,
		`{"uuid":"u0","type":"user","message":{"role":"user","content":"list files"}}`,
		assistantLine("a1", "The user wants a listing; ls is enough.", "Listing the directory."),
	)

	meta := SessionMetadata{SessionID: "sess-1", Origin: OriginClaudeCode, TranscriptPath: transcriptPath}
	sessionID := canonicalSessionID(OriginClaudeCode, meta.SessionID)

	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
	runAssistantTurn(t, recorder, meta, "", "list files", "toolu_1", "Listing the directory.")

	messages := sessionMessages(t, recorder, sessionID)

	reasoning := textsOfType(messages, "reasoning")
	if len(reasoning) != 1 || reasoning[0] != "The user wants a listing; ls is enough." {
		t.Fatalf("reasoning messages = %#v, want the transcript thinking block", reasoning)
	}

	assistant := textsOfType(messages, "assistant")
	if countText(assistant, "Listing the directory.") != 1 {
		t.Fatalf("assistant messages = %#v, want the text captured exactly once", assistant)
	}

	// Reasoning must precede the tool call it explains.
	reasoningPos, toolPos := -1, -1
	for i, msg := range messages {
		if msg.MessageType == "reasoning" && reasoningPos == -1 {
			reasoningPos = i
		}
		if msg.MessageType == "tool_call" && toolPos == -1 {
			toolPos = i
		}
	}
	if reasoningPos == -1 || toolPos == -1 || reasoningPos > toolPos {
		t.Fatalf("reasoning at %d, tool call at %d: reasoning should come first", reasoningPos, toolPos)
	}
}

// The transcript is rescanned on every tool batch and again at Stop, so an
// unchanged (or merely appended-to) transcript must never duplicate a block.
func TestRecorder_TranscriptCaptureIsIdempotent(t *testing.T) {
	recorder, _ := newTestRecorder(t)

	transcriptPath := filepath.Join(t.TempDir(), "session.jsonl")
	turnOne := assistantLine("a1", "First thought.", "First reply.")
	writeTranscriptFile(t, transcriptPath, turnOne)

	meta := SessionMetadata{SessionID: "sess-2", Origin: OriginClaudeCode, TranscriptPath: transcriptPath}
	sessionID := canonicalSessionID(OriginClaudeCode, meta.SessionID)

	runAssistantTurn(t, recorder, meta, "", "first", "toolu_1", "First reply.")

	// Claude Code appends the next turn to the same file.
	writeTranscriptFile(t, transcriptPath, turnOne, assistantLine("a2", "Second thought.", "Second reply."))
	runAssistantTurn(t, recorder, meta, "", "second", "toolu_2", "Second reply.")

	messages := sessionMessages(t, recorder, sessionID)
	reasoning := textsOfType(messages, "reasoning")
	assistant := textsOfType(messages, "assistant")

	for _, want := range []string{"First thought.", "Second thought."} {
		if got := countText(reasoning, want); got != 1 {
			t.Errorf("reasoning %q recorded %d times, want 1 (all: %#v)", want, got, reasoning)
		}
	}
	for _, want := range []string{"First reply.", "Second reply."} {
		if got := countText(assistant, want); got != 1 {
			t.Errorf("assistant %q recorded %d times, want 1 (all: %#v)", want, got, assistant)
		}
	}
}

// /compact rewrites the transcript to a summary, discarding the earlier records.
// Everything captured before the rewrite must still be there afterwards.
func TestRecorder_TranscriptCaptureSurvivesCompact(t *testing.T) {
	recorder, _ := newTestRecorder(t)

	transcriptPath := filepath.Join(t.TempDir(), "session.jsonl")
	writeTranscriptFile(t, transcriptPath, assistantLine("a1", "Pre-compact reasoning.", "Pre-compact reply."))

	meta := SessionMetadata{SessionID: "sess-3", Origin: OriginClaudeCode, TranscriptPath: transcriptPath}
	sessionID := canonicalSessionID(OriginClaudeCode, meta.SessionID)

	runAssistantTurn(t, recorder, meta, "", "before compact", "toolu_1", "Pre-compact reply.")

	// /compact: the file is replaced by a summary plus fresh records.
	writeTranscriptFile(t, transcriptPath,
		`{"uuid":"s1","type":"summary","summary":"conversation summary"}`,
		assistantLine("a2", "Post-compact reasoning.", "Post-compact reply."),
	)
	runAssistantTurn(t, recorder, meta, "", "after compact", "toolu_2", "Post-compact reply.")

	reasoning := textsOfType(sessionMessages(t, recorder, sessionID), "reasoning")
	for _, want := range []string{"Pre-compact reasoning.", "Post-compact reasoning."} {
		if got := countText(reasoning, want); got != 1 {
			t.Errorf("reasoning %q recorded %d times, want 1 (all: %#v)", want, got, reasoning)
		}
	}
}

// /clear removes the transcript entirely. Capture must keep working and must not
// disturb what was already recorded.
func TestRecorder_TranscriptCaptureSurvivesClear(t *testing.T) {
	recorder, _ := newTestRecorder(t)

	transcriptPath := filepath.Join(t.TempDir(), "session.jsonl")
	writeTranscriptFile(t, transcriptPath, assistantLine("a1", "Reasoning before clear.", "Reply before clear."))

	meta := SessionMetadata{SessionID: "sess-4", Origin: OriginClaudeCode, TranscriptPath: transcriptPath}
	sessionID := canonicalSessionID(OriginClaudeCode, meta.SessionID)

	runAssistantTurn(t, recorder, meta, "", "before clear", "toolu_1", "Reply before clear.")

	if err := os.Remove(transcriptPath); err != nil {
		t.Fatalf("remove transcript: %v", err)
	}
	runAssistantTurn(t, recorder, meta, "", "after clear", "toolu_2", "Reply after clear.")

	messages := sessionMessages(t, recorder, sessionID)
	if got := countText(textsOfType(messages, "reasoning"), "Reasoning before clear."); got != 1 {
		t.Errorf("reasoning captured before /clear was lost (count %d)", got)
	}
	// With no transcript the hook payload is the only source, so it is used.
	if got := countText(textsOfType(messages, "assistant"), "Reply after clear."); got != 1 {
		t.Errorf("final assistant message after /clear recorded %d times, want 1", got)
	}
}

func TestRecorder_TranscriptCaptureToleratesMissingPath(t *testing.T) {
	recorder, _ := newTestRecorder(t)

	meta := SessionMetadata{SessionID: "sess-5", Origin: OriginCodexCLI, TranscriptPath: filepath.Join(t.TempDir(), "absent.jsonl")}
	sessionID := canonicalSessionID(OriginCodexCLI, meta.SessionID)

	runAssistantTurn(t, recorder, meta, "turn-1", "do it", "call_1", "done")

	if got := countText(textsOfType(sessionMessages(t, recorder, sessionID), "assistant"), "done"); got != 1 {
		t.Fatalf("assistant message recorded %d times, want 1", got)
	}
}

func TestAssistantMessageID_StableAndDistinct(t *testing.T) {
	entry := func(uuid string, index int, kind, text string) jsonl.AssistantEntry {
		return jsonl.AssistantEntry{UUID: uuid, Index: index, Kind: kind, Text: text}
	}

	base := entry("u1", 0, jsonl.KindReasoning, "thinking hard")
	baseID := assistantMessageID("s", "t", base)
	if baseID != assistantMessageID("s", "t", base) {
		t.Fatal("expected the same id for the same entry")
	}

	distinct := map[string]string{
		"different index":   assistantMessageID("s", "t", entry("u1", 1, jsonl.KindReasoning, "thinking hard")),
		"different uuid":    assistantMessageID("s", "t", entry("u2", 0, jsonl.KindReasoning, "thinking hard")),
		"different kind":    assistantMessageID("s", "t", entry("u1", 0, jsonl.KindAssistant, "thinking hard")),
		"different text":    assistantMessageID("s", "t", entry("u1", 0, jsonl.KindReasoning, "thinking harder")),
		"different session": assistantMessageID("s2", "t", base),
		"different turn":    assistantMessageID("s", "t2", base),
	}
	for name, id := range distinct {
		if id == baseID {
			t.Errorf("%s produced the same id as the base entry", name)
		}
	}
}
