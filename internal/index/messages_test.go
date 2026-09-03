package index

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bonez-io/re_gent/internal/store"
)

func newTestIndex(t *testing.T) *DB {
	t.Helper()

	s, err := store.Init(t.TempDir())
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	idx, err := Open(s)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	return idx
}

func TestAppendMessageIfNew_DedupesByIDAndAssignsSequence(t *testing.T) {
	idx := newTestIndex(t)

	const sessionID = "claude_code--sess"
	msg := Message{
		ID:          "msg_reasoning_fixed",
		SessionID:   sessionID,
		TurnID:      "turn-1",
		Timestamp:   time.Now().UnixNano(),
		MessageType: "reasoning",
		ContentText: "weighing the options",
	}

	inserted, err := idx.AppendMessageIfNew(msg)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	if !inserted {
		t.Fatal("first append should insert")
	}

	inserted, err = idx.AppendMessageIfNew(msg)
	if err != nil {
		t.Fatalf("second append: %v", err)
	}
	if inserted {
		t.Fatal("replaying the same id must not insert a second row")
	}

	second := msg
	second.ID = "msg_assistant_fixed"
	second.MessageType = "assistant"
	second.ContentText = "here is the answer"
	if _, err := idx.AppendMessageIfNew(second); err != nil {
		t.Fatalf("append second message: %v", err)
	}

	pending, err := idx.GetPendingMessages(sessionID, "turn-1")
	if err != nil {
		t.Fatalf("get pending messages: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("got %d pending messages, want 2", len(pending))
	}
	if pending[0].SeqNum != 0 || pending[1].SeqNum != 1 {
		t.Fatalf("seq nums = %d, %d; want 0, 1", pending[0].SeqNum, pending[1].SeqNum)
	}
	if pending[0].ContentText != "weighing the options" || pending[1].ContentText != "here is the answer" {
		t.Fatalf("unexpected message order: %q then %q", pending[0].ContentText, pending[1].ContentText)
	}
}

func TestAppendMessageIfNew_RequiresIDAndSession(t *testing.T) {
	idx := newTestIndex(t)

	if _, err := idx.AppendMessageIfNew(Message{SessionID: "s", MessageType: "reasoning"}); err == nil {
		t.Error("expected an error when the message id is missing")
	}
	if _, err := idx.AppendMessageIfNew(Message{ID: "m", MessageType: "reasoning"}); err == nil {
		t.Error("expected an error when the session id is missing")
	}
}

func TestPendingMessageExists(t *testing.T) {
	idx := newTestIndex(t)

	const sessionID = "claude_code--sess"
	if _, err := idx.AppendMessageIfNew(Message{
		ID:          "msg_assistant_1",
		SessionID:   sessionID,
		TurnID:      "turn-1",
		Timestamp:   time.Now().UnixNano(),
		MessageType: "assistant",
		ContentText: "all done",
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	tests := []struct {
		name        string
		turnID      string
		messageType string
		content     string
		allTurns    bool
		want        bool
	}{
		{name: "same turn and text", turnID: "turn-1", messageType: "assistant", content: "all done", want: true},
		{name: "across all turns", messageType: "assistant", content: "all done", allTurns: true, want: true},
		{name: "different turn", turnID: "turn-2", messageType: "assistant", content: "all done"},
		{name: "different text", turnID: "turn-1", messageType: "assistant", content: "all done!"},
		{name: "different type", turnID: "turn-1", messageType: "reasoning", content: "all done"},
		{name: "empty text is never a match", turnID: "turn-1", messageType: "assistant", content: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := idx.PendingMessageExists(sessionID, tt.turnID, tt.messageType, tt.content, tt.allTurns)
			if err != nil {
				t.Fatalf("PendingMessageExists() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("PendingMessageExists() = %v, want %v", got, tt.want)
			}
		})
	}

	// Once a message is linked to a step it is no longer pending, so a later turn
	// repeating the same text is recorded rather than swallowed.
	if _, err := idx.LinkPendingMessagesToStep(sessionID, "turn-1", store.Hash("step-hash"), time.Now().UnixNano()); err != nil {
		t.Fatalf("link messages: %v", err)
	}
	exists, err := idx.PendingMessageExists(sessionID, "turn-1", "assistant", "all done", false)
	if err != nil {
		t.Fatalf("PendingMessageExists() error = %v", err)
	}
	if exists {
		t.Error("a linked message must not count as pending")
	}
}

func TestAppendToolUseMessages_IsIdempotentUnderConcurrency(t *testing.T) {
	root := t.TempDir()
	s, err := store.Init(root)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	idx, err := Open(s)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer func() { _ = idx.Close() }()

	const sessionID = "codex_cli:session"
	const turnID = "turn-1"
	const toolUseID = "tool-1"

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	inserted := make(chan bool, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			now := time.Now().UnixNano()
			ok, err := idx.AppendToolUseMessages(Message{
				ID:          fmt.Sprintf("call-%d", i),
				SessionID:   sessionID,
				TurnID:      turnID,
				Timestamp:   now,
				MessageType: "tool_call",
				ToolName:    "Write",
				ToolUseID:   toolUseID,
				ToolInput:   "args",
			}, Message{
				ID:          fmt.Sprintf("result-%d", i),
				SessionID:   sessionID,
				TurnID:      turnID,
				Timestamp:   now + 1,
				MessageType: "tool_result",
				ToolName:    "Write",
				ToolUseID:   toolUseID,
				ToolOutput:  "result",
			})
			if err != nil {
				errs <- err
				return
			}
			inserted <- ok
		}(i)
	}
	wg.Wait()
	close(errs)
	close(inserted)

	for err := range errs {
		t.Fatalf("append tool use messages: %v", err)
	}
	insertedCount := 0
	for ok := range inserted {
		if ok {
			insertedCount++
		}
	}
	if insertedCount != 1 {
		t.Fatalf("inserted count = %d, want 1", insertedCount)
	}

	messages, err := idx.GetPendingMessages(sessionID, turnID)
	if err != nil {
		t.Fatalf("get pending messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want call/result pair", len(messages))
	}
	if messages[0].MessageType != "tool_call" || messages[1].MessageType != "tool_result" {
		t.Fatalf("unexpected messages: %#v", messages)
	}
}

func TestAppendToolUseMessages_RejectsConflictingDuplicate(t *testing.T) {
	root := t.TempDir()
	s, err := store.Init(root)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	idx, err := Open(s)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer func() { _ = idx.Close() }()

	call := Message{
		ID:          "call-1",
		SessionID:   "codex_cli:session",
		TurnID:      "turn-1",
		Timestamp:   time.Now().UnixNano(),
		MessageType: "tool_call",
		ToolName:    "Write",
		ToolUseID:   "tool-1",
		ToolInput:   "args-1",
	}
	result := Message{
		ID:          "result-1",
		SessionID:   call.SessionID,
		TurnID:      call.TurnID,
		Timestamp:   call.Timestamp + 1,
		MessageType: "tool_result",
		ToolName:    call.ToolName,
		ToolUseID:   call.ToolUseID,
		ToolOutput:  "result-1",
	}
	ok, err := idx.AppendToolUseMessages(call, result)
	if err != nil {
		t.Fatalf("append initial tool use: %v", err)
	}
	if !ok {
		t.Fatal("expected initial tool use to insert")
	}

	conflictingCall := call
	conflictingCall.ID = "call-2"
	conflictingCall.ToolInput = "args-2"
	conflictingResult := result
	conflictingResult.ID = "result-2"

	ok, err = idx.AppendToolUseMessages(conflictingCall, conflictingResult)
	if err == nil {
		t.Fatal("expected conflicting duplicate to fail")
	}
	if ok {
		t.Fatal("conflicting duplicate should not report insertion")
	}
	if !strings.Contains(err.Error(), "existing tool_call payload differs") {
		t.Fatalf("unexpected duplicate error: %v", err)
	}

	messages, err := idx.GetPendingMessages(call.SessionID, call.TurnID)
	if err != nil {
		t.Fatalf("get pending messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("conflicting duplicate inserted messages: %#v", messages)
	}
}
