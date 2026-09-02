package identity

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Resolver is the composition's admission logic. It receives the verified
// Profile from a completed Exchange and the State the round trip carried,
// and decides what happens next: a session for an existing or new user, a
// path to an invitation, or a refusal with a reason the not-invited page can
// show. Resolver never sees a provider access token: Handlers calls it only
// after Exchange has already discarded one.
type Resolver interface {
	Resolve(ctx context.Context, p Profile, s State) (Outcome, error)
}

// Outcome is what a Resolver decides for one callback.
type Outcome struct {
	UserID   string // set when a session should be issued for this user
	Refused  bool   // true when admission is denied; Reason is shown, never Email
	Reason   string // short code, e.g. "not_invited", "wrong_organization"; never an email address
	Redirect string // where to send the browser on success; empty means the default
}

// Logger receives structured, secret-free events from this package: never a
// token, an authorization code, or a signed state string. event is a short
// dotted name (e.g. "identity.callback.exchange_failed"); fields are safe to
// log or forward as-is. A nil Logger discards events.
type Logger interface {
	LogEvent(event string, fields map[string]any)
}

// LoggerFunc adapts a function to Logger.
type LoggerFunc func(event string, fields map[string]any)

// LogEvent implements Logger.
func (f LoggerFunc) LogEvent(event string, fields map[string]any) { f(event, fields) }

type noopLogger struct{}

func (noopLogger) LogEvent(string, map[string]any) {}

// Option configures Handlers.
type Option func(*handlers)

// WithSessionNonce supplies the accessor Handlers uses to read the current
// browser session's nonce. The same accessor is used at start (to stamp the
// signed state) and at callback (to verify it): a mismatch means the state
// token is being replayed outside the browser session that requested it, and
// the callback is refused. The default accessor returns "", which only
// binds correctly if every caller shares one session (fine for NewFake-only
// tests, not for production).
func WithSessionNonce(nonce func(*http.Request) string) Option {
	return func(h *handlers) { h.sessionNonce = nonce }
}

// WithCallbackBaseURL fixes the base URL Handlers appends "/<provider>/callback"
// to when building the redirect_uri passed to a Provider's AuthURL and
// Exchange, e.g. "https://team.example.com/api/v1/auth". This must match
// whatever callback URL was registered with the provider. When unset,
// Handlers derives it from the incoming request, which is adequate for a
// single-hostname deployment but should be set explicitly behind a proxy or
// load balancer that changes the visible Host or scheme.
func WithCallbackBaseURL(base string) Option {
	return func(h *handlers) { h.callbackBaseURL = strings.TrimRight(base, "/") }
}

// WithRateLimit installs a hook consulted before Handlers acts on a start or
// callback request. It receives the request and the provider name and
// returns false to refuse the request with 429. The default hook allows
// every request; actual policy (per-IP, per-provider, ...) is the
// composition's to define.
func WithRateLimit(allow func(r *http.Request, provider string) bool) Option {
	return func(h *handlers) { h.rateLimit = allow }
}

// WithLogger installs a Logger. The default discards every event.
func WithLogger(logger Logger) Option {
	return func(h *handlers) {
		if logger != nil {
			h.logger = logger
		}
	}
}

type handlers struct {
	providers       map[string]Provider
	signer          Signer
	resolver        Resolver
	sessionNonce    func(*http.Request) string
	callbackBaseURL string
	rateLimit       func(r *http.Request, provider string) bool
	logger          Logger
}

