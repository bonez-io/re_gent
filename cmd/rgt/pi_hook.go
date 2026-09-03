package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/bonez-io/re_gent/internal/capture"
	"github.com/spf13/cobra"
)

func PiHookCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "pi-hook",
		Short:  "Internal: Pi hook",
		RunE:   runPiHook,
		Hidden: true,
	}
}

type piHookPayload struct {
	SessionID            string          `json:"session_id"`
	TurnID               string          `json:"turn_id"`
	TranscriptPath       string          `json:"transcript_path"`
	CWD                  string          `json:"cwd"`
	HookEventName        string          `json:"hook_event_name"`
	Model                string          `json:"model"`
	Prompt               string          `json:"prompt"`
	LastAssistantMessage string          `json:"last_assistant_message"`
	ToolName             string          `json:"tool_name"`
	ToolInput            json.RawMessage `json:"tool_input"`
	ToolUseID            string          `json:"tool_use_id"`
	ToolResponse         json.RawMessage `json:"tool_response"`
	AgentID              string          `json:"agent_id"`
}

func runPiHook(cmd *cobra.Command, args []string) error {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read payload: %w", err)
	}

	var payload piHookPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return logHookPayloadError(raw, fmt.Errorf("parse payload: %w", err))
	}

	cwd, err := hookCWD(payload.CWD)
	if err != nil {
		return err
	}

	return withHookRecorder(cwd, func(recorder *capture.Recorder) error {
		meta := capture.SessionMetadata{
			SessionID:      payload.SessionID,
			Origin:         capture.OriginPi,
			Model:          payload.Model,
			TranscriptPath: payload.TranscriptPath,
			AgentID:        payload.AgentID,
		}

		switch normalizeHookEventName(payload.HookEventName) {
		case "sessionstart":
			return recorder.UpsertSession(meta)
		case "userpromptsubmit":
			return recorder.RecordUserPrompt(capture.UserPrompt{
				SessionMetadata: meta,
				TurnID:          payload.TurnID,
				Prompt:          payload.Prompt,
			})
		case "posttooluse":
			return recorder.RecordToolUse(capture.ToolUse{
				SessionMetadata: meta,
				TurnID:          payload.TurnID,
				ToolName:        payload.ToolName,
				ToolUseID:       payload.ToolUseID,
				ToolInput:       payload.ToolInput,
				ToolResponse:    payload.ToolResponse,
			})
		case "stop":
			return recorder.RecordAssistantAndFinalize(capture.AssistantResponse{
				SessionMetadata:      meta,
				TurnID:               payload.TurnID,
				LastAssistantMessage: payload.LastAssistantMessage,
			})
		default:
			return fmt.Errorf("unsupported Pi hook event: %s", payload.HookEventName)
		}
	})
}
