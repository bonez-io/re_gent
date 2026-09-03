package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// State carries what must survive the OAuth round trip: which provider
// started the flow, the invitation and return path the browser arrived with,
// and a nonce binding the token to the browser session that requested it.
// Handlers signs a State before redirecting to the provider and verifies it
// on the callback; the package has no cookie of its own, so the nonce is
// supplied by the composition through the session-nonce accessor Option.
type State struct {
	Nonce    string
	Invite   string
	Return   string
	Provider string
	IssuedAt time.Time
}

// Signer signs and verifies State values. Verify rejects a bad signature and
// an expired token. It does not see the current request, so checking the
// nonce against the caller's browser session is the caller's (Handlers')
// responsibility, not the Signer's.
type Signer interface {
	Sign(State) (string, error)
	Verify(string) (State, error)
}

// Errors returned by the HMAC signer. Composition code that wants to
// distinguish "tampered or foreign" from "just too old" can use errors.Is.
var (
	ErrStateSignature = errors.New("identity: state signature invalid")
	ErrStateExpired   = errors.New("identity: state expired")
)

type hmacSigner struct {
	key []byte
	ttl time.Duration
}

// NewHMACSigner returns a Signer that HMAC-SHA256 signs the JSON encoding of
// a State, base64url encoded. Each signed token carries its own IssuedAt so
// Verify can reject anything older than ttl regardless of clock skew between
// signing processes. key is copied and should be at least 32 random bytes;
// it is owned entirely by the composition, which is responsible for keeping
// it stable across the lifetime of any token it wants to still verify (e.g.
// across a process restart) and rotating it when it wants to invalidate
// every outstanding state.
func NewHMACSigner(key []byte, ttl time.Duration) Signer {
	cp := make([]byte, len(key))
	copy(cp, key)
	return &hmacSigner{key: cp, ttl: ttl}
}

func (s *hmacSigner) Sign(state State) (string, error) {
	if len(s.key) == 0 {
		return "", errors.New("identity: HMAC signer has an empty key")
	}
	if state.IssuedAt.IsZero() {
		state.IssuedAt = time.Now().UTC()
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	mac := s.sign(encodedPayload)
	encodedMAC := base64.RawURLEncoding.EncodeToString(mac)
	return encodedPayload + "." + encodedMAC, nil
}

func (s *hmacSigner) Verify(token string) (State, error) {
	encodedPayload, encodedMAC, ok := strings.Cut(token, ".")
	if !ok || encodedPayload == "" || encodedMAC == "" {
		return State{}, ErrStateSignature
	}
	gotMAC, err := base64.RawURLEncoding.DecodeString(encodedMAC)
	if err != nil {
		return State{}, ErrStateSignature
	}
	wantMAC := s.sign(encodedPayload)
	if subtle.ConstantTimeCompare(gotMAC, wantMAC) != 1 {
		return State{}, ErrStateSignature
	}
	payload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return State{}, ErrStateSignature
	}
	var state State
	if err := json.Unmarshal(payload, &state); err != nil {
		return State{}, ErrStateSignature
	}
	if s.ttl > 0 && time.Since(state.IssuedAt) > s.ttl {
		return State{}, ErrStateExpired
	}
	return state, nil
}

func (s *hmacSigner) sign(encodedPayload string) []byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(encodedPayload))
	return mac.Sum(nil)
}

// isRelativeReturn reports whether path is safe to redirect a browser to
// after sign-in: a same-origin, root-relative path with no scheme or host of
// its own. This is the open-redirect guard: Return travels inside a signed
// token controlled by this package, but its value originates from an
// unauthenticated query parameter, so it must be constrained on the way in
// (start) and re-checked on the way out (callback) in case a signer key
// rotation or a future relaxation of Sign ever lets an unchecked value
// through.
func isRelativeReturn(path string) bool {
	if path == "" {
		return true
	}
	if !strings.HasPrefix(path, "/") {
		return false
	}
	// "//host/path" and "/\host/path" are both browser-honored ways to smuggle
	// a scheme-relative absolute URL through something that merely checks for
	// a leading "/".
	if strings.HasPrefix(path, "//") || strings.HasPrefix(path, "/\\") {
		return false
	}
	return true
}
