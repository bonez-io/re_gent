package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bonez-io/re_gent/internal/capture"
	"github.com/bonez-io/re_gent/internal/remote"
	"github.com/bonez-io/re_gent/internal/remote/remotecapture"
	"github.com/spf13/cobra"
)

// agentIDFromPayloadOrEnv returns agentID from the payload field if present,
// falling back to the CLAUDE_AGENT_ID environment variable that Claude Code
// sets for Task-spawned subagent processes.
func agentIDFromPayloadOrEnv(payloadAgentID string) string {
	if payloadAgentID != "" {
		return payloadAgentID
	}
	return os.Getenv("CLAUDE_AGENT_ID")
}

func MessageHookCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "message-hook [user|assistant]",
		Short:  "Internal: Claude Code hook for capturing messages",
		Args:   cobra.ExactArgs(1),
		RunE:   runMessageHook,
		Hidden: true,
	}
}

type claudeMessagePayload struct {
	Prompt               string `json:"prompt"`
	LastAssistantMessage string `json:"last_assistant_message"`
	SessionID            string `json:"session_id"`
	TurnID               string `json:"turn_id"`
	TranscriptPath       string `json:"transcript_path"`
	CWD                  string `json:"cwd"`
	Model                string `json:"model"`
	PermissionMode       string `json:"permission_mode"`
	AgentID              string `json:"agent_id"`
}

func runMessageHook(cmd *cobra.Command, args []string) error {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read payload: %w", err)
	}

	var payload claudeMessagePayload
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
			Origin:         capture.OriginClaudeCode,
			Model:          payload.Model,
			PermissionMode: payload.PermissionMode,
			TranscriptPath: payload.TranscriptPath,
			AgentID:        agentIDFromPayloadOrEnv(payload.AgentID),
		}

		switch args[0] {
		case "user":
			return recorder.RecordUserPrompt(capture.UserPrompt{
				SessionMetadata: meta,
				TurnID:          payload.TurnID,
				Prompt:          payload.Prompt,
			})
		case "assistant":
			return recorder.RecordAssistantAndFinalize(capture.AssistantResponse{
				SessionMetadata:      meta,
				TurnID:               payload.TurnID,
				LastAssistantMessage: payload.LastAssistantMessage,
			})
		default:
			return fmt.Errorf("unknown hook type: %s", args[0])
		}
	})
}

func hookCWD(cwd string) (string, error) {
	if cwd != "" {
		return cwd, nil
	}

	workingDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	return workingDir, nil
}

func withHookRecorder(cwd string, fn func(*capture.Recorder) error) error {
	// This is the sole hook command edge that chooses a store. Capture receives
	// an already-opened store and never examines remote configuration itself.
	recorder, ok, err := openHookRecorder(cwd)
	if err != nil {
		logHookCommandError(cwd, err)
		return nil
	}
	if !ok {
		return nil
	}
	defer func() { _ = recorder.Close() }()

	if err := fn(recorder); err != nil {
		logHookCommandError(cwd, err)
	}
	return nil
}

func openHookRecorder(cwd string) (*capture.Recorder, bool, error) {
	cfg, err := remote.LoadConfigForCWD(remote.OSEnv, cwd)
	if err == nil && cfg.Enabled() {
		if err := cfg.Validate(); err == nil {
			recorder, link, err := remotecapture.Open(cwd, cfg)
			if err == nil {
				link.Start(recorder)
				return recorder, true, nil
			}
			logServerModeFallback(cfg, err)
		} else {
			logServerModeFallback(cfg, err)
		}
	} else if err != nil {
		logServerModeFallback(cfg, err)
	}
	return capture.Open(cwd)
}

func logServerModeFallback(cfg remote.Config, err error) {
	cacheDir, cacheErr := remote.CacheDirFor(cfg)
	if err == nil || cacheErr != nil {
		return
	}
	capture.LogHookError(cacheDir, fmt.Sprintf("server mode unavailable, falling back to local capture: %v", err))
}

func logHookPayloadError(raw []byte, err error) error {
	cwd, cwdErr := hookCWD(cwdFromPayload(raw))
	if cwdErr != nil {
		return nil
	}
	logHookCommandError(cwd, err)
	return nil
}

func cwdFromPayload(raw []byte) string {
	var payload struct {
		CWD string `json:"cwd"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	return payload.CWD
}

func logHookCommandError(cwd string, err error) {
	if err == nil || cwd == "" {
		return
	}
	regentDir := filepath.Join(cwd, ".regent")
	if _, statErr := os.Stat(regentDir); statErr != nil {
		return
	}
	capture.LogHookError(regentDir, err.Error())
}
