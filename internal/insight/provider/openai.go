package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/bonez-io/re_gent/internal/config"
)

// maxEmbedBatch is the most texts sent in one embeddings call.
const maxEmbedBatch = 64

// openAIReader calls an OpenAI-compatible chat completions endpoint
// (OpenAI itself, Ollama, LM Studio, vLLM, OpenRouter, ...).
type openAIReader struct {
	cfg    config.InsightModelConfig
	client *http.Client
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatRequest struct {
	Model          string          `json:"model"`
	Temperature    float64         `json:"temperature"`
	Messages       []chatMessage   `json:"messages"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (r *openAIReader) Read(ctx context.Context, instructions string, request []byte) ([]byte, error) {
	key, err := readAPIKey(r.cfg.APIKeyEnv)
	if err != nil {
		return nil, err
	}

	url := strings.TrimSuffix(r.cfg.BaseURL, "/") + "/chat/completions"
	headers := map[string]string{"content-type": "application/json"}
	if key != "" {
		headers["Authorization"] = "Bearer " + key
	}

	messages := []chatMessage{
		{Role: "system", Content: instructions},
		{Role: "user", Content: string(request)},
	}

	body := chatRequest{
		Model:          r.cfg.Model,
		Temperature:    0,
		Messages:       messages,
		ResponseFormat: &responseFormat{Type: "json_object"},
	}

	resp, respBody, err := doWithRetry(ctx, r.client, http.MethodPost, url, headers, func() []byte {
		b, _ := json.Marshal(body)
		return b
	})
	if err != nil {
		return nil, fmt.Errorf("openai-compatible: %w", err)
	}

	// Some OpenAI-compatible servers (many local proxies) reject an unknown
	// response_format field with 400. Retry once without it before giving
	// up, since the field is a nicety, not a requirement of the contract.
	if resp.StatusCode == http.StatusBadRequest && bytes.Contains(bytes.ToLower(respBody), []byte("response_format")) {
		body.ResponseFormat = nil
		resp, respBody, err = doWithRetry(ctx, r.client, http.MethodPost, url, headers, func() []byte {
			b, _ := json.Marshal(body)
			return b
		})
		if err != nil {
			return nil, fmt.Errorf("openai-compatible: %w", err)
		}
	}

	if !isSuccess(resp.StatusCode) {
		return nil, fmt.Errorf("openai-compatible: %w", statusError(resp.StatusCode, respBody))
	}

	var parsed chatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("openai-compatible: parse reply: %w", err)
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("openai-compatible: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("openai-compatible: reply had no choices")
	}

	obj, err := extractJSON(parsed.Choices[0].Message.Content)
	if err != nil {
		return nil, fmt.Errorf("openai-compatible: %w", err)
	}
	return obj, nil
}

// openAIEmbedder calls an OpenAI-compatible embeddings endpoint.
type openAIEmbedder struct {
	cfg    config.InsightEmbeddingConfig
	client *http.Client
}

type embeddingsRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingsResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (e *openAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	key, err := readAPIKey(e.cfg.APIKeyEnv)
	if err != nil {
		return nil, err
	}

	url := strings.TrimSuffix(e.cfg.BaseURL, "/") + "/embeddings"
	headers := map[string]string{"content-type": "application/json"}
	if key != "" {
		headers["Authorization"] = "Bearer " + key
	}

	result := make([][]float32, 0, len(texts))
	var wantDim int
	if e.cfg.Dimensions > 0 {
		wantDim = e.cfg.Dimensions
	}

	for start := 0; start < len(texts); start += maxEmbedBatch {
		end := start + maxEmbedBatch
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[start:end]

		body := embeddingsRequest{Model: e.cfg.Model, Input: batch}
		resp, respBody, err := doWithRetry(ctx, e.client, http.MethodPost, url, headers, func() []byte {
			b, _ := json.Marshal(body)
			return b
		})
		if err != nil {
			return nil, fmt.Errorf("openai-compatible embeddings: %w", err)
		}
		if !isSuccess(resp.StatusCode) {
			return nil, fmt.Errorf("openai-compatible embeddings: %w", statusError(resp.StatusCode, respBody))
		}

		var parsed embeddingsResponse
		if err := json.Unmarshal(respBody, &parsed); err != nil {
			return nil, fmt.Errorf("openai-compatible embeddings: parse reply: %w", err)
		}
		if parsed.Error != nil {
			return nil, fmt.Errorf("openai-compatible embeddings: %s", parsed.Error.Message)
		}
		if len(parsed.Data) != len(batch) {
			return nil, fmt.Errorf("openai-compatible embeddings: got %d vectors for %d texts", len(parsed.Data), len(batch))
		}

		vectors := make([][]float32, len(batch))
		for _, d := range parsed.Data {
			if d.Index < 0 || d.Index >= len(vectors) {
				return nil, fmt.Errorf("openai-compatible embeddings: vector index %d out of range", d.Index)
			}
			vectors[d.Index] = d.Embedding
		}

		for _, v := range vectors {
			if wantDim == 0 {
				wantDim = len(v)
			}
			if len(v) != wantDim {
				return nil, fmt.Errorf("openai-compatible embeddings: vector length %d, want %d", len(v), wantDim)
			}
			result = append(result, v)
		}
	}

	return result, nil
}
