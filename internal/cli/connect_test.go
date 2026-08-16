package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/regent-vcs/regent/internal/config"
	"github.com/regent-vcs/regent/internal/store"
)

// newTestServer returns an httptest.Server that handles POST /repos.
func newTestServer(t *testing.T, statusCode int, repoID string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos" {
			http.NotFound(w, r)
			return
		}
		if statusCode != http.StatusOK && statusCode != http.StatusCreated {
			http.Error(w, "error", statusCode)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_ = json.NewEncoder(w).Encode(map[string]string{"repo_id": repoID})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// writeGlobalConfig writes a global config with the given token to path.
func writeGlobalConfig(t *testing.T, path, token string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := config.SaveTo(path, &config.UserConfig{Auth: config.Auth{Token: token}}); err != nil {
		t.Fatalf("write global config: %v", err)
	}
}

func TestConnect_WritesRemoteConfig(t *testing.T) {
	srv := newTestServer(t, http.StatusCreated, "repo-abc")
	root := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	writeGlobalConfig(t, cfgPath, "my-token")

	if err := runConnect(connectParams{
		serverURL:   srv.URL,
		projectRoot: root,
		configPath:  cfgPath,
		httpClient:  srv.Client(),
	}); err != nil {
		t.Fatalf("runConnect: %v", err)
	}

	s, err := store.Open(filepath.Join(root, ".regent"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cfg, err := s.ReadRepoConfig()
	if err != nil {
		t.Fatalf("ReadRepoConfig: %v", err)
	}
	if cfg.Remote.URL != srv.URL {
		t.Errorf("URL: got %q, want %q", cfg.Remote.URL, srv.URL)
	}
	if cfg.Remote.RepoID != "repo-abc" {
		t.Errorf("RepoID: got %q, want %q", cfg.Remote.RepoID, "repo-abc")
	}
}

func TestConnect_InstallsClaudeHooks(t *testing.T) {
	srv := newTestServer(t, http.StatusCreated, "repo-hooks")
	root := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	writeGlobalConfig(t, cfgPath, "tok")

	if err := runConnect(connectParams{
		serverURL:   srv.URL,
		projectRoot: root,
		configPath:  cfgPath,
		httpClient:  srv.Client(),
	}); err != nil {
		t.Fatalf("runConnect: %v", err)
	}

	settingsPath := filepath.Join(root, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read claude settings: %v", err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		t.Fatalf("hooks missing from settings")
	}
	for _, event := range []string{"UserPromptSubmit", "Stop", "PostToolBatch"} {
		if hooks[event] == nil {
			t.Errorf("hook %s missing", event)
		}
	}
}

// TestConnect_MergesExistingHooks is the main AC test: connect on a repo that
// already has hooks must merge/dedupe, never overwrite.
func TestConnect_MergesExistingHooks(t *testing.T) {
	srv := newTestServer(t, http.StatusCreated, "repo-merge")
	root := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	writeGlobalConfig(t, cfgPath, "tok")

	// Pre-install a non-regent Claude hook.
	claudeDir := filepath.Join(root, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := `{
  "hooks": {
    "Stop": [
      {
        "matcher": "",
        "hooks": [{"type": "command", "command": "echo keep-me"}]
      }
    ]
  }
}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(existing), 0o644); err != nil {
		t.Fatalf("write existing settings: %v", err)
	}

	if err := runConnect(connectParams{
		serverURL:   srv.URL,
		projectRoot: root,
		configPath:  cfgPath,
		httpClient:  srv.Client(),
	}); err != nil {
		t.Fatalf("runConnect: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	hooks := settings["hooks"].(map[string]interface{})
	stopCommands := hookCommands(t, hooks["Stop"])

	if countCommand(stopCommands, "echo keep-me") != 1 {
		t.Errorf("existing hook was lost; commands: %v", stopCommands)
	}
	if countCommand(stopCommands, claudeAssistantHook()) != 1 {
		t.Errorf("regent assistant hook missing; commands: %v", stopCommands)
	}
}

// TestConnect_Idempotent verifies re-running does not duplicate hooks or config.
func TestConnect_Idempotent(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/repos" {
			callCount++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"repo_id": "repo-idem"})
		}
	}))
	t.Cleanup(srv.Close)

	root := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	writeGlobalConfig(t, cfgPath, "tok")

	params := connectParams{
		serverURL:   srv.URL,
		projectRoot: root,
		configPath:  cfgPath,
		httpClient:  srv.Client(),
	}

	if err := runConnect(params); err != nil {
		t.Fatalf("first runConnect: %v", err)
	}
	if err := runConnect(params); err != nil {
		t.Fatalf("second runConnect: %v", err)
	}

	if callCount > 1 {
		t.Errorf("expected at most 1 /repos call, got %d", callCount)
	}

	data, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	hooks := settings["hooks"].(map[string]interface{})
	stopCmds := hookCommands(t, hooks["Stop"])
	if countCommand(stopCmds, claudeAssistantHook()) != 1 {
		t.Errorf("expected exactly 1 assistant hook after two connects, got %v", stopCmds)
	}
}

// TestConnect_SucceedsWithoutToken is the new-teammate path: a machine that has
// never run `rgt login` must still connect, because the default server is open.
// Requiring a token here broke onboarding for everyone except the person who had
// signed in at some point — and their stale token hid the bug during testing.
func TestConnect_SucceedsWithoutToken(t *testing.T) {
	srv := newTestServer(t, http.StatusCreated, "repo-open")
	root := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "config.toml") // no token written

	if err := runConnect(connectParams{
		serverURL:   srv.URL,
		projectRoot: root,
		configPath:  cfgPath,
		httpClient:  srv.Client(),
	}); err != nil {
		t.Fatalf("connect without a token must succeed against an open server: %v", err)
	}

	s, err := store.Open(filepath.Join(root, ".regent"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cfg, err := s.ReadRepoConfig()
	if err != nil {
		t.Fatalf("read repo config: %v", err)
	}
	if cfg.Remote.RepoID != "repo-open" {
		t.Errorf("repo_id = %q, want %q", cfg.Remote.RepoID, "repo-open")
	}
}

// TestConnect_OmitsAuthHeaderWithoutToken guards the wire format: with no token
// stored we must send no Authorization header at all, rather than a malformed
// "Bearer " that a stricter server would reject.
func TestConnect_OmitsAuthHeaderWithoutToken(t *testing.T) {
	var gotAuth string
	var seen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		seen = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"repo_id": "repo-open"})
	}))
	t.Cleanup(srv.Close)

	if err := runConnect(connectParams{
		serverURL:   srv.URL,
		projectRoot: t.TempDir(),
		configPath:  filepath.Join(t.TempDir(), "config.toml"),
		httpClient:  srv.Client(),
	}); err != nil {
		t.Fatalf("runConnect: %v", err)
	}
	if !seen {
		t.Fatal("server never received the register request")
	}
	if gotAuth != "" {
		t.Errorf("Authorization header = %q, want none", gotAuth)
	}
}

// TestConnect_ServerUnauthorized checks a 401 is surfaced as a sign-in error.
func TestConnect_ServerUnauthorized(t *testing.T) {
	srv := newTestServer(t, http.StatusUnauthorized, "")
	root := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	writeGlobalConfig(t, cfgPath, "bad-token")

	err := runConnect(connectParams{
		serverURL:   srv.URL,
		projectRoot: root,
		configPath:  cfgPath,
		httpClient:  srv.Client(),
	})
	if err == nil {
		t.Fatal("want error for 401, got nil")
	}
	// A 401 must still be reported as a 401. What changed is the remedy: the
	// old message said "Run: rgt login <server-url>", and login no longer
	// exists, so following the advice would have produced "unknown command".
	// An error that names a removed command is worse than one that names none.
	if !strings.Contains(err.Error(), "unauthenticated") {
		t.Errorf("error should say the server rejected us as unauthenticated, got: %v", err)
	}
	if strings.Contains(err.Error(), "rgt login") {
		t.Errorf("error points at `rgt login`, which has been removed: %v", err)
	}
}
