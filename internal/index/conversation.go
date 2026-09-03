package index

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/bonez-io/re_gent/internal/store"
)

// ConversationEntry is the portable normalized conversation shape
// attached to modern steps. It deliberately mirrors capture's on-disk schema
// without importing an implementation detail from that package.
type ConversationEntry struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	TS         int64  `json:"ts,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	ToolUseID  string `json:"tool_use_id,omitempty"`
	ToolInput  string `json:"tool_input,omitempty"`
	ToolOutput string `json:"tool_output,omitempty"`
}

// RebuildConversation restores the disposable message rows that log, show,
// and the insight worker read from the canonical conversation object carried
// by a step. Existing rows win: an incremental pull must never duplicate the
// recording machine's local messages with reconstructed copies. It is used by
// `rgt pull` on a machine-local cache and by a server mirroring pushed steps
// into its own index.
func RebuildConversation(s *store.Store, idx *DB, stepHash store.Hash, step *store.Step) error {
	existing, err := idx.GetMessagesForStep(stepHash)
	if err != nil {
		return fmt.Errorf("read existing conversation for step %s: %w", stepHash, err)
	}
	if len(existing) > 0 {
		return nil
	}

	var entries []ConversationEntry
	if step.Conversation != "" {
		data, err := s.ReadBlob(step.Conversation)
		if err != nil {
			return fmt.Errorf("read conversation blob %s: %w", step.Conversation, err)
		}
		if err := json.Unmarshal(data, &entries); err != nil {
			return fmt.Errorf("decode conversation blob %s: %w", step.Conversation, err)
		}
	}

	hasPortableToolMessages := false
	for position, entry := range entries {
		switch entry.Type {
		case "user", "assistant", "reasoning", "tool_call", "tool_result":
		default:
			continue
		}
		if entry.Type == "tool_call" || entry.Type == "tool_result" {
			hasPortableToolMessages = true
		}
		timestamp := entry.TS
		if timestamp == 0 {
			timestamp = step.TimestampNanos + int64(position)
		}
		if _, err := idx.AppendMessageIfNew(Message{
			ID:          rebuiltMessageID(stepHash, "conversation", position),
			SessionID:   step.SessionID,
			StepID:      string(stepHash),
			TurnID:      step.TurnID,
			Timestamp:   timestamp,
			ProcessedAt: step.TimestampNanos,
			MessageType: entry.Type,
			ContentText: entry.Text,
			ToolName:    entry.ToolName,
			ToolUseID:   entry.ToolUseID,
			ToolInput:   entry.ToolInput,
			ToolOutput:  entry.ToolOutput,
		}); err != nil {
			return fmt.Errorf("restore %s message for step %s: %w", entry.Type, stepHash, err)
		}
	}

	if hasPortableToolMessages {
		return nil
	}

	// Early conversation blobs contained text only. For those steps, synthesize
	// tool rows from canonical causes so older remote history remains useful.
	for position, cause := range step.Causes {
		base := len(entries) + position*2
		if _, err := idx.AppendMessageIfNew(Message{
			ID:          rebuiltMessageID(stepHash, "tool-call", position),
			SessionID:   step.SessionID,
			StepID:      string(stepHash),
			TurnID:      step.TurnID,
			Timestamp:   step.TimestampNanos + int64(base),
			ProcessedAt: step.TimestampNanos,
			MessageType: "tool_call",
			ToolName:    cause.ToolName,
			ToolUseID:   cause.ToolUseID,
			ToolInput:   string(cause.ArgsBlob),
		}); err != nil {
			return fmt.Errorf("restore tool call for step %s: %w", stepHash, err)
		}
		if _, err := idx.AppendMessageIfNew(Message{
			ID:          rebuiltMessageID(stepHash, "tool-result", position),
			SessionID:   step.SessionID,
			StepID:      string(stepHash),
			TurnID:      step.TurnID,
			Timestamp:   step.TimestampNanos + int64(base+1),
			ProcessedAt: step.TimestampNanos,
			MessageType: "tool_result",
			ToolName:    cause.ToolName,
			ToolUseID:   cause.ToolUseID,
			ToolOutput:  string(cause.ResultBlob),
		}); err != nil {
			return fmt.Errorf("restore tool result for step %s: %w", stepHash, err)
		}
	}

	return nil
}

func rebuiltMessageID(stepHash store.Hash, kind string, position int) string {
	return "pulled_" + string(stepHash) + "_" + kind + "_" + strconv.Itoa(position)
}
