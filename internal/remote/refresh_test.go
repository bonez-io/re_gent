package remote

import (
	"context"
	"testing"
	"time"

	"github.com/bonez-io/re_gent/internal/remotetest"
	"github.com/bonez-io/re_gent/internal/store"
)

// The headline behaviour: a request that fails with 401 token_expired is
// retried, transparently, exactly once, after refreshing the token — and the
// caller sees a normal successful response, not an error.
func TestHTTPClientRefreshesExpiredTokenAndRetries(t *testing.T) {
	srv := remotetest.New()
	defer srv.Close()
	srv.RequireToken("stale-access-token")
	srv.ExpireToken("stale-access-token")
	srv.SetRefreshResult("a-refresh-token", remotetest.RefreshResult{
		AccessToken: "fresh-access-token", RefreshToken: "next-refresh-token", ExpiresIn: 3600,
	})

	c, err := NewHTTPClient(Config{ServerURL: srv.URL(), RepoID: "test-repo", Token: "stale-access-token", Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	var persistedAccess, persistedRefresh string
	var persistedExpiresIn int
	c.SetRefresh("a-refresh-token", func(access, refresh string, expiresIn int) {
		persistedAccess, persistedRefresh, persistedExpiresIn = access, refresh, expiresIn
	})

	content := []byte(`{"hello":"world"}`)
	got, err := c.PutObject(context.Background(), content)
	if err != nil {
		t.Fatalf("PutObject after an expired token should succeed via refresh, got: %v", err)
	}
	if want := store.HashBytes(content); got != want {
		t.Fatalf("PutObject returned %s, want %s", got, want)
	}

	if c.CurrentToken() != "fresh-access-token" {
		t.Errorf("CurrentToken() = %q, want the refreshed access token", c.CurrentToken())
	}
	if persistedAccess != "fresh-access-token" || persistedRefresh != "next-refresh-token" {
		t.Errorf("onRefresh callback got (%q, %q), want the new pair", persistedAccess, persistedRefresh)
	}
	if persistedExpiresIn != 3600 {
		t.Errorf("onRefresh callback got expiresIn=%d, want 3600", persistedExpiresIn)
	}
}

// A refresh happens at most once per request: if the newly refreshed token is
// itself rejected, the client must not loop forever trying to refresh again.
func TestHTTPClientRefreshOnlyRetriesOnce(t *testing.T) {
	srv := remotetest.New()
	defer srv.Close()
	srv.RequireToken("stale-access-token")
	srv.ExpireToken("stale-access-token")
	// The refreshed token the fake would issue is never actually accepted
	// (RequireToken still only recognises stale-access-token and whatever
	// SetRefreshResult mints as an extra token) — but the fake DOES add the
	// refreshed access token to its accepted set, so to exercise "refresh
	// didn't help" we simply configure no refresh result at all: the refresh
	// call itself fails, and the client must report ErrUnauthorized rather
	// than retrying indefinitely.
	c, err := NewHTTPClient(Config{ServerURL: srv.URL(), RepoID: "test-repo", Token: "stale-access-token", Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	c.SetRefresh("a-refresh-token-with-no-configured-result", nil)

	if _, err := c.PutObject(context.Background(), []byte("payload")); err == nil {
		t.Fatal("expected an error when the refresh call itself fails")
	}
	// Exactly one PUT should have reached the server: the client must not
	// spin retrying a refresh that will never succeed.
	if got := srv.Requests("PUT"); got != 1 {
		t.Errorf("server saw %d PUT requests, want 1 (no infinite refresh loop)", got)
	}
}

// A client with no refresh token configured behaves exactly as before RFC
// 0004: a 401 is terminal, no matter what code the body carries.
func TestHTTPClientWithoutRefreshTokenStillFailsOn401(t *testing.T) {
	srv := remotetest.New()
	defer srv.Close()
	srv.RequireToken("expected-token")

	c := newTestClient(t, srv) // no token configured at all
	if _, err := c.PutObject(context.Background(), []byte("payload")); err == nil {
		t.Fatal("expected ErrUnauthorized with no token configured")
	}
}
