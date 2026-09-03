package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/store"
)

func TestRunPiHook_CapturesTurn(t *testing.T) {
	root := t.TempDir()
	if _, err := store.Init(root); err != nil {
		t.Fatalf("init store: %v", err)
	}

	sessionRawID := "pi/session:1"
	runPiPayload(t, fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":%q,"cwd":%q,"model":"anthropic/claude-sonnet-4-5"}`, sessionRawID, root))
	runPiPayload(t, fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":%q,"turn_id":"turn-1","cwd":%q,"prompt":"write file"}`, sessionRawID, root))
	if err := os.WriteFile(filepath.Join(root, "pi.txt"), []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runPiPayload(t, fmt.Sprintf(`{"hook_event_name":"PostToolUse","session_id":%q,"turn_id":"turn-1","cwd":%q,"tool_name":"write","tool_use_id":"tool-1","tool_input":{"file_path":"pi.txt","content":"ok\n","nested":{"keep":[1,true]}},"tool_response":{"content":[{"type":"text","text":"wrote pi.txt"}],"details":{"bytes":3},"isError":false}}`, sessionRawID, root))
	runPiPayload(t, fmt.Sprintf(`{"hook_event_name":"Stop","session_id":%q,"turn_id":"turn-1","cwd":%q,"last_assistant_message":"done"}`, sessionRawID, root))

	s, err := store.Open(filepath.Join(root, ".regent"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	idx, err := index.Open(s)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer func() { _ = idx.Close() }()

	sessionID := "pi--" + url.QueryEscape(sessionRawID)
	steps, err := idx.ListSteps(sessionID, 10)
	if err != nil {
		t.Fatalf("list steps: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("expected one step, got %d", len(steps))
	}
	if steps[0].Origin != "pi" || steps[0].TurnID != "turn-1" || steps[0].ToolName != "write" {
		t.Fatalf("unexpected step metadata: %#v", steps[0])
	}

	messages, err := idx.GetMessagesForStep(steps[0].Hash)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(messages) != 4 {
		t.Fatalf("expected 4 linked messages, got %d", len(messages))
	}
	userMessage := messageByType(t, messages, "user")
	if userMessage.ContentText != "write file" {
		t.Fatalf("prompt = %q", userMessage.ContentText)
	}
	assistantMessage := messageByType(t, messages, "assistant")
	if assistantMessage.ContentText != "done" {
		t.Fatalf("assistant = %q", assistantMessage.ContentText)
	}

	toolCall := messageByType(t, messages, "tool_call")
	inputData, err := s.ReadBlob(store.Hash(toolCall.ToolInput))
	if err != nil {
		t.Fatalf("read tool input: %v", err)
	}
	wantInput := `{"file_path":"pi.txt","content":"ok\n","nested":{"keep":[1,true]}}`
	if string(inputData) != wantInput {
		t.Fatalf("tool input blob = %s, want %s", inputData, wantInput)
	}

	toolResult := messageByType(t, messages, "tool_result")
	resultData, err := s.ReadBlob(store.Hash(toolResult.ToolOutput))
	if err != nil {
		t.Fatalf("read tool response: %v", err)
	}
	wantResult := `{"content":[{"type":"text","text":"wrote pi.txt"}],"details":{"bytes":3},"isError":false}`
	if string(resultData) != wantResult {
		t.Fatalf("tool response blob = %s, want %s", resultData, wantResult)
	}
}

func TestRunPiHook_UnsupportedEventLogsAndDoesNotFail(t *testing.T) {
	root := t.TempDir()
	if _, err := store.Init(root); err != nil {
		t.Fatalf("init store: %v", err)
	}

	runPiPayload(t, fmt.Sprintf(`{"hook_event_name":"unknown_event","session_id":"pi-session","cwd":%q}`, root))

	data, err := os.ReadFile(filepath.Join(root, ".regent", "log", "hook-error.log"))
	if err != nil {
		t.Fatalf("expected hook error log: %v", err)
	}
	if !strings.Contains(string(data), "unsupported Pi hook event") {
		t.Fatalf("unexpected hook error log: %s", data)
	}
}

func TestRunPiHook_NoStoreIsNoop(t *testing.T) {
	root := t.TempDir()

	runPiPayload(t, fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":"pi-session","cwd":%q}`, root))

	if _, err := os.Stat(filepath.Join(root, ".regent")); !os.IsNotExist(err) {
		t.Fatalf("hook should not create .regent, stat err=%v", err)
	}
}

func TestRunPiHook_InvalidPayloadNoStoreIsNoop(t *testing.T) {
	root := t.TempDir()
	chdir(t, root)

	runPiPayload(t, `{not-json`)

	if _, err := os.Stat(filepath.Join(root, ".regent")); !os.IsNotExist(err) {
		t.Fatalf("invalid hook payload should not create .regent, stat err=%v", err)
	}
}

func TestRunPiHook_InvalidPayloadLogsWhenStoreExists(t *testing.T) {
	root := t.TempDir()
	chdir(t, root)
	if _, err := store.Init(root); err != nil {
		t.Fatalf("init store: %v", err)
	}

	runPiPayload(t, `{not-json`)

	data, err := os.ReadFile(filepath.Join(root, ".regent", "log", "hook-error.log"))
	if err != nil {
		t.Fatalf("expected hook error log: %v", err)
	}
	if !strings.Contains(string(data), "parse payload") {
		t.Fatalf("unexpected hook error log: %s", data)
	}
}

func TestRunPiHook_MissingTurnIDLogsAndDoesNotFail(t *testing.T) {
	root := t.TempDir()
	if _, err := store.Init(root); err != nil {
		t.Fatalf("init store: %v", err)
	}

	runPiPayload(t, fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":"pi-session","cwd":%q,"prompt":"missing turn"}`, root))

	data, err := os.ReadFile(filepath.Join(root, ".regent", "log", "hook-error.log"))
	if err != nil {
		t.Fatalf("expected hook error log: %v", err)
	}
	if !strings.Contains(string(data), "turn id is required") {
		t.Fatalf("unexpected hook error log: %s", data)
	}
}

func runPiPayload(t *testing.T, payload string) {
	t.Helper()
	runWithStdin(t, payload, func() error {
		return runPiHook(nil, nil)
	})
}

func messageByType(t *testing.T, messages []index.Message, messageType string) index.Message {
	t.Helper()
	for _, message := range messages {
		if message.MessageType == messageType {
			return message
		}
	}
	t.Fatalf("message type %q not found in %#v", messageType, messages)
	return index.Message{}
}
