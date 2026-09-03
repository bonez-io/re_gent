package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/bonez-io/re_gent/internal/config"
)

// defaultAnthropicBaseURL is used when cfg.BaseURL is empty.
const defaultAnthropicBaseURL = "https://api.anthropic.com"

// anthropicMaxTokens bounds every read call's reply. RFC 0007's structured
// reply is small; this is generous headroom, not a tuning knob.
const anthropicMaxTokens = 4096

// anthropicReader calls the Anthropic Messages API.
type anthropicReader struct {
	cfg    config.InsightModelConfig
	client *http.Client
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature"`
	System      string             `json:"system"`
	Messages    []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []anthropicContentBlock `json:"content"`
	Error   *anthropicError         `json:"error"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (r *anthropicReader) Read(ctx context.Context, instructions string, request []byte) ([]byte, error) {
	key, err := readAPIKey(r.cfg.APIKeyEnv)
	if err != nil {
		return nil, err
	}

	base := strings.TrimSuffix(r.cfg.BaseURL, "/")
	if base == "" {
		base = defaultAnthropicBaseURL
	}
	url := base + "/v1/messages"

	body := anthropicRequest{
		Model:       r.cfg.Model,
		MaxTokens:   anthropicMaxTokens,
		Temperature: 0,
		System:      instructions,
		Messages: []anthropicMessage{
			{Role: "user", Content: string(request)},
		},
	}

	headers := map[string]string{
		"content-type":      "application/json",
		"anthropic-version": "2023-06-01",
	}
	if key != "" {
		headers["x-api-key"] = key
	}

	resp, respBody, err := doWithRetry(ctx, r.client, http.MethodPost, url, headers, func() []byte {
		b, _ := json.Marshal(body)
		return b
	})
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	if !isSuccess(resp.StatusCode) {
		return nil, fmt.Errorf("anthropic: %w", statusError(resp.StatusCode, respBody))
	}

	var parsed anthropicResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("anthropic: parse reply: %w", err)
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("anthropic: %s: %s", parsed.Error.Type, parsed.Error.Message)
	}

	var text strings.Builder
	for _, block := range parsed.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	if text.Len() == 0 {
		return nil, fmt.Errorf("anthropic: reply had no text content")
	}

	obj, err := extractJSON(text.String())
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	return obj, nil
}

// readAPIKey reads env at call time, never at construction, so a key set
// after startup is still picked up and so no key value is ever held in a
// long-lived struct. An empty env name means the provider needs no key
// (e.g. a local proxy).
func readAPIKey(env string) (string, error) {
	env = strings.TrimSpace(env)
	if env == "" {
		return "", nil
	}
	v := os.Getenv(env)
	if v == "" {
		return "", fmt.Errorf("api key variable %s is not set", env)
	}
	return v, nil
}
