package remote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// envMap builds an Env from a map so config resolution never depends on the
// ambient process environment.
func envMap(kv map[string]string) Env {
	return func(key string) (string, bool) {
		v, ok := kv[key]
		return v, ok
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadConfig(t *testing.T) {
	fileBody := "[server]\nurl = \"http://file.example\"\nrepo_id = \"from-file\"\ntoken = \"file-token\"\ntimeout = \"3s\"\n"

	tests := []struct {
		name        string
		env         map[string]string
		file        string
		wantURL     string
		wantRepo    string
		wantToken   string
		wantTimeout time.Duration
		wantEnabled bool
		wantErr     bool
	}{
		{
			name:        "empty environment disables server mode",
			wantTimeout: DefaultTimeout,
		},
		{
			name:        "file only",
			file:        fileBody,
			wantURL:     "http://file.example",
			wantRepo:    "from-file",
			wantToken:   "file-token",
			wantTimeout: 3 * time.Second,
			wantEnabled: true,
		},
		{
			name: "env overrides file",
			file: fileBody,
			env: map[string]string{
				"REGENT_SERVER_URL": "https://env.example/",
				"REGENT_REPO_ID":    "from-env",
				"REGENT_TOKEN":      "env-token",
			},
			wantURL:     "https://env.example",
			wantRepo:    "from-env",
			wantToken:   "env-token",
			wantTimeout: 3 * time.Second,
			wantEnabled: true,
		},
		{
			name:        "url without repo id is not enabled",
			env:         map[string]string{"REGENT_SERVER_URL": "https://env.example"},
			wantURL:     "https://env.example",
			wantTimeout: DefaultTimeout,
		},
		{
			name:        "timeout is clamped to the maximum",
			env:         map[string]string{"REGENT_SERVER_TIMEOUT": "10h"},
			wantTimeout: maxTimeout,
		},
		{
			name:        "zero timeout falls back to the default",
			env:         map[string]string{"REGENT_SERVER_TIMEOUT": "0s"},
			wantTimeout: DefaultTimeout,
		},
		{
			name:    "unparsable timeout is an error",
			env:     map[string]string{"REGENT_SERVER_TIMEOUT": "soon"},
			wantErr: true,
		},
		{
			name:    "malformed config file is an error",
			file:    "[server\nurl =",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := ""
			if tt.file != "" {
				path = writeConfig(t, tt.file)
			}

			cfg, err := LoadConfig(envMap(tt.env), path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got config %+v", cfg)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.ServerURL != tt.wantURL {
				t.Errorf("ServerURL = %q, want %q", cfg.ServerURL, tt.wantURL)
			}
			if cfg.RepoID != tt.wantRepo {
				t.Errorf("RepoID = %q, want %q", cfg.RepoID, tt.wantRepo)
			}
			if cfg.Token != tt.wantToken {
				t.Errorf("Token = %q, want %q", cfg.Token, tt.wantToken)
			}
			if cfg.Timeout != tt.wantTimeout {
				t.Errorf("Timeout = %v, want %v", cfg.Timeout, tt.wantTimeout)
			}
			if cfg.Enabled() != tt.wantEnabled {
				t.Errorf("Enabled() = %v, want %v", cfg.Enabled(), tt.wantEnabled)
			}
		})
	}
}

func TestLoadConfigMissingFileIsNotAnError(t *testing.T) {
	cfg, err := LoadConfig(envMap(nil), filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("missing config file should not error: %v", err)
	}
	if cfg.Enabled() {
		t.Fatal("expected server mode to be disabled")
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"valid", Config{ServerURL: "https://a.example", RepoID: "repo"}, ""},
		{"no url", Config{RepoID: "repo"}, "server url is required"},
		{"bad scheme", Config{ServerURL: "ftp://a.example", RepoID: "repo"}, "scheme must be http or https"},
		{"no host", Config{ServerURL: "http://", RepoID: "repo"}, "missing host"},
		{"no repo", Config{ServerURL: "https://a.example"}, "repo id is required"},
		{"repo traversal", Config{ServerURL: "https://a.example", RepoID: "../etc"}, "invalid repo id"},
		{"repo slash", Config{ServerURL: "https://a.example", RepoID: "a/b"}, "invalid repo id"},
		{"repo leading dot", Config{ServerURL: "https://a.example", RepoID: ".hidden"}, "must start with"},
		{"repo too long", Config{ServerURL: "https://a.example", RepoID: strings.Repeat("a", 65)}, "too long"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestCacheDirForIsOutsideTheRepository(t *testing.T) {
	base := t.TempDir()
	dir, err := CacheDirFor(Config{RepoID: "my-repo", CacheDir: base})
	if err != nil {
		t.Fatalf("CacheDirFor: %v", err)
	}
	// Asserted as "under the cache root, named for the repo" rather than as an
	// exact path: the layout gained a per-server segment, and a test that spells
	// out every segment fails on layout changes that break nothing.
	if root := filepath.Join(base, "repos"); !strings.HasPrefix(dir, root+string(filepath.Separator)) {
		t.Fatalf("cache dir = %q, want it under %q", dir, root)
	}
	if got := filepath.Base(dir); got != "my-repo" {
		t.Fatalf("cache dir = %q, want the repo id %q as its last segment", dir, "my-repo")
	}

	// A repo id that could escape the cache root must be rejected, not cleaned.
	if _, err := CacheDirFor(Config{RepoID: "../../etc", CacheDir: base}); err == nil {
		t.Fatal("expected traversal repo id to be rejected")
	}
}

func TestRedactNeverRevealsWholeToken(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"abc", "****"},
		{"abcdefg", "****"},
		{"abcdefgh", "abcd****"},
		{"secret-token-value", "secr****"},
	}
	for _, tt := range tests {
		if got := Redact(tt.in); got != tt.want {
			t.Errorf("Redact(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// The cache is keyed by repo id alone, so two projects with the same id
// pointed at different servers share one cache directory — one object store,
// one index, one set of upload watermarks. Each machine then holds a blend of
// two servers' histories and pushes each server the other's watermarks.
//
// This is not hypothetical once identity comes from the git remote: a project
// connected to a staging server and a production server derives the same id
// for both, because it is the same repository. The id is *supposed* to match.
// The cache is what must not.
func TestCacheDirIsSeparatePerServer(t *testing.T) {
	base := t.TempDir()

	staging, err := CacheDirFor(Config{RepoID: "github.com-acme-api", ServerURL: "https://staging.example.com", CacheDir: base})
	if err != nil {
		t.Fatalf("CacheDirFor(staging): %v", err)
	}
	production, err := CacheDirFor(Config{RepoID: "github.com-acme-api", ServerURL: "https://production.example.com", CacheDir: base})
	if err != nil {
		t.Fatalf("CacheDirFor(production): %v", err)
	}

	if staging == production {
		t.Errorf("both servers share the cache at %s; each holds a blend of the other's history", staging)
	}
}

// The other half: the path has to be the same every time for one binding, or
// every run starts from an empty cache and re-uploads everything it has.
func TestCacheDirIsStableForOneBinding(t *testing.T) {
	base := t.TempDir()
	cfg := Config{RepoID: "github.com-acme-api", ServerURL: "https://example.com", CacheDir: base}

	first, err := CacheDirFor(cfg)
	if err != nil {
		t.Fatalf("CacheDirFor: %v", err)
	}
	second, err := CacheDirFor(cfg)
	if err != nil {
		t.Fatalf("CacheDirFor: %v", err)
	}

	if first != second {
		t.Errorf("cache path moved between calls: %q then %q; every run would re-upload from an empty cache", first, second)
	}
}
