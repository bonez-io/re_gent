package identity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// stubResolver is a Resolver whose decision and error are fixed per test; it
// also records the last Profile and State it was called with so tests can
// assert Handlers passed the right values through.
type stubResolver struct {
	outcome     Outcome
	err         error
	calls       int
	lastProfile Profile
	lastState   State
}

func (s *stubResolver) Resolve(_ context.Context, p Profile, st State) (Outcome, error) {
	s.calls++
	s.lastProfile = p
	s.lastState = st
	return s.outcome, s.err
}

// nonceHeaderAccessor reads the browser session's nonce from a test-only
// header, so tests can control it independently for the start and callback
// legs of a round trip without standing up real cookies.
func nonceHeaderAccessor(r *http.Request) string { return r.Header.Get("X-Test-Nonce") }

func newTestHandlers(providers map[string]Provider, signer Signer, resolver Resolver, extra ...Option) http.Handler {
	opts := append([]Option{WithSessionNonce(nonceHeaderAccessor)}, extra...)
	return Handlers(providers, signer, resolver, opts...)
}

func doStart(t *testing.T, h http.Handler, provider, nonce, invite, returnPath string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/api/v1/auth/" + provider + "/start"
	q := url.Values{}
	if invite != "" {
		q.Set("invite", invite)
	}
	if returnPath != "" {
		q.Set("return", returnPath)
	}
	if enc := q.Encode(); enc != "" {
		target += "?" + enc
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("X-Test-Nonce", nonce)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func stateFromRedirect(t *testing.T, location string) string {
	t.Helper()
	u, err := url.Parse(location)
	if err != nil {
		t.Fatalf("start redirected to an unparseable URL %q: %v", location, err)
	}
	state := u.Query().Get("state")
	if state == "" {
		t.Fatalf("start redirect %q carried no state", location)
	}
	return state
}

func doCallback(t *testing.T, h http.Handler, provider, nonce, code, state string) *httptest.ResponseRecorder {
	t.Helper()
	q := url.Values{}
	if code != "" {
		q.Set("code", code)
	}
	if state != "" {
		q.Set("state", state)
	}
	target := "/api/v1/auth/" + provider + "/callback?" + q.Encode()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("X-Test-Nonce", nonce)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHandlersHappyPath(t *testing.T) {
	profiles := map[string]Profile{
		"good-code": {Provider: "fake", Subject: "u1", Email: "person@example.com", EmailVerified: true},
	}
	providers := map[string]Provider{"fake": NewFake(profiles)}
	signer := NewHMACSigner([]byte("k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k"), time.Hour)
	resolver := &stubResolver{outcome: Outcome{UserID: "u1", Redirect: "/projects"}}
	h := newTestHandlers(providers, signer, resolver)

	startRec := doStart(t, h, "fake", "session-nonce", "invite-1", "/dashboard")
	if startRec.Code != http.StatusFound {
		t.Fatalf("start status = %d, body %s", startRec.Code, startRec.Body.String())
	}
	state := stateFromRedirect(t, startRec.Header().Get("Location"))

	callbackRec := doCallback(t, h, "fake", "session-nonce", "good-code", state)
	if callbackRec.Code != http.StatusFound {
		t.Fatalf("callback status = %d, body %s", callbackRec.Code, callbackRec.Body.String())
	}
	if loc := callbackRec.Header().Get("Location"); loc != "/projects" {
		t.Fatalf("callback redirected to %q, want /projects", loc)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver called %d times, want 1", resolver.calls)
	}
	if resolver.lastProfile.Email != "person@example.com" {
		t.Fatalf("resolver saw profile %+v", resolver.lastProfile)
	}
	if resolver.lastState.Invite != "invite-1" || resolver.lastState.Return != "/dashboard" {
		t.Fatalf("resolver saw state %+v", resolver.lastState)
	}
}

func TestHandlersHappyPathDefaultRedirect(t *testing.T) {
	providers := map[string]Provider{"fake": NewFake(map[string]Profile{"c": {}})}
	signer := NewHMACSigner([]byte("k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k"), time.Hour)
	resolver := &stubResolver{outcome: Outcome{UserID: "u1"}} // no Redirect set
	h := newTestHandlers(providers, signer, resolver)

	startRec := doStart(t, h, "fake", "n", "", "")
	state := stateFromRedirect(t, startRec.Header().Get("Location"))
	rec := doCallback(t, h, "fake", "n", "c", state)
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Fatalf("callback redirected to %q, want / (the default)", loc)
	}
}

func TestHandlersRefusalRedirectsToNotInvited(t *testing.T) {
	providers := map[string]Provider{"fake": NewFake(map[string]Profile{"c": {Email: "nope@example.com"}})}
	signer := NewHMACSigner([]byte("k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k"), time.Hour)
	resolver := &stubResolver{outcome: Outcome{Refused: true, Reason: "not_invited"}}
	h := newTestHandlers(providers, signer, resolver)

	startRec := doStart(t, h, "fake", "n", "", "")
	state := stateFromRedirect(t, startRec.Header().Get("Location"))
	rec := doCallback(t, h, "fake", "n", "c", state)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if loc != "/not-invited?reason=not_invited" {
		t.Fatalf("Location = %q", loc)
	}
	// The refusal reason must never be (or contain) the profile's email.
	if u, err := url.Parse(loc); err == nil && u.Query().Get("reason") == "nope@example.com" {
		t.Fatal("refusal reason leaked the email address")
	}
}

func TestHandlersRefusalDefaultReason(t *testing.T) {
	providers := map[string]Provider{"fake": NewFake(map[string]Profile{"c": {}})}
	signer := NewHMACSigner([]byte("k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k"), time.Hour)
	resolver := &stubResolver{outcome: Outcome{Refused: true}} // Reason left empty
	h := newTestHandlers(providers, signer, resolver)

	startRec := doStart(t, h, "fake", "n", "", "")
	state := stateFromRedirect(t, startRec.Header().Get("Location"))
	rec := doCallback(t, h, "fake", "n", "c", state)
	if loc := rec.Header().Get("Location"); loc != "/not-invited?reason=not_invited" {
		t.Fatalf("Location = %q, want the default reason filled in", loc)
	}
}

func TestHandlersMissingCodeOrState(t *testing.T) {
	providers := map[string]Provider{"fake": NewFake(map[string]Profile{"c": {}})}
	signer := NewHMACSigner([]byte("k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k"), time.Hour)
	resolver := &stubResolver{outcome: Outcome{Redirect: "/ok"}}
	h := newTestHandlers(providers, signer, resolver)

	startRec := doStart(t, h, "fake", "n", "", "")
	state := stateFromRedirect(t, startRec.Header().Get("Location"))

	for _, tc := range []struct{ code, state string }{
		{"", state},
		{"c", ""},
		{"", ""},
	} {
		rec := doCallback(t, h, "fake", "n", tc.code, tc.state)
		if loc := rec.Header().Get("Location"); loc != "/not-invited?reason=state_invalid" {
			t.Errorf("code=%q state=%q: Location = %q", tc.code, tc.state, loc)
		}
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver should not have been called, got %d calls", resolver.calls)
	}
}

func TestHandlersInvalidStateSignature(t *testing.T) {
	providers := map[string]Provider{"fake": NewFake(map[string]Profile{"c": {}})}
	signer := NewHMACSigner([]byte("k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k"), time.Hour)
	resolver := &stubResolver{outcome: Outcome{Redirect: "/ok"}}
	h := newTestHandlers(providers, signer, resolver)

	rec := doCallback(t, h, "fake", "n", "c", "garbage-not-a-real-token")
	if loc := rec.Header().Get("Location"); loc != "/not-invited?reason=state_invalid" {
		t.Fatalf("Location = %q", loc)
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver should not have been called, got %d calls", resolver.calls)
	}
}

func TestHandlersNonceMismatchIsRefused(t *testing.T) {
	providers := map[string]Provider{"fake": NewFake(map[string]Profile{"c": {}})}
	signer := NewHMACSigner([]byte("k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k"), time.Hour)
	resolver := &stubResolver{outcome: Outcome{Redirect: "/ok"}}
	h := newTestHandlers(providers, signer, resolver)

	startRec := doStart(t, h, "fake", "browser-A-nonce", "", "")
	state := stateFromRedirect(t, startRec.Header().Get("Location"))

	// The callback arrives with a different session nonce than the one that
	// started the flow — as if the signed state were replayed into another
	// browser.
	rec := doCallback(t, h, "fake", "browser-B-nonce", "c", state)
	if loc := rec.Header().Get("Location"); loc != "/not-invited?reason=state_invalid" {
		t.Fatalf("Location = %q, want a state_invalid refusal", loc)
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver should not have been called, got %d calls", resolver.calls)
	}
}

func TestHandlersProviderMismatchIsRefused(t *testing.T) {
	providers := map[string]Provider{
		"fake":  NewFake(map[string]Profile{"c": {}}),
		"fake2": NewFake(map[string]Profile{"c": {}}),
	}
	signer := NewHMACSigner([]byte("k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k"), time.Hour)
	resolver := &stubResolver{outcome: Outcome{Redirect: "/ok"}}
	h := newTestHandlers(providers, signer, resolver)

	startRec := doStart(t, h, "fake", "n", "", "")
	state := stateFromRedirect(t, startRec.Header().Get("Location"))

	// The state was signed for "fake" but presented at "fake2"'s callback.
	rec := doCallback(t, h, "fake2", "n", "c", state)
	if loc := rec.Header().Get("Location"); loc != "/not-invited?reason=state_invalid" {
		t.Fatalf("Location = %q", loc)
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver should not have been called, got %d calls", resolver.calls)
	}
}

func TestHandlersOpenRedirectRejectedAtStart(t *testing.T) {
	providers := map[string]Provider{"fake": NewFake(map[string]Profile{"c": {}})}
	signer := NewHMACSigner([]byte("k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k"), time.Hour)
	resolver := &stubResolver{}
	h := newTestHandlers(providers, signer, resolver)

	for _, bad := range []string{
		"http://evil.example/steal",
		"https://evil.example/",
		"//evil.example/",
		"/\\evil.example/",
	} {
		rec := doStart(t, h, "fake", "n", "", bad)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("return=%q: status = %d, want 400", bad, rec.Code)
		}
	}
}

func TestHandlersOpenRedirectRejectedAtCallback(t *testing.T) {
	// A state signed with a bad Return (e.g. by an older or misbehaving
	// signer) must still be caught on the way out, not just on the way in.
	providers := map[string]Provider{"fake": NewFake(map[string]Profile{"c": {}})}
	signer := NewHMACSigner([]byte("k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k"), time.Hour)
	resolver := &stubResolver{outcome: Outcome{Redirect: "/ok"}}
	h := newTestHandlers(providers, signer, resolver)

	token, err := signer.Sign(State{Nonce: "n", Provider: "fake", Return: "https://evil.example/steal"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	rec := doCallback(t, h, "fake", "n", "c", token)
	if loc := rec.Header().Get("Location"); loc != "/not-invited?reason=state_invalid" {
		t.Fatalf("Location = %q", loc)
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver should not have been called, got %d calls", resolver.calls)
	}
}

func TestHandlersExchangeFailureRedirectsWithoutEchoingProvider(t *testing.T) {
	providers := map[string]Provider{"fake": NewFake(map[string]Profile{"good": {}})}
	signer := NewHMACSigner([]byte("k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k"), time.Hour)
	resolver := &stubResolver{outcome: Outcome{Redirect: "/ok"}}
	h := newTestHandlers(providers, signer, resolver)

	startRec := doStart(t, h, "fake", "n", "", "")
	state := stateFromRedirect(t, startRec.Header().Get("Location"))

	// "bad-code" is not a key in the fake provider's profile map, so
	// Exchange fails.
	rec := doCallback(t, h, "fake", "n", "bad-code", state)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if loc != "/sign-in-error?code=provider_unavailable" {
		t.Fatalf("Location = %q", loc)
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver should not have been called, got %d calls", resolver.calls)
	}
}

func TestHandlersResolverErrorRedirects(t *testing.T) {
	providers := map[string]Provider{"fake": NewFake(map[string]Profile{"c": {}})}
	signer := NewHMACSigner([]byte("k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k"), time.Hour)
	resolver := &stubResolver{err: errors.New("store unavailable")}
	h := newTestHandlers(providers, signer, resolver)

	startRec := doStart(t, h, "fake", "n", "", "")
	state := stateFromRedirect(t, startRec.Header().Get("Location"))
	rec := doCallback(t, h, "fake", "n", "c", state)
	if loc := rec.Header().Get("Location"); loc != "/sign-in-error?code=resolve_failed" {
		t.Fatalf("Location = %q", loc)
	}
}

func TestHandlersUnknownProviderNotFound(t *testing.T) {
	providers := map[string]Provider{"fake": NewFake(nil)}
	signer := NewHMACSigner([]byte("k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k"), time.Hour)
	h := newTestHandlers(providers, signer, &stubResolver{})

	rec := doStart(t, h, "nope", "n", "", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandlersRejectsNonGET(t *testing.T) {
	providers := map[string]Provider{"fake": NewFake(nil)}
	signer := NewHMACSigner([]byte("k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k"), time.Hour)
	h := newTestHandlers(providers, signer, &stubResolver{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/fake/start", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestHandlersRateLimitHook(t *testing.T) {
	providers := map[string]Provider{"fake": NewFake(nil)}
	signer := NewHMACSigner([]byte("k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k"), time.Hour)
	h := newTestHandlers(providers, signer, &stubResolver{}, WithRateLimit(func(*http.Request, string) bool { return false }))

	rec := doStart(t, h, "fake", "n", "", "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
}

func TestHandlersLoggerNeverSeesTokenOrCode(t *testing.T) {
	providers := map[string]Provider{"fake": NewFake(map[string]Profile{"good": {}})}
	signer := NewHMACSigner([]byte("k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k-k"), time.Hour)
	resolver := &stubResolver{outcome: Outcome{Redirect: "/ok"}}

	var events []map[string]any
	logger := LoggerFunc(func(event string, fields map[string]any) {
		events = append(events, fields)
		_ = event
	})
	h := newTestHandlers(providers, signer, resolver, WithLogger(logger))

	startRec := doStart(t, h, "fake", "n", "", "")
	state := stateFromRedirect(t, startRec.Header().Get("Location"))
	// Deliberately fail the exchange, which is where a naive implementation
	// would be tempted to log the raw code.
	doCallback(t, h, "fake", "n", "unknown-code", state)

	for _, fields := range events {
		for key, value := range fields {
			if key == "code" || key == "token" || key == "access_token" || key == "state" {
				t.Fatalf("logger observed a secret-shaped field %q = %v", key, value)
			}
			if s, ok := value.(string); ok && s == "unknown-code" {
				t.Fatalf("logger observed the raw authorization code")
			}
			if s, ok := value.(string); ok && s == state {
				t.Fatalf("logger observed the raw state token")
			}
		}
	}
}
