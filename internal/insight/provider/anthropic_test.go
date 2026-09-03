package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bonez-io/re_gent/internal/config"
)

func TestAnthropicReader_Read(t *testing.T) {
	t.Setenv("RGT_TEST_ANTHROPIC_KEY", "sk-test-secret")

	var gotHeaders http.Header
	var gotBody anthropicRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotHeaders = req.Header.Clone()
		if err := json.NewDecoder(req.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		if req.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected path: %s", req.URL.Path)
		}
		resp := anthropicResponse{Content: []anthropicContentBlock{
			{Type: "text", Text: `{"work_items": [{"goal": "test"}]}`},
		}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := config.InsightModelConfig{
		Provider:  "anthropic",
		Model:     "claude-haiku-4-5-20251001",
		APIKeyEnv: "RGT_TEST_ANTHROPIC_KEY",
		BaseURL:   server.URL,
	}
	reader, info, err := NewReader(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if info.Provider != "anthropic" || info.Model != cfg.Model {
		t.Fatalf("unexpected info: %+v", info)
	}

	obj, err := reader.Read(context.Background(), "system prompt", []byte(`{"turns": []}`))
	if err != nil {
		t.Fatal(err)
	}
	mustHaveWorkItems(t, obj)

	if gotHeaders.Get("x-api-key") != "sk-test-secret" {
		t.Fatalf("x-api-key not sent: %v", gotHeaders)
	}
	if gotHeaders.Get("anthropic-version") != "2023-06-01" {
		t.Fatalf("anthropic-version not sent: %v", gotHeaders)
	}
	if gotBody.System != "system prompt" {
		t.Fatalf("system not sent: %q", gotBody.System)
	}
	if len(gotBody.Messages) != 1 || gotBody.Messages[0].Role != "user" {
		t.Fatalf("unexpected messages: %+v", gotBody.Messages)
	}
	if gotBody.MaxTokens != anthropicMaxTokens {
		t.Fatalf("unexpected max_tokens: %d", gotBody.MaxTokens)
	}
	if gotBody.Temperature != 0 {
		t.Fatalf("unexpected temperature: %v", gotBody.Temperature)
	}
}

func TestAnthropicReader_MissingKey(t *testing.T) {
	cfg := config.InsightModelConfig{
		Provider:  "anthropic",
		Model:     "claude-haiku-4-5-20251001",
		APIKeyEnv: "RGT_TEST_ANTHROPIC_KEY_UNSET",
	}
	reader, _, err := NewReader(cfg)
	if err != nil {
		t.Fatal(err)
	}

	_, err = reader.Read(context.Background(), "sys", []byte(`{}`))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "RGT_TEST_ANTHROPIC_KEY_UNSET is not set") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(err.Error(), "sk-") {
		t.Fatalf("error must never contain a key value: %v", err)
	}
}

func TestAnthropicReader_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": {"type": "authentication_error", "message": "bad key"}}`))
	}))
	defer server.Close()

	cfg := config.InsightModelConfig{Provider: "anthropic", Model: "m", BaseURL: server.URL}
	reader, _, err := NewReader(cfg)
	if err != nil {
		t.Fatal(err)
	}

	_, err = reader.Read(context.Background(), "sys", []byte(`{}`))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected status in error: %v", err)
	}
}

func TestAnthropicReader_RetriesOn5xx(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("boom"))
			return
		}
		resp := anthropicResponse{Content: []anthropicContentBlock{
			{Type: "text", Text: `{"work_items": []}`},
		}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := config.InsightModelConfig{Provider: "anthropic", Model: "m", BaseURL: server.URL}
	reader, _, err := NewReader(cfg)
	if err != nil {
		t.Fatal(err)
	}

	obj, err := reader.Read(context.Background(), "sys", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	mustHaveWorkItems(t, obj)
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
}

func TestNewReader_NotConfigured(t *testing.T) {
	_, _, err := NewReader(config.InsightModelConfig{})
	if err != ErrNotConfigured {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}
