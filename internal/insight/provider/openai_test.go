package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bonez-io/re_gent/internal/config"
)

func TestOpenAIReader_Read(t *testing.T) {
	var gotBody chatRequest
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotAuth = req.Header.Get("Authorization")
		if req.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", req.URL.Path)
		}
		if err := json.NewDecoder(req.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		resp := chatResponse{Choices: []struct {
			Message chatMessage `json:"message"`
		}{{Message: chatMessage{Role: "assistant", Content: `{"work_items": [{"goal": "test"}]}`}}}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	t.Setenv("RGT_TEST_OPENAI_KEY", "sk-openai-secret")
	cfg := config.InsightModelConfig{
		Provider:  "openai-compatible",
		Model:     "gpt-test",
		BaseURL:   server.URL + "/v1/",
		APIKeyEnv: "RGT_TEST_OPENAI_KEY",
	}
	reader, info, err := NewReader(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if info.Provider != "openai-compatible" {
		t.Fatalf("unexpected info: %+v", info)
	}

	obj, err := reader.Read(context.Background(), "system prompt", []byte(`{"turns": []}`))
	if err != nil {
		t.Fatal(err)
	}
	mustHaveWorkItems(t, obj)

	if gotAuth != "Bearer sk-openai-secret" {
		t.Fatalf("unexpected Authorization header: %q", gotAuth)
	}
	if gotBody.ResponseFormat == nil || gotBody.ResponseFormat.Type != "json_object" {
		t.Fatalf("expected response_format json_object: %+v", gotBody.ResponseFormat)
	}
	if len(gotBody.Messages) != 2 || gotBody.Messages[0].Role != "system" || gotBody.Messages[1].Role != "user" {
		t.Fatalf("unexpected messages: %+v", gotBody.Messages)
	}
	if gotBody.Messages[0].Content != "system prompt" {
		t.Fatalf("unexpected system content: %q", gotBody.Messages[0].Content)
	}
}

func TestOpenAIReader_NoAuthHeaderWithoutKeyEnv(t *testing.T) {
	var gotAuth string
	seen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen = true
		gotAuth = req.Header.Get("Authorization")
		resp := chatResponse{Choices: []struct {
			Message chatMessage `json:"message"`
		}{{Message: chatMessage{Content: `{"work_items": []}`}}}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := config.InsightModelConfig{Provider: "openai-compatible", Model: "m", BaseURL: server.URL}
	reader, _, err := NewReader(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(context.Background(), "sys", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("server never received a request")
	}
	if gotAuth != "" {
		t.Fatalf("expected no Authorization header, got %q", gotAuth)
	}
}

// TestOpenAIReader_ResponseFormatFallback covers a server that rejects an
// unrecognized response_format field with 400: the reader must retry once
// without it rather than failing the call.
func TestOpenAIReader_ResponseFormatFallback(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		attempts++
		var body chatRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.ResponseFormat != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error": {"message": "unsupported field: response_format"}}`))
			return
		}
		resp := chatResponse{Choices: []struct {
			Message chatMessage `json:"message"`
		}{{Message: chatMessage{Content: `{"work_items": [{"goal": "fallback"}]}`}}}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := config.InsightModelConfig{Provider: "openai-compatible", Model: "m", BaseURL: server.URL}
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
		t.Fatalf("expected 2 attempts (with fallback), got %d", attempts)
	}
}

func TestOpenAIReader_ErrorStatusNotResponseFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": {"message": "invalid model"}}`))
	}))
	defer server.Close()

	cfg := config.InsightModelConfig{Provider: "openai-compatible", Model: "m", BaseURL: server.URL}
	reader, _, err := NewReader(cfg)
	if err != nil {
		t.Fatal(err)
	}

	_, err = reader.Read(context.Background(), "sys", []byte(`{}`))
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestOpenAIEmbedder_Embed(t *testing.T) {
	var batches [][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body embeddingsRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		batches = append(batches, body.Input)

		resp := embeddingsResponse{}
		for i := range body.Input {
			resp.Data = append(resp.Data, struct {
				Embedding []float32 `json:"embedding"`
				Index     int       `json:"index"`
			}{Embedding: []float32{float32(i), 0.5, 0.25}, Index: i})
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := config.InsightEmbeddingConfig{Provider: "openai-compatible", Model: "embed-test", BaseURL: server.URL}
	embedder, info, err := NewEmbedder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if info.Provider != "openai-compatible" || info.Model != "embed-test" {
		t.Fatalf("unexpected info: %+v", info)
	}

	texts := make([]string, 100)
	for i := range texts {
		texts[i] = "text"
	}

	vecs, err := embedder.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 100 {
		t.Fatalf("expected 100 vectors, got %d", len(vecs))
	}
	if len(batches) != 2 {
		t.Fatalf("expected 2 batches (64 + 36), got %d: sizes %v", len(batches), func() []int {
			var s []int
			for _, b := range batches {
				s = append(s, len(b))
			}
			return s
		}())
	}
	if len(batches[0]) != 64 || len(batches[1]) != 36 {
		t.Fatalf("unexpected batch sizes: %d, %d", len(batches[0]), len(batches[1]))
	}
}

func TestOpenAIEmbedder_DimensionMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		resp := embeddingsResponse{Data: []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}{
			{Embedding: []float32{1, 2, 3}, Index: 0},
			{Embedding: []float32{1, 2}, Index: 1},
		}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := config.InsightEmbeddingConfig{Provider: "openai-compatible", Model: "m", BaseURL: server.URL}
	embedder, _, err := NewEmbedder(cfg)
	if err != nil {
		t.Fatal(err)
	}

	_, err = embedder.Embed(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("expected a dimension mismatch error")
	}
}

func TestOpenAIEmbedder_ConfiguredDimensionsEnforced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		resp := embeddingsResponse{Data: []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}{
			{Embedding: []float32{1, 2, 3}, Index: 0},
		}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := config.InsightEmbeddingConfig{Provider: "openai-compatible", Model: "m", BaseURL: server.URL, Dimensions: 768}
	embedder, _, err := NewEmbedder(cfg)
	if err != nil {
		t.Fatal(err)
	}

	_, err = embedder.Embed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("expected an error for wrong configured dimensions")
	}
}

func TestNewEmbedder_NotConfigured(t *testing.T) {
	_, _, err := NewEmbedder(config.InsightEmbeddingConfig{})
	if err != ErrNotConfigured {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}
