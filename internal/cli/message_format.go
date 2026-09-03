package cli

import (
	"encoding/json"
	"fmt"

	"github.com/bonez-io/re_gent/internal/conversation"
	"github.com/bonez-io/re_gent/internal/style"
)

// FormatMessagesHumanReadable converts raw message JSON into readable conversation format
func FormatMessagesHumanReadable(messages []json.RawMessage, indent string) string {
	if len(messages) == 0 {
		return style.DimText(indent + "(no conversation)")
	}

	// Extract actual conversation from agent wrapper format (Claude Code, etc.)
	conv, err := conversation.ExtractConversation(messages)
	if err != nil || len(conv) == 0 {
		return style.DimText(indent + fmt.Sprintf("(no conversation extracted from %d events)\n", len(messages)))
	}

	// Format conversation for display
	return conversation.FormatConversation(conv, indent)
}
