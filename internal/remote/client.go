package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bonez-io/re_gent/internal/store"
)

// MaxObjectSize mirrors the server's per-object limit. Objects larger than this
// are rejected client-side so the failure is a clear local error instead of a
// truncated upload.
const MaxObjectSize = 50 << 20

// maxRetries bounds retries for a single request. The context deadline is the
// real budget; this only stops a fast-failing server from being hammered.
const maxRetries = 3

// maxRefListSize bounds a ref listing. One entry is a session name and a
// 64-character hash, so this holds tens of thousands of sessions — far past any
// real repository, and still a bound rather than an open-ended read.
const maxRefListSize = 4 << 20

var (
	// ErrNotFound is returned when an object or ref does not exist on the server.
	ErrNotFound = errors.New("not found on server")
	// ErrConflict is returned when a ref CAS loses a race with another writer.
	ErrConflict = errors.New("ref was modified concurrently on the server")
	// ErrUnauthorized is returned for 401/403 responses. It is never retried:
	// a bad token does not get better by trying again.
	ErrUnauthorized = errors.New("server rejected credentials")
	// ErrIncomplete is returned when the server refuses a ref update because
	// objects the step references were never received (HTTP 422). It signals
	// the caller to re-push the full history rather than the delta.
	ErrIncomplete = errors.New("server is missing objects referenced by this step")
	// ErrTooLarge is returned when an object exceeds MaxObjectSize.
	ErrTooLarge = errors.New("object exceeds server size limit")
)

// Client is the subset of the server protocol that capture and the sync
// commands depend on. It is an interface so tests can inject faults without a
// network stack.
type Client interface {
	// HasObject reports whether the server already stores the object.
	HasObject(ctx context.Context, h store.Hash) (bool, error)
	// PutObject uploads object bytes. It is idempotent: the server is
	// content-addressed, so re-uploading the same bytes is a no-op.
	PutObject(ctx context.Context, content []byte) (store.Hash, error)
	// GetObject downloads object bytes, verifying the hash before returning.
	GetObject(ctx context.Context, h store.Hash) ([]byte, error)
	// GetRef reads a ref. It returns ErrNotFound when the ref does not exist.
	GetRef(ctx context.Context, name string) (store.Hash, error)
	// ListRefs lists the server's refs under dir (e.g. "sessions"), keyed by
	// name relative to dir. It is the only call that does not require the
	// caller to already know a ref name, which is what a machine that has
	// pushed nothing needs in order to pull anything.
	ListRefs(ctx context.Context, dir string) (map[string]store.Hash, error)
	// UpdateRef compare-and-swaps a ref. expected is "" for a new ref.
	UpdateRef(ctx context.Context, name string, expected, next store.Hash) error
}

// HTTPClient is the production Client, speaking the re_gent server protocol:
//
//	HEAD /repos/{repo}/objects/{hash}   -> 200 | 404
//	POST /repos/{repo}/objects          -> 201 {"hash":"..."}
//	GET  /repos/{repo}/objects/{hash}   -> 200 <bytes> | 404
//	GET  /repos/{repo}/refs/{ref...}    -> 200 {"hash":"..."} | 404
//	PUT  /repos/{repo}/refs/{ref...}    -> 200 | 409 | 422
type HTTPClient struct {
	baseURL string
	// repoID is the routing key interpolated into every object/ref/history
	// URL. Despite the name it holds whatever Config.Key() resolved to: a
	// legacy repo_id against an old server, or a project_id (RFC 0004)
	// against one that has adopted the project API. The wire protocol does
	// not care which — "Object/ref/history routes accept a prj_… id wherever
	// a repo id is used today" — so one field and one name serve both.
	repoID string
	http   *http.Client

	mu           sync.Mutex
	token        string
	refreshToken string
	onRefresh    func(accessToken, refreshToken string, expiresIn int)
}

