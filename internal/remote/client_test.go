package remote

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/regent-vcs/regent/internal/remotetest"
	"github.com/regent-vcs/regent/internal/store"
)

func newTestClient(t *testing.T, srv *remotetest.Server) *HTTPClient {
	t.Helper()
	c, err := NewHTTPClient(Config{ServerURL: srv.URL(), RepoID: "test-repo", Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	return c
}

func TestClientObjectRoundTrip(t *testing.T) {
	srv := remotetest.New()
	defer srv.Close()
	c := newTestClient(t, srv)
	ctx := context.Background()

	content := []byte(`{"hello":"world"}`)
	want := store.HashBytes(content)

	present, err := c.HasObject(ctx, want)
	if err != nil {
		t.Fatalf("HasObject: %v", err)
	}
	if present {
		t.Fatal("object should not exist yet")
	}

	got, err := c.PutObject(ctx, content)
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if got != want {
		t.Fatalf("PutObject returned %s, want %s", got, want)
	}

	// Re-uploading identical bytes is a no-op, not an error: content addressing
	// makes every upload idempotent, which is what makes retries safe.
	if _, err := c.PutObject(ctx, content); err != nil {
		t.Fatalf("second PutObject: %v", err)
	}

	present, err = c.HasObject(ctx, want)
	if err != nil || !present {
		t.Fatalf("HasObject after put = %v, %v", present, err)
	}

	data, err := c.GetObject(ctx, want)
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if string(data) != string(content) {
		t.Fatalf("GetObject returned %q, want %q", data, content)
	}
}

func TestClientRefCASAndConflict(t *testing.T) {
	srv := remotetest.New()
	defer srv.Close()
	c := newTestClient(t, srv)
	ctx := context.Background()

	if _, err := c.GetRef(ctx, "sessions/absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRef on missing ref = %v, want ErrNotFound", err)
	}

	first, err := c.PutObject(ctx, []byte(`{"tree":""}`))
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	second, err := c.PutObject(ctx, []byte(`{"tree":"","n":2}`))
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	if err := c.UpdateRef(ctx, "sessions/a", "", first); err != nil {
		t.Fatalf("UpdateRef create: %v", err)
	}
	got, err := c.GetRef(ctx, "sessions/a")
	if err != nil || got != first {
		t.Fatalf("GetRef = %s, %v; want %s", got, err, first)
	}

	// A stale expected value must lose the race rather than clobber.
	if err := c.UpdateRef(ctx, "sessions/a", "", second); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CAS = %v, want ErrConflict", err)
	}
	if srv.Ref("sessions/a") != first {
		t.Fatal("losing CAS must not change the server ref")
	}
}

func TestClientStatusMapping(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   error
	}{
		{"conflict", http.StatusConflict, ErrConflict},
		{"unprocessable", http.StatusUnprocessableEntity, ErrIncomplete},
		{"unauthorized", http.StatusUnauthorized, ErrUnauthorized},
		{"forbidden", http.StatusForbidden, ErrUnauthorized},
		{"too large", http.StatusRequestEntityTooLarge, ErrTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := remotetest.New()
			defer srv.Close()
			c := newTestClient(t, srv)

			srv.InjectFaults(remotetest.Fault{Status: tt.status})
			_, err := c.PutObject(context.Background(), []byte("payload"))
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestClientRetriesServerErrorsButNotClientErrors(t *testing.T) {
	srv := remotetest.New()
	defer srv.Close()
	c := newTestClient(t, srv)

	// Two transient failures then success: the request must survive them.
	srv.InjectFaults(
		remotetest.Fault{Status: http.StatusInternalServerError},
		remotetest.Fault{Status: http.StatusServiceUnavailable},
	)
	if _, err := c.PutObject(context.Background(), []byte("payload")); err != nil {
		t.Fatalf("PutObject should have retried through 5xx: %v", err)
	}
	if got := srv.Requests(http.MethodPut); got != 3 {
		t.Fatalf("PUT attempts = %d, want 3", got)
	}

	// A 401 is terminal: retrying a bad token only wastes the agent's turn.
	srv2 := remotetest.New()
	defer srv2.Close()
	c2 := newTestClient(t, srv2)
	srv2.RequireToken("expected-token")
	if _, err := c2.PutObject(context.Background(), []byte("payload")); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("error = %v, want ErrUnauthorized", err)
	}
	if got := srv2.Requests(http.MethodPut); got != 1 {
		t.Fatalf("PUT attempts = %d, want 1 (no retry on auth failure)", got)
	}
}

func TestClientAuthenticatesWithBearerToken(t *testing.T) {
	srv := remotetest.New()
	defer srv.Close()
	srv.RequireToken("s3cret-token")

	c, err := NewHTTPClient(Config{ServerURL: srv.URL(), RepoID: "test-repo", Token: "s3cret-token"})
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	if _, err := c.PutObject(context.Background(), []byte("payload")); err != nil {
		t.Fatalf("PutObject with valid token: %v", err)
	}
}

func TestClientRejectsTruncatedObject(t *testing.T) {
	srv := remotetest.New()
	defer srv.Close()
	c := newTestClient(t, srv)
	ctx := context.Background()

	h, err := c.PutObject(ctx, []byte("the full object body"))
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	// A connection that cuts the response short must never be mistaken for the
	// real object: the hash check is the integrity guarantee.
	srv.InjectFaults(remotetest.Fault{Truncate: true})
	if _, err := c.GetObject(ctx, h); err == nil || !strings.Contains(err.Error(), "integrity check") {
		t.Fatalf("GetObject on truncated body = %v, want an integrity failure", err)
	}
}

func TestClientOfflineIsATransportError(t *testing.T) {
	srv := remotetest.New()
	defer srv.Close()
	c := newTestClient(t, srv)
	srv.SetOffline(true)

	_, err := c.GetRef(context.Background(), "sessions/a")
	if err == nil {
		t.Fatal("expected a transport error while offline")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatal("an unreachable server must not look like a missing ref")
	}
}

func TestClientHonoursContextDeadline(t *testing.T) {
	srv := remotetest.New()
	defer srv.Close()
	c := newTestClient(t, srv)
	srv.SetOffline(true)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := c.GetRef(ctx, "sessions/a"); err == nil {
		t.Fatal("expected an error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("request took %v; the deadline must bound retries", elapsed)
	}
}

func TestValidateRefNameRejectsTraversalAndBadCharacters(t *testing.T) {
	valid := []string{"sessions/claude_code--abc", "sessions/a.b-c_d", "sessions/codex_cli--1"}
	for _, name := range valid {
		if err := ValidateRefName(name); err != nil {
			t.Errorf("ValidateRefName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{"", "sessions/../etc", "sessions/.", "sessions//a", "sessions/a b", "sessions/a%2Fb", "sessions/" + strings.Repeat("a", 300)}
	for _, name := range invalid {
		if err := ValidateRefName(name); err == nil {
			t.Errorf("ValidateRefName(%q) = nil, want an error", name)
		}
	}
}

func TestClientRejectsMalformedHashes(t *testing.T) {
	srv := remotetest.New()
	defer srv.Close()
	c := newTestClient(t, srv)
	ctx := context.Background()

	for _, h := range []store.Hash{"", "abc", store.Hash(strings.Repeat("A", 64)), store.Hash(strings.Repeat("z", 64))} {
		if _, err := c.GetObject(ctx, h); err == nil {
			t.Errorf("GetObject(%q) = nil error, want a validation failure", h)
		}
		if _, err := c.HasObject(ctx, h); err == nil {
			t.Errorf("HasObject(%q) = nil error, want a validation failure", h)
		}
	}
}