// Handlers wires the start and callback routes for every provider in
// providers under whatever prefix the composition mounts the returned
// http.Handler at: the two routes it recognizes are the last two path
// segments being "/<name-in-providers>/start" and
// "/<name-in-providers>/callback", so mounting at "/api/v1/auth/" (per RFC
// 0005 Appendix A) or at "/o/<org>/auth/" both work unchanged. It knows
// nothing about users, sessions, or cookies: admission is entirely the
// Resolver's decision, and Handlers only ever produces a redirect based on
// the Outcome it returns.
func Handlers(providers map[string]Provider, signer Signer, resolver Resolver, opts ...Option) http.Handler {
	h := &handlers{
		providers:    providers,
		signer:       signer,
		resolver:     resolver,
		sessionNonce: func(*http.Request) string { return "" },
		rateLimit:    func(*http.Request, string) bool { return true },
		logger:       noopLogger{},
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

func (h *handlers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name, action, ok := splitProviderAction(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	provider, exists := h.providers[name]
	if !exists {
		http.NotFound(w, r)
		return
	}
	if !h.rateLimit(r, name) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	switch action {
	case "start":
		h.start(w, r, name, provider)
	case "callback":
		h.callback(w, r, name, provider)
	default:
		http.NotFound(w, r)
	}
}

func (h *handlers) start(w http.ResponseWriter, r *http.Request, name string, provider Provider) {
	invite := r.URL.Query().Get("invite")
	returnPath := r.URL.Query().Get("return")
	if !isRelativeReturn(returnPath) {
		h.logger.LogEvent("identity.start.invalid_return", map[string]any{"provider": name})
		http.Error(w, "invalid return path", http.StatusBadRequest)
		return
	}
	state := State{
		Nonce:    h.sessionNonce(r),
		Invite:   invite,
		Return:   returnPath,
		Provider: name,
		IssuedAt: time.Now().UTC(),
	}
	token, err := h.signer.Sign(state)
	if err != nil {
		h.logger.LogEvent("identity.start.sign_failed", map[string]any{"provider": name})
		http.Error(w, "sign-in unavailable", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, provider.AuthURL(token, h.redirectURL(r, name)), http.StatusFound)
}

func (h *handlers) callback(w http.ResponseWriter, r *http.Request, name string, provider Provider) {
	code := r.URL.Query().Get("code")
	rawState := r.URL.Query().Get("state")
	if code == "" || rawState == "" {
		h.logger.LogEvent("identity.callback.missing_parameters", map[string]any{"provider": name})
		h.refuse(w, r, "state_invalid")
		return
	}
	state, err := h.signer.Verify(rawState)
	if err != nil {
		h.logger.LogEvent("identity.callback.state_invalid", map[string]any{"provider": name})
		h.refuse(w, r, "state_invalid")
		return
	}
	if state.Provider != name {
		h.logger.LogEvent("identity.callback.provider_mismatch", map[string]any{"provider": name})
		h.refuse(w, r, "state_invalid")
		return
	}
	if state.Nonce != h.sessionNonce(r) {
		h.logger.LogEvent("identity.callback.nonce_mismatch", map[string]any{"provider": name})
		h.refuse(w, r, "state_invalid")
		return
	}
	if !isRelativeReturn(state.Return) {
		h.logger.LogEvent("identity.callback.invalid_return", map[string]any{"provider": name})
		h.refuse(w, r, "state_invalid")
		return
	}
	profile, err := provider.Exchange(r.Context(), code, h.redirectURL(r, name))
	if err != nil {
		h.logger.LogEvent("identity.callback.exchange_failed", map[string]any{"provider": name})
		h.errorRedirect(w, r, "provider_unavailable")
		return
	}
	outcome, err := h.resolver.Resolve(r.Context(), profile, state)
	if err != nil {
		h.logger.LogEvent("identity.callback.resolve_failed", map[string]any{"provider": name})
		h.errorRedirect(w, r, "resolve_failed")
		return
	}
	if outcome.Refused {
		h.logger.LogEvent("identity.callback.refused", map[string]any{"provider": name, "reason": outcome.Reason})
		h.refuse(w, r, outcome.Reason)
		return
	}
	dest := outcome.Redirect
	if dest == "" {
		dest = "/"
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

// refuse sends the browser to the not-invited page with a short reason code.
// reason is never an email address or any other identifying value; callers
// (and Resolver implementations) are responsible for keeping it that way.
func (h *handlers) refuse(w http.ResponseWriter, r *http.Request, reason string) {
	if reason == "" {
		reason = "not_invited"
	}
	http.Redirect(w, r, "/not-invited?reason="+url.QueryEscape(reason), http.StatusFound)
}

// errorRedirect sends the browser to a sign-in error page carrying a short
// code, for failures on the provider or resolver side of the callback (the
// "502-style" case: something upstream of this package did not work). It
// never echoes a provider response body or any other raw upstream detail.
func (h *handlers) errorRedirect(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/sign-in-error?code="+url.QueryEscape(code), http.StatusFound)
}

func (h *handlers) redirectURL(r *http.Request, name string) string {
	if h.callbackBaseURL != "" {
		return h.callbackBaseURL + "/" + name + "/callback"
	}
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded != "" {
		scheme = forwarded
	}
	return scheme + "://" + r.Host + trimLastTwoSegments(r.URL.Path) + "/" + name + "/callback"
}

// splitProviderAction reads the last two non-empty segments of an incoming
// request path as (provider, action).
func splitProviderAction(path string) (provider, action string, ok bool) {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return "", "", false
	}
	segments := strings.Split(trimmed, "/")
	if len(segments) < 2 {
		return "", "", false
	}
	provider = segments[len(segments)-2]
	action = segments[len(segments)-1]
	if provider == "" || action == "" {
		return "", "", false
	}
	return provider, action, true
}

// trimLastTwoSegments removes the trailing "/<a>/<b>" from path, used to
// recover the mount prefix Handlers was reached under from the request path
// itself when WithCallbackBaseURL was not supplied.
func trimLastTwoSegments(path string) string {
	trimmed := strings.TrimRight(path, "/")
	for i := 0; i < 2; i++ {
		idx := strings.LastIndexByte(trimmed, '/')
		if idx < 0 {
			return ""
		}
		trimmed = trimmed[:idx]
	}
	return trimmed
}
