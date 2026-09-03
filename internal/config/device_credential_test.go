package config

import "testing"

// The device-login flow (RFC 0004) stores an access token, a refresh token,
// and an expiry — none of which a PAT credential has ever needed. These pin
// that storage, that TokenForServer keeps returning the access token exactly
// as it does for a PAT (hooks must not need to know which kind they have),
// and that a later PAT login clears any stale device fields for that server.
func TestSetDeviceCredentialStoresAccessRefreshAndExpiry(t *testing.T) {
	cfg := &UserConfig{}
	SetDeviceCredential(cfg, "https://app.regent.dev", "access-1", "refresh-1", 3600)

	if got := TokenForServer(cfg, "https://app.regent.dev"); got != "access-1" {
		t.Fatalf("TokenForServer = %q, want the access token", got)
	}
	if got := RefreshTokenForServer(cfg, "https://app.regent.dev"); got != "refresh-1" {
		t.Fatalf("RefreshTokenForServer = %q, want the refresh token", got)
	}
	if CredentialExpired(cfg, "https://app.regent.dev") {
		t.Fatal("a token issued for 3600s should not read as expired immediately")
	}
	if len(cfg.Credentials) != 1 || cfg.Credentials[0].Kind != CredentialKindDevice {
		t.Fatalf("credential kind = %+v, want exactly one device credential", cfg.Credentials)
	}
}

func TestSetDeviceCredentialReplacesSameServerOnly(t *testing.T) {
	cfg := &UserConfig{}
	SetDeviceCredential(cfg, "https://a.example", "a-access", "a-refresh", 3600)
	SetDeviceCredential(cfg, "https://b.example", "b-access", "b-refresh", 3600)
	SetDeviceCredential(cfg, "https://a.example", "a-access-2", "a-refresh-2", 3600)

	if len(cfg.Credentials) != 2 {
		t.Fatalf("expected 2 credentials, got %d: %+v", len(cfg.Credentials), cfg.Credentials)
	}
	if got := TokenForServer(cfg, "https://a.example"); got != "a-access-2" {
		t.Fatalf("server a token = %q, want the replaced access token", got)
	}
	if got := TokenForServer(cfg, "https://b.example"); got != "b-access" {
		t.Fatalf("server b token was disturbed by server a's replacement: %q", got)
	}
}

func TestCredentialExpiredForAlreadyExpiredToken(t *testing.T) {
	cfg := &UserConfig{}
	SetDeviceCredential(cfg, "https://app.regent.dev", "access-1", "refresh-1", -1)
	if !CredentialExpired(cfg, "https://app.regent.dev") {
		t.Fatal("a token issued with a negative expiry should read as expired")
	}
}

// A personal access token has no expiry at all and must never be reported
// expired, regardless of age.
func TestCredentialExpiredIsAlwaysFalseForAPAT(t *testing.T) {
	cfg := &UserConfig{}
	SetCredential(cfg, "https://app.regent.dev", "a-pat-token-at-least-16")
	if CredentialExpired(cfg, "https://app.regent.dev") {
		t.Fatal("a PAT must never read as expired")
	}
	if got := RefreshTokenForServer(cfg, "https://app.regent.dev"); got != "" {
		t.Fatalf("a PAT has no refresh token, got %q", got)
	}
}

// Logging in again with a plain PAT for a server that previously had a
// device credential must not leave stale device fields behind.
func TestSetCredentialClearsStaleDeviceFields(t *testing.T) {
	cfg := &UserConfig{}
	SetDeviceCredential(cfg, "https://app.regent.dev", "access-1", "refresh-1", 3600)
	SetCredential(cfg, "https://app.regent.dev", "a-pat-token-at-least-16")

	if len(cfg.Credentials) != 1 {
		t.Fatalf("expected exactly one credential after re-login, got %+v", cfg.Credentials)
	}
	got := cfg.Credentials[0]
	if got.Kind != "" || got.RefreshToken != "" || got.ExpiresAt != "" {
		t.Errorf("PAT login left stale device fields: %+v", got)
	}
	if got.Token != "a-pat-token-at-least-16" {
		t.Errorf("Token = %q", got.Token)
	}
}
