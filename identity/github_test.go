package identity

import (
	"context"
	"net/url"
	"strings"
	"testing"
)

func TestNewGitHubDefaultEndpoints(t *testing.T) {
	p := NewGitHub(Config{ClientID: "id", ClientSecret: "secret"}).(*githubProvider)
	if p.authorizeURL != "https://github.com/login/oauth/authorize" {
		t.Errorf("authorizeURL = %q", p.authorizeURL)
	}
	if p.tokenURL != "https://github.com/login/oauth/access_token" {
		t.Errorf("tokenURL = %q", p.tokenURL)
	}
	if p.apiBaseURL != "https://api.github.com" {
		t.Errorf("apiBaseURL = %q", p.apiBaseURL)
	}
	if got := strings.Join(p.cfg.Scopes, " "); got != "read:user user:email" {
		t.Errorf("default scopes = %q", got)
	}
}

func TestNewGitHubEnterpriseEndpoints(t *testing.T) {
	p := NewGitHub(Config{
		ClientID:     "id",
		ClientSecret: "secret",
		BaseURL:      "https://github.example.com/",
		ReadOrgs:     true,
	}).(*githubProvider)
	if p.authorizeURL != "https://github.example.com/login/oauth/authorize" {
		t.Errorf("authorizeURL = %q", p.authorizeURL)
	}
	if p.tokenURL != "https://github.example.com/login/oauth/access_token" {
		t.Errorf("tokenURL = %q", p.tokenURL)
	}
	if p.apiBaseURL != "https://github.example.com/api/v3" {
		t.Errorf("apiBaseURL = %q", p.apiBaseURL)
	}
	if got := strings.Join(p.cfg.Scopes, " "); got != "read:user user:email read:org" {
		t.Errorf("default scopes with ReadOrgs = %q", got)
	}
}

func TestGitHubAuthURL(t *testing.T) {
	p := NewGitHub(Config{ClientID: "abc123"})
	raw := p.AuthURL("signed-state", "https://team.example.com/api/v1/auth/github/callback")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("AuthURL produced an unparseable URL: %v", err)
	}
	q := u.Query()
	if q.Get("client_id") != "abc123" {
		t.Errorf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("state") != "signed-state" {
		t.Errorf("state = %q", q.Get("state"))
	}
	if q.Get("redirect_uri") != "https://team.example.com/api/v1/auth/github/callback" {
		t.Errorf("redirect_uri = %q", q.Get("redirect_uri"))
	}
}

func TestGitHubExchangeAgainstFakeServer(t *testing.T) {
	server := NewFakeServer(t)
	server.SetFixture("good-code", FakeGitHubFixture{
		User: FakeGitHubUser{ID: 42, Login: "octocat", Name: "The Octocat", AvatarURL: "https://example.com/a.png"},
		Emails: []FakeGitHubEmail{
			{Email: "unverified@example.com", Primary: false, Verified: false},
			{Email: "secondary@example.com", Primary: false, Verified: true},
			{Email: "primary@example.com", Primary: true, Verified: true},
		},
	})
	provider := NewGitHub(Config{ClientID: "id", ClientSecret: "secret", BaseURL: server.URL()})

	profile, err := provider.Exchange(context.Background(), "good-code", "https://team.example.com/callback")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	want := Profile{
		Provider:      "github",
		Subject:       "42",
		Login:         "octocat",
		DisplayName:   "The Octocat",
		Email:         "primary@example.com",
		EmailVerified: true,
		AvatarURL:     "https://example.com/a.png",
	}
	if profile.Provider != want.Provider || profile.Subject != want.Subject || profile.Login != want.Login ||
		profile.DisplayName != want.DisplayName || profile.Email != want.Email || profile.EmailVerified != want.EmailVerified ||
		profile.AvatarURL != want.AvatarURL {
		t.Fatalf("Exchange profile = %+v, want %+v", profile, want)
	}
	if len(profile.Orgs) != 0 {
		t.Fatalf("Orgs = %v, want none (ReadOrgs was false)", profile.Orgs)
	}
}

func TestGitHubExchangeEnterpriseBaseURLPaths(t *testing.T) {
	server := NewFakeServer(t)
	server.SetFixture("ghes-code", FakeGitHubFixture{
		User:   FakeGitHubUser{ID: 7, Login: "ghes-user"},
		Emails: []FakeGitHubEmail{{Email: "ghes@example.com", Primary: true, Verified: true}},
	})
	// A trailing slash on BaseURL should be tolerated, exactly as it is for
	// github.com.
	provider := NewGitHub(Config{ClientID: "id", ClientSecret: "secret", BaseURL: server.URL() + "/"})
	profile, err := provider.Exchange(context.Background(), "ghes-code", "https://team.example.com/callback")
	if err != nil {
		t.Fatalf("Exchange against GHES-style paths: %v", err)
	}
	if profile.Subject != "7" || profile.Login != "ghes-user" {
		t.Fatalf("Exchange profile = %+v", profile)
	}
}

func TestGitHubExchangeReadOrgs(t *testing.T) {
	server := NewFakeServer(t)
	server.SetFixture("org-code", FakeGitHubFixture{
		User:   FakeGitHubUser{ID: 9, Login: "org-user"},
		Emails: []FakeGitHubEmail{{Email: "org@example.com", Primary: true, Verified: true}},
		Orgs:   []string{"acme", "widgets-inc"},
	})
	provider := NewGitHub(Config{ClientID: "id", ClientSecret: "secret", BaseURL: server.URL(), ReadOrgs: true})
	profile, err := provider.Exchange(context.Background(), "org-code", "https://team.example.com/callback")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if strings.Join(profile.Orgs, ",") != "acme,widgets-inc" {
		t.Fatalf("Orgs = %v", profile.Orgs)
	}
}

func TestGitHubExchangeRejectsUnknownCode(t *testing.T) {
	server := NewFakeServer(t)
	provider := NewGitHub(Config{ClientID: "id", ClientSecret: "secret", BaseURL: server.URL()})
	if _, err := provider.Exchange(context.Background(), "no-such-code", "https://team.example.com/callback"); err == nil {
		t.Fatal("Exchange with an unknown code succeeded, want an error")
	}
}

func TestChooseVerifiedPrimaryEmail(t *testing.T) {
	cases := []struct {
		name     string
		emails   []githubEmail
		wantAddr string
		wantOK   bool
	}{
		{
			name: "verified primary wins",
			emails: []githubEmail{
				{Email: "a@example.com", Primary: false, Verified: true},
				{Email: "b@example.com", Primary: true, Verified: true},
			},
			wantAddr: "b@example.com",
			wantOK:   true,
		},
		{
			name: "unverified primary is skipped for a verified secondary",
			emails: []githubEmail{
				{Email: "unverified-primary@example.com", Primary: true, Verified: false},
				{Email: "verified-secondary@example.com", Primary: false, Verified: true},
			},
			wantAddr: "verified-secondary@example.com",
			wantOK:   true,
		},
		{
			name: "nothing verified",
			emails: []githubEmail{
				{Email: "a@example.com", Primary: true, Verified: false},
			},
			wantAddr: "",
			wantOK:   false,
		},
		{
			name:     "no emails",
			emails:   nil,
			wantAddr: "",
			wantOK:   false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			addr, ok := chooseVerifiedPrimaryEmail(c.emails)
			if addr != c.wantAddr || ok != c.wantOK {
				t.Errorf("chooseVerifiedPrimaryEmail() = (%q, %v), want (%q, %v)", addr, ok, c.wantAddr, c.wantOK)
			}
		})
	}
}
