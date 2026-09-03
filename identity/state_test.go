package identity

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestHMACSignerRoundTrip(t *testing.T) {
	signer := NewHMACSigner([]byte("a-32-byte-or-longer-secret-key!"), time.Hour)
	state := State{Nonce: "n1", Invite: "inv-1", Return: "/projects", Provider: "github"}
	token, err := signer.Sign(state)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	got, err := signer.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.Nonce != state.Nonce || got.Invite != state.Invite || got.Return != state.Return || got.Provider != state.Provider {
		t.Fatalf("round trip mismatch: got %+v, want fields from %+v", got, state)
	}
	if got.IssuedAt.IsZero() {
		t.Fatal("IssuedAt was not stamped")
	}
}

func TestHMACSignerRejectsTamperedPayload(t *testing.T) {
	signer := NewHMACSigner([]byte("key-one-key-one-key-one-key-one"), time.Hour)
	token, err := signer.Sign(State{Nonce: "n1", Provider: "github"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	payload, mac, ok := strings.Cut(token, ".")
	if !ok {
		t.Fatalf("token has no separator: %q", token)
	}
	tampered := payload + "x." + mac
	if _, err := signer.Verify(tampered); !errors.Is(err, ErrStateSignature) {
		t.Fatalf("Verify(tampered payload) = %v, want ErrStateSignature", err)
	}
}

func TestHMACSignerRejectsWrongKey(t *testing.T) {
	signer := NewHMACSigner([]byte("key-one-key-one-key-one-key-one"), time.Hour)
	other := NewHMACSigner([]byte("key-two-key-two-key-two-key-two"), time.Hour)
	token, err := signer.Sign(State{Nonce: "n1", Provider: "github"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := other.Verify(token); !errors.Is(err, ErrStateSignature) {
		t.Fatalf("Verify with wrong key = %v, want ErrStateSignature", err)
	}
}

func TestHMACSignerRejectsExpired(t *testing.T) {
	signer := NewHMACSigner([]byte("key-one-key-one-key-one-key-one"), time.Minute)
	old := State{Nonce: "n1", Provider: "github", IssuedAt: time.Now().Add(-time.Hour)}
	token, err := signer.Sign(old)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := signer.Verify(token); !errors.Is(err, ErrStateExpired) {
		t.Fatalf("Verify(expired) = %v, want ErrStateExpired", err)
	}
}

func TestHMACSignerRejectsMalformedTokens(t *testing.T) {
	signer := NewHMACSigner([]byte("key-one-key-one-key-one-key-one"), time.Hour)
	cases := []string{
		"",
		"no-dot-in-here",
		".",
		"payload.",
		".mac",
		"not-base64!!!.not-base64!!!",
	}
	for _, c := range cases {
		if _, err := signer.Verify(c); err == nil {
			t.Errorf("Verify(%q) succeeded, want an error", c)
		}
	}
}

func TestHMACSignerEmptyKeyRefusesToSign(t *testing.T) {
	signer := NewHMACSigner(nil, time.Hour)
	if _, err := signer.Sign(State{Provider: "github"}); err == nil {
		t.Fatal("Sign with empty key succeeded, want an error")
	}
}

func TestHMACSignerZeroTTLNeverExpires(t *testing.T) {
	signer := NewHMACSigner([]byte("key-one-key-one-key-one-key-one"), 0)
	old := State{Provider: "github", IssuedAt: time.Now().Add(-24 * time.Hour)}
	token, err := signer.Sign(old)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := signer.Verify(token); err != nil {
		t.Fatalf("Verify with zero TTL = %v, want nil", err)
	}
}

func TestIsRelativeReturn(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"", true},
		{"/", true},
		{"/projects", true},
		{"/projects?x=1", true},
		{"http://evil.example/", false},
		{"https://evil.example/", false},
		{"//evil.example/", false},
		{"/\\evil.example/", false},
		{"evil.example", false},
		{"javascript:alert(1)", false},
	}
	for _, c := range cases {
		if got := isRelativeReturn(c.path); got != c.want {
			t.Errorf("isRelativeReturn(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
