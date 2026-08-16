package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/regent-vcs/regent/internal/conversation"
	"github.com/regent-vcs/regent/internal/style"
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

func formatTextContent(text, indent string) string {
	if text == "" {
		return ""
	}

	// Split by lines
	lines := strings.Split(text, "\n")
	var formatted strings.Builder

	for _, line := range lines {
		// Preserve empty lines
		if strings.TrimSpace(line) == "" {
			formatted.WriteString("\n")
			continue
		}

		// Wrap long lines
		if len(line) > 100 {
			words := strings.Fields(line)
			currentLine := indent
			for _, word := range words {
				if len(currentLine)+len(word)+1 > 100 {
					formatted.WriteString(strings.TrimRight(currentLine, " ") + "\n")
					currentLine = indent + word + " "
				} else {
					currentLine += word + " "
				}
			}
			if len(currentLine) > len(indent) {
				formatted.WriteString(strings.TrimRight(currentLine, " ") + "\n")
			}
		} else {
			formatted.WriteString(indent + line + "\n")
		}
	}

	return strings.TrimRight(formatted.String(), "\n")
}

// shouldShowArg determines if a tool argument should be displayed
func shouldShowArg(key string) bool {
	// Show these important argument keys
	important := map[string]bool{
		"file_path":   true,
		"command":     true,
		"description": true,
		"prompt":      true,
		"query":       true,
		"path":        true,
	}
	return important[key]
}