// NewHTTPClient builds a Client for cfg. The configuration is validated up
// front so that a malformed server URL fails before any hook runs.
func NewHTTPClient(cfg Config) (*HTTPClient, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &HTTPClient{
		baseURL:      strings.TrimRight(cfg.ServerURL, "/"),
		repoID:       cfg.Key(),
		token:        cfg.Token,
		refreshToken: cfg.RefreshToken,
		// No client-level timeout: every call carries a context deadline, which
		// also covers retries. A Client.Timeout here would silently split that
		// budget per attempt.
		http: &http.Client{},
	}, nil
}

// SetRefresh equips the client to recover from an expired access token
// without failing the call: on a 401 whose body carries
// {"code":"token_expired"}, the client exchanges refreshToken for a new pair
// via POST /api/v1/auth/token/refresh and retries the request once. onRefresh,
// when non-nil, is called with the new pair so the caller can persist it (the
// client itself never writes to disk) — a refresh that succeeds but is never
// persisted just means the next process refreshes again, which is safe but
// wasteful, so callers should wire onRefresh whenever they have somewhere to
// put it.
func (c *HTTPClient) SetRefresh(refreshToken string, onRefresh func(accessToken, refreshToken string, expiresIn int)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshToken = refreshToken
	c.onRefresh = onRefresh
}

// CurrentToken returns the access token the client is presenting right now,
// which may have changed since construction if a refresh happened.
func (c *HTTPClient) CurrentToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

func (c *HTTPClient) currentAuth() (token, refreshToken string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token, c.refreshToken
}

func (c *HTTPClient) objectURL(h store.Hash) string {
	return fmt.Sprintf("%s/%s/objects/%s", c.baseURL, c.repoID, h)
}

func (c *HTTPClient) refURL(name string) string {
	return fmt.Sprintf("%s/%s/refs/%s", c.baseURL, c.repoID, name)
}

// refListURL addresses the collection rather than one member: GET on the refs
// directory itself, which is how a client that has never pushed learns what the
// server holds.
func (c *HTTPClient) refListURL(dir string) string {
	base := fmt.Sprintf("%s/%s/refs", c.baseURL, c.repoID)
	if dir == "" {
		return base
	}
	return base + "?dir=" + url.QueryEscape(dir)
}

// HasObject implements Client.
func (c *HTTPClient) HasObject(ctx context.Context, h store.Hash) (bool, error) {
	if err := validateFullHash(h); err != nil {
		return false, err
	}
	resp, err := c.do(ctx, http.MethodHead, c.objectURL(h), nil)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	closeBody(resp)
	return true, nil
}

// PutObject implements Client.
func (c *HTTPClient) PutObject(ctx context.Context, content []byte) (store.Hash, error) {
	if len(content) > MaxObjectSize {
		return "", fmt.Errorf("%w: %d bytes", ErrTooLarge, len(content))
	}
	want := store.HashBytes(content)

	// Objects are content-addressed and uploaded idempotently: PUT the bytes to
	// the object's own address. The server verifies the body hashes to the URL
	// hash, so any 2xx is proof of storage — there is no response body to parse.
	resp, err := c.do(ctx, http.MethodPut, c.objectURL(want), content)
	if err != nil {
		return "", err
	}
	closeBody(resp)
	return want, nil
}

// GetObject implements Client.
func (c *HTTPClient) GetObject(ctx context.Context, h store.Hash) ([]byte, error) {
	if err := validateFullHash(h); err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodGet, c.objectURL(h), nil)
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)

	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxObjectSize+1))
	if err != nil {
		return nil, fmt.Errorf("read object %s: %w", h, err)
	}
	if len(data) > MaxObjectSize {
		return nil, fmt.Errorf("%w: object %s", ErrTooLarge, h)
	}
	// Content addressing is the integrity check: a truncated or substituted
	// body cannot hash to the requested value.
	if got := store.HashBytes(data); got != h {
		return nil, fmt.Errorf("object %s failed integrity check (got %s)", h, got)
	}
	return data, nil
}

