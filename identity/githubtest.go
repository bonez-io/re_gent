package identity

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// FakeGitHubUser is the subset of GitHub's /user response a FakeGitHubServer
// fixture supplies.
type FakeGitHubUser struct {
	ID        int64
	Login     string
	Name      string
	AvatarURL string
}

// FakeGitHubEmail is one entry of GitHub's /user/emails response.
type FakeGitHubEmail struct {
	Email    string
	Primary  bool
	Verified bool
}

// FakeGitHubFixture is what a FakeGitHubServer returns for one authorization
// code: the token exchange succeeds and is followed by /user, /user/emails,
// and (when requested) /user/orgs responses built from these fields.
type FakeGitHubFixture struct {
	User   FakeGitHubUser
	Emails []FakeGitHubEmail
	Orgs   []string
}

// FakeGitHubServer stands in for github.com's (or a GitHub Enterprise
// Server's) OAuth and REST endpoints, so the real NewGitHub provider can be
// exercised end to end — AuthURL, the token exchange, and the profile and
// email lookups — without network access. It is safe for concurrent use and
// is closed automatically via testing.TB's Cleanup.
type FakeGitHubServer struct {
	*httptest.Server

	mu       sync.Mutex
	fixtures map[string]FakeGitHubFixture // authorization code -> fixture
	tokens   map[string]string            // issued access token -> authorization code
}

// NewFakeServer starts a FakeGitHubServer. Register fixtures with SetFixture
// before exchanging their code. Point identity.Config.BaseURL at
// server.URL() to exercise NewGitHub against it, including the GitHub
// Enterprise Server-style "/api/v3" and "/login/oauth/..." paths NewGitHub
// builds from a non-empty BaseURL.
func NewFakeServer(t testing.TB) *FakeGitHubServer {
	t.Helper()
	s := &FakeGitHubServer{
		fixtures: make(map[string]FakeGitHubFixture),
		tokens:   make(map[string]string),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", s.handleToken)
	mux.HandleFunc("/api/v3/user", s.handleUser)
	mux.HandleFunc("/api/v3/user/emails", s.handleEmails)
	mux.HandleFunc("/api/v3/user/orgs", s.handleOrgs)
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Server.Close)
	return s
}

// URL is the server's base URL, suitable for identity.Config.BaseURL.
func (s *FakeGitHubServer) URL() string { return s.Server.URL }

// SetFixture registers what exchanging code returns. Calling it again for
// the same code replaces the fixture.
func (s *FakeGitHubServer) SetFixture(code string, fixture FakeGitHubFixture) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fixtures[code] = fixture
}

func (s *FakeGitHubServer) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	code := r.PostForm.Get("code")
	s.mu.Lock()
	_, ok := s.fixtures[code]
	var token string
	if ok {
		token = randomToken()
		s.tokens[token] = code
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if !ok {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"access_token": token, "token_type": "bearer"})
}

func (s *FakeGitHubServer) fixtureForRequest(r *http.Request) (FakeGitHubFixture, bool) {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(auth) <= len(prefix) || auth[:len(prefix)] != prefix {
		return FakeGitHubFixture{}, false
	}
	token := auth[len(prefix):]
	s.mu.Lock()
	defer s.mu.Unlock()
	code, ok := s.tokens[token]
	if !ok {
		return FakeGitHubFixture{}, false
	}
	fixture, ok := s.fixtures[code]
	return fixture, ok
}

func (s *FakeGitHubServer) handleUser(w http.ResponseWriter, r *http.Request) {
	fixture, ok := s.fixtureForRequest(r)
	if !ok {
		http.Error(w, "bad credentials", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":         fixture.User.ID,
		"login":      fixture.User.Login,
		"name":       fixture.User.Name,
		"avatar_url": fixture.User.AvatarURL,
	})
}

func (s *FakeGitHubServer) handleEmails(w http.ResponseWriter, r *http.Request) {
	fixture, ok := s.fixtureForRequest(r)
	if !ok {
		http.Error(w, "bad credentials", http.StatusUnauthorized)
		return
	}
	emails := make([]map[string]any, 0, len(fixture.Emails))
	for _, e := range fixture.Emails {
		emails = append(emails, map[string]any{"email": e.Email, "primary": e.Primary, "verified": e.Verified})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(emails)
}

func (s *FakeGitHubServer) handleOrgs(w http.ResponseWriter, r *http.Request) {
	fixture, ok := s.fixtureForRequest(r)
	if !ok {
		http.Error(w, "bad credentials", http.StatusUnauthorized)
		return
	}
	orgs := make([]map[string]any, 0, len(fixture.Orgs))
	for _, login := range fixture.Orgs {
		orgs = append(orgs, map[string]any{"login": login})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(orgs)
}

func randomToken() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return "fake_gh_" + hex.EncodeToString(buf)
}
