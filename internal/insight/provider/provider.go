package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/bonez-io/re_gent/internal/config"
	"github.com/bonez-io/re_gent/internal/insight"
)

// ErrNotConfigured is returned by NewReader and NewEmbedder when the config
// names no provider. Callers treat this the same as Settings.Active being
// false: nothing to do, not a failure.
var ErrNotConfigured = errors.New("insight: no provider configured")

// Info names what produced a result. The worker keys derived rows (work
// items, embeddings) by it so a later change of model or provider is
// visible in the data rather than silently blended into history it did not
// produce.
type Info struct {
	Provider string
	Model    string
}

// Reader makes one structured read call. instructions is the system
// prompt; request is the JSON object described in RFC 0007 Appendix A. The
// returned bytes are the single top-level JSON object the model replied
// with, ready for json.Unmarshal — never a fenced block, wrapper, or JSONL
// stream.
type Reader interface {
	Read(ctx context.Context, instructions string, request []byte) ([]byte, error)
}

// Embedder embeds texts. result[i] corresponds to texts[i]; every vector in
// the result has the same length.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// readTimeout bounds one structured read call. Reads walk a full turn's
// worth of hunks and conversation, so they get the longer budget.
const readTimeout = 120 * time.Second

// embedTimeout bounds one embedding call.
const embedTimeout = 60 * time.Second

// retryPause is how long a retried call waits before its second attempt.
const retryPause = 2 * time.Second

// NewReader builds the Reader named by cfg.Provider. It returns
// ErrNotConfigured when cfg.Provider is empty, and does no network I/O.
func NewReader(cfg config.InsightModelConfig) (Reader, Info, error) {
	info := Info{Provider: cfg.Provider, Model: cfg.Model}
	switch cfg.Provider {
	case "":
		return nil, Info{}, ErrNotConfigured
	case insight.ProviderAnthropic:
		return &anthropicReader{cfg: cfg, client: newHTTPClient(readTimeout)}, info, nil
	case insight.ProviderOpenAICompatible:
		return &openAIReader{cfg: cfg, client: newHTTPClient(readTimeout)}, info, nil
	case insight.ProviderCommand:
		return &commandReader{cfg: cfg}, info, nil
	default:
		return nil, Info{}, fmt.Errorf("insight: unknown model provider %q", cfg.Provider)
	}
}

// NewEmbedder builds the Embedder named by cfg.Provider. It returns
// ErrNotConfigured when cfg.Provider is empty, and does no network I/O.
// Anthropic has no embeddings endpoint, so it is not a valid embedding
// provider; internal/insight/settings.Resolve already rejects it before a
// caller reaches here.
func NewEmbedder(cfg config.InsightEmbeddingConfig) (Embedder, Info, error) {
	info := Info{Provider: cfg.Provider, Model: cfg.Model}
	switch cfg.Provider {
	case "":
		return nil, Info{}, ErrNotConfigured
	case insight.ProviderOpenAICompatible:
		return &openAIEmbedder{cfg: cfg, client: newHTTPClient(embedTimeout)}, info, nil
	case insight.ProviderCommand:
		return &commandEmbedder{cfg: cfg}, info, nil
	default:
		return nil, Info{}, fmt.Errorf("insight: unknown embedding provider %q", cfg.Provider)
	}
}

func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// doWithRetry sends req and retries once, after retryPause, on a connection
// error, HTTP 429, or a 5xx status. It never retries a request whose body
// has already been drained by a prior attempt without a fresh reader, so
// callers pass bodyFn to produce the body anew on each attempt.
func doWithRetry(ctx context.Context, client *http.Client, method, url string, headers map[string]string, bodyFn func() []byte) (*http.Response, []byte, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(retryPause):
			}
		}

		body := bodyFn()
		req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
		if err != nil {
			return nil, nil, err
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = statusError(resp.StatusCode, respBody)
			continue
		}

		return resp, respBody, nil
	}
	return nil, nil, lastErr
}

// statusError formats a non-2xx response error. The body excerpt is capped
// at 300 bytes; provider bodies never carry the API key, so this is safe to
// surface directly.
func statusError(status int, body []byte) error {
	excerpt := body
	if len(excerpt) > 300 {
		excerpt = excerpt[:300]
	}
	return fmt.Errorf("http %d: %s", status, string(excerpt))
}

func isSuccess(status int) bool {
	return status >= 200 && status < 300
}
