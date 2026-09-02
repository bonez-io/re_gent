package identity

import (
	"context"
	"fmt"
	"net/url"
)

// fakeProvider is an in-memory Provider for tests and the dev composition.
// It performs no network calls: Exchange succeeds only when code is a key of
// the profiles map supplied to NewFake, and returns that Profile verbatim
// (with Provider forced to "fake" if the caller left it empty).
type fakeProvider struct {
	profiles map[string]Profile
}

// NewFake returns a Provider whose Exchange looks the authorization code up
// directly in profiles: code == map key. There is no token exchange and no
// AuthURL redirect target beyond an inert marker URL carrying the state, so
// tests and the dev composition can drive the whole Handlers flow without a
// real provider or network access.
func NewFake(profiles map[string]Profile) Provider {
	return &fakeProvider{profiles: profiles}
}

func (p *fakeProvider) Name() string { return "fake" }

func (p *fakeProvider) AuthURL(state, redirectURL string) string {
	values := url.Values{"state": {state}, "redirect_uri": {redirectURL}}
	return "https://fake.invalid/authorize?" + values.Encode()
}

func (p *fakeProvider) Exchange(_ context.Context, code, _ string) (Profile, error) {
	profile, ok := p.profiles[code]
	if !ok {
		return Profile{}, fmt.Errorf("identity: fake provider: unknown code")
	}
	if profile.Provider == "" {
		profile.Provider = "fake"
	}
	return profile, nil
}