// ListRefs implements Client.
func (c *HTTPClient) ListRefs(ctx context.Context, dir string) (map[string]store.Hash, error) {
	if dir != "" {
		if err := ValidateRefName(dir); err != nil {
			return nil, err
		}
	}
	resp, err := c.do(ctx, http.MethodGet, c.refListURL(dir), nil)
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)

	var body struct {
		Refs map[string]string `json:"refs"`
	}
	// A ref listing is one hash and one short name per session. maxRefListSize
	// is generous for that and still bounded, because this response is the one
	// place a client reads something whose size it did not choose.
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRefListSize)).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode ref listing: %w", err)
	}

	out := make(map[string]store.Hash, len(body.Refs))
	for name, hash := range body.Refs {
		if err := validateFullHash(store.Hash(hash)); err != nil {
			return nil, fmt.Errorf("ref %s: %w", name, err)
		}
		out[name] = store.Hash(hash)
	}
	return out, nil
}

// GetRef implements Client.
func (c *HTTPClient) GetRef(ctx context.Context, name string) (store.Hash, error) {
	if err := ValidateRefName(name); err != nil {
		return "", err
	}
	resp, err := c.do(ctx, http.MethodGet, c.refURL(name), nil)
	if err != nil {
		return "", err
	}
	defer closeBody(resp)

	var body struct {
		Hash string `json:"hash"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&body); err != nil {
		return "", fmt.Errorf("decode ref response: %w", err)
	}
	if err := validateFullHash(store.Hash(body.Hash)); err != nil {
		return "", fmt.Errorf("ref %s: %w", name, err)
	}
	return store.Hash(body.Hash), nil
}

// UpdateRef implements Client.
func (c *HTTPClient) UpdateRef(ctx context.Context, name string, expected, next store.Hash) error {
	if err := ValidateRefName(name); err != nil {
		return err
	}
	if err := validateFullHash(next); err != nil {
		return fmt.Errorf("new hash: %w", err)
	}
	if expected != "" {
		if err := validateFullHash(expected); err != nil {
			return fmt.Errorf("expected hash: %w", err)
		}
	}

	payload, err := json.Marshal(map[string]string{
		"old": string(expected),
		"new": string(next),
	})
	if err != nil {
		return fmt.Errorf("encode ref update: %w", err)
	}

	resp, err := c.do(ctx, http.MethodPost, c.refURL(name), payload)
	if err != nil {
		return err
	}
	closeBody(resp)
	return nil
}

// do performs one request with retries, translating HTTP status codes into
// sentinel errors. Only network errors and 5xx are retried; 4xx are terminal
// — except a 401 that names "token_expired", which is refreshed and retried
// exactly once (see refreshedOnce below), because that failure mode gets
// better by trying again and every other 4xx does not.
func (c *HTTPClient) do(ctx context.Context, method, url string, body []byte) (*http.Response, error) {
	var lastErr error
	refreshedOnce := false

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, backoff(attempt)); err != nil {
				return nil, errors.Join(err, lastErr)
			}
		}

		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, reader)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		token, _ := c.currentAuth()
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/octet-stream")
			req.ContentLength = int64(len(body))
		}

		resp, err := c.http.Do(req)
		if err != nil {
			// Transport-level failure: connection refused, reset, DNS, or the
			// context deadline expiring mid-flight. Retry unless the context is
			// already done.
			lastErr = fmt.Errorf("%s %s: %w", method, redactURL(url), err)
			if ctx.Err() != nil {
				return nil, lastErr
			}
			continue
		}

		if resp.StatusCode == http.StatusUnauthorized {
			if !refreshedOnce && authErrorCode(resp) == "token_expired" {
				if _, ok := c.attemptRefresh(ctx); ok {
					refreshedOnce = true
					attempt-- // this attempt did not consume a retry: retry immediately, no backoff.
					continue
				}
			}
			return nil, ErrUnauthorized
		}

		statusErr := statusError(resp)
		if statusErr == nil {
			return resp, nil
		}
		retryable := isRetryableStatus(resp.StatusCode)
		closeBody(resp)
		lastErr = statusErr
		if !retryable {
			return nil, lastErr
		}
	}

	return nil, lastErr
}

// authErrorCode reads and closes a 401 response body, returning the server's
// {"code":"..."} value or "" if the body is absent, oversized, or not JSON.
// 401 never carries anything statusError would otherwise report (it maps
// straight to ErrUnauthorized with no detail), so consuming the body here
// costs nothing the caller needed.
func authErrorCode(resp *http.Response) string {
	defer closeBody(resp)
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil {
		return ""
	}
	var body struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(data, &body) != nil {
		return ""
	}
	return body.Code
}

// attemptRefresh exchanges the client's refresh token for a new pair,
// updates the token presented on the retry, and — when the caller wired one
// — hands the new pair to onRefresh to persist. A missing refresh token, or a
// refresh call that itself fails, reports ok=false and changes nothing.
func (c *HTTPClient) attemptRefresh(ctx context.Context) (accessToken string, ok bool) {
	_, refreshToken := c.currentAuth()
	if refreshToken == "" {
		return "", false
	}
	pair, err := RefreshTokens(ctx, c.http, c.baseURL, refreshToken)
	if err != nil || pair.AccessToken == "" {
		return "", false
	}
	c.mu.Lock()
	c.token = pair.AccessToken
	if pair.RefreshToken != "" {
		c.refreshToken = pair.RefreshToken
	}
	onRefresh := c.onRefresh
	nextRefresh := c.refreshToken
	c.mu.Unlock()
	if onRefresh != nil {
		onRefresh(pair.AccessToken, nextRefresh, pair.ExpiresIn)
	}
	return pair.AccessToken, true
}

// statusError maps a response status to a sentinel error, or nil for success.
func statusError(resp *http.Response) error {
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusConflict:
		return ErrConflict
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return ErrUnauthorized
	case resp.StatusCode == http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %s", ErrIncomplete, serverMessage(resp))
	case resp.StatusCode == http.StatusRequestEntityTooLarge:
		return fmt.Errorf("%w: %s", ErrTooLarge, serverMessage(resp))
	default:
		return fmt.Errorf("server returned %s: %s", resp.Status, serverMessage(resp))
	}
}

func isRetryableStatus(code int) bool {
	return code >= 500 || code == http.StatusTooManyRequests
}

// serverMessage extracts the server's {"error": "..."} body, bounded in size.
func serverMessage(resp *http.Response) string {
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil || len(data) == 0 {
		return "no detail"
	}
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &body) == nil && body.Error != "" {
		return body.Error
	}
	return strings.TrimSpace(string(data))
}

func backoff(attempt int) time.Duration {
	d := 50 * time.Millisecond
	for i := 1; i < attempt; i++ {
		d *= 2
	}
	if d > 400*time.Millisecond {
		d = 400 * time.Millisecond
	}
	return d
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func closeBody(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	// Drain a bounded amount so keep-alive connections can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
}

// redactURL strips any query string before an error message reaches a log,
// since tokens are sometimes carried there by proxies.
func redactURL(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i] + "?<redacted>"
	}
	return raw
}

func validateFullHash(h store.Hash) error {
	if len(h) != 64 {
		return fmt.Errorf("invalid hash %q: must be 64 hex characters", h)
	}
	for _, r := range h {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			continue
		}
		return fmt.Errorf("invalid hash %q: must be lowercase hex", h)
	}
	return nil
}

// ValidateRefName mirrors the server's ref rules and, critically, rejects path
// traversal before a ref name is interpolated into a URL.
func ValidateRefName(name string) error {
	if name == "" {
		return fmt.Errorf("ref name is required")
	}
	if len(name) > 256 {
		return fmt.Errorf("ref name too long (max 256 characters)")
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("invalid ref segment %q: empty, '.', or '..' not allowed", seg)
		}
		for _, r := range seg {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			case r == ':' || r == '.' || r == '_' || r == '-':
			default:
				return fmt.Errorf("invalid ref segment %q: use letters, digits, ':', '.', '_', '-' only", seg)
			}
		}
	}
	return nil
}
