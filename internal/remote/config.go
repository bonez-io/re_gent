// Package remote implements the client half of re_gent's server mode: an HTTP
// client for the object/ref protocol, a durable outbox for offline resilience,
// and the push/hydrate walkers that move a session DAG between a local cache
// and the server.
//
// In server mode the server is the source of truth. The local directory is a
// disposable write-ahead cache; see docs/server-mode.md for the failure-mode
// contract this package implements.
package remote

import (
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"lukechampine.com/blake3"
)

// DefaultTimeout bounds all network work performed inside a single hook
// invocation. Hooks run inside a live agent turn, so this is deliberately
// short: exceeding it spools and returns rather than stalling the agent.
const DefaultTimeout = 5 * time.Second

// maxTimeout caps operator-supplied timeouts. A hook that blocks longer than
// this is indistinguishable from a hung agent, so we refuse to honour it.
const maxTimeout = 60 * time.Second

// Config describes how (and whether) capture talks to a re_gent server.
type Config struct {
	// ServerURL is the base URL of the re_gent server, e.g. https://regent.example.com.
	ServerURL string
	// RepoID is the repository name registered with the server.
	RepoID string
	// Token is the bearer token used for authentication. It is never logged.
	Token string
	// Timeout bounds all network work for one hook invocation.
	Timeout time.Duration
	// CacheDir overrides the default machine-local cache location.
	CacheDir string
}

// Enabled reports whether server mode is configured. Both a server URL and a
// repo id are required: half a configuration is treated as no configuration so
// that a typo degrades to local mode rather than to a broken remote.
func (c Config) Enabled() bool {
	return c.ServerURL != "" && c.RepoID != ""
}

// Validate checks a server-mode configuration without contacting the server.
func (c Config) Validate() error {
	if c.ServerURL == "" {
		return fmt.Errorf("server url is required")
	}
	u, err := url.Parse(c.ServerURL)
	if err != nil {
		return fmt.Errorf("invalid server url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid server url %q: scheme must be http or https", c.ServerURL)
	}
	if u.Host == "" {
		return fmt.Errorf("invalid server url %q: missing host", c.ServerURL)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid server url %q: credentials, query, and fragment are not allowed", c.ServerURL)
	}
	if err := ValidateCredentialTransport(c.ServerURL, c.Token); err != nil {
		return err
	}
	return ValidateRepoID(c.RepoID)
}

// ValidateCredentialTransport prevents bearer credentials from crossing a
// plaintext network. HTTP remains supported for an explicit loopback-only
// development server and for open servers where no token is present.
func ValidateCredentialTransport(serverURL, token string) error {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	u, err := url.Parse(serverURL)
	if err != nil {
		return fmt.Errorf("invalid server url: %w", err)
	}
	if u.Scheme == "https" {
		return nil
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	ip := net.ParseIP(host)
	if u.Scheme == "http" && (host == "localhost" || (ip != nil && ip.IsLoopback())) {
		return nil
	}
	return fmt.Errorf("refusing to send a bearer token to %q over plaintext HTTP; use HTTPS", u.Host)
}

// ValidateRepoID mirrors the server's repo-name rules so that an unusable id is
// rejected on the client instead of producing a confusing 400 mid-turn.
func ValidateRepoID(repo string) error {
	if repo == "" {
		return fmt.Errorf("repo id is required")
	}
	if len(repo) > 64 {
		return fmt.Errorf("repo id too long (max 64 characters)")
	}
	for _, r := range repo {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return fmt.Errorf("invalid repo id %q: use letters, digits, '.', '_', '-' only", repo)
		}
	}
	switch repo[0] {
	case '.', '-', '_':
		return fmt.Errorf("invalid repo id %q: must start with a letter or digit", repo)
	}
	return nil
}

// fileConfig is the on-disk shape of a re_gent config.toml. Three tables are
// recognised so that the files written by different commands all resolve:
//   - [server]: the operator escape hatch and historical shape.
//   - [remote]: the per-repo binding written by `rgt connect` into the repo's
//     own .regent/config.toml (url + repo_id, which is inherently per-repo).
//   - [auth]:   the legacy per-user credential shape written by old releases.
//   - [[credentials]]: server-keyed credentials written by `rgt auth login` into
//     ~/.regent/config.toml (server_url + token).
type fileConfig struct {
	Server struct {
		URL     string `toml:"url"`
		RepoID  string `toml:"repo_id"`
		Token   string `toml:"token"`
		Timeout string `toml:"timeout"`
	} `toml:"server"`
	Remote struct {
		URL    string `toml:"url"`
		RepoID string `toml:"repo_id"`
	} `toml:"remote"`
	Auth struct {
		ServerURL string `toml:"server_url"`
		Token     string `toml:"token"`
	} `toml:"auth"`
	Credentials []struct {
		ServerURL string `toml:"server_url"`
		Token     string `toml:"token"`
	} `toml:"credentials"`
}

// Env is a lookup function with the shape of os.LookupEnv. Tests inject a map
// so that configuration resolution never depends on the ambient environment.
type Env func(string) (string, bool)

// OSEnv reads the real process environment.
func OSEnv(key string) (string, bool) { return os.LookupEnv(key) }

// LoadConfig resolves server-mode configuration from a single config file plus
// environment variables. Environment variables win over the file so an operator
// can disable or redirect server mode for one process without editing shared
// state. All recognised tables ([server], [remote], [auth]) in the file are
// honoured; see LoadConfigForCWD for the repo-aware resolver used by the hooks.
//
// A malformed config file is reported as an error but never panics; callers in
// the hook path treat any error as "server mode unavailable" and fall back.
func LoadConfig(env Env, configPath string) (Config, error) {
	var cfg Config
	if err := mergeFile(configPath, &cfg); err != nil {
		return Config{}, err
	}
	if err := applyEnv(env, &cfg); err != nil {
		return Config{}, err
	}
	cfg.ServerURL = strings.TrimRight(cfg.ServerURL, "/")
	cfg.Timeout = clampTimeout(cfg.Timeout)
	return cfg, nil
}

// LoadConfigForCWD resolves server-mode configuration for a working directory,
// layering lowest-to-highest precedence:
//
//  1. the repo-local .regent/config.toml at or above cwd  ([remote] url+repo_id,
//     written by `rgt connect`)
//  2. the per-user ~/.regent/config.toml                  ([auth] token from
//     `rgt auth login`; [server] operator overrides)
//  3. environment variables
//
// This is what wires `rgt connect` (which writes a repo-local [remote] binding)
// to server mode: repo_id is inherently per-repo, so it must come from the repo
// rather than from shared global state, which also keeps multi-repo coherent.
func LoadConfigForCWD(env Env, cwd string) (Config, error) {
	var cfg Config
	if p := RepoConfigPath(cwd); p != "" {
		if err := mergeFile(p, &cfg); err != nil {
			return Config{}, err
		}
	}
	if g := DefaultConfigPath(); g != "" {
		if err := mergeFile(g, &cfg); err != nil {
			return Config{}, err
		}
	}
	if err := applyEnv(env, &cfg); err != nil {
		return Config{}, err
	}
	cfg.ServerURL = strings.TrimRight(cfg.ServerURL, "/")
	cfg.Timeout = clampTimeout(cfg.Timeout)
	return cfg, nil
}

// RepoConfigPath returns the path to the nearest .regent/config.toml at or above
// cwd, or "" when no re_gent repository is found. It lets server-mode resolution
// pick up the per-repo [remote] binding that `rgt connect` writes.
func RepoConfigPath(cwd string) string {
	dir := cwd
	for dir != "" {
		p := filepath.Join(dir, ".regent", "config.toml")
		info, err := os.Stat(p)
		switch {
		case err == nil && !info.IsDir():
			return p
		case err != nil && !os.IsNotExist(err):
			// A permission/IO error is not "no config here": return the path so
			// the caller's read surfaces the real error instead of silently
			// treating the repo as disconnected from its server.
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// applyEnv overlays REGENT_* environment variables onto cfg. Env values always
// win over file values.
func applyEnv(env Env, cfg *Config) error {
	if env == nil {
		env = OSEnv
	}
	if v, ok := env("REGENT_SERVER_URL"); ok {
		nextURL := strings.TrimSpace(v)
		if strings.TrimRight(nextURL, "/") != strings.TrimRight(cfg.ServerURL, "/") {
			// A file credential is scoped to the file's server. Redirecting the
			// request through the environment must not carry that credential to
			// another host; REGENT_TOKEN is the explicit override for that case.
			cfg.Token = ""
		}
		cfg.ServerURL = nextURL
	}
	if v, ok := env("REGENT_REPO_ID"); ok {
		cfg.RepoID = strings.TrimSpace(v)
	}
	if v, ok := env("REGENT_TOKEN"); ok {
		cfg.Token = strings.TrimSpace(v)
	}
	if v, ok := env("REGENT_CACHE_DIR"); ok {
		cfg.CacheDir = strings.TrimSpace(v)
	}
	if v, ok := env("REGENT_SERVER_TIMEOUT"); ok {
		d, err := time.ParseDuration(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("invalid REGENT_SERVER_TIMEOUT %q: %w", v, err)
		}
		cfg.Timeout = d
	}
	return nil
}

// mergeFile fills empty fields of cfg from a config file, honouring the
// [server], [remote] and [auth] tables. Existing non-empty fields are left
// untouched, so callers layer files by calling mergeFile in precedence order.
// A missing file is not an error.
func mergeFile(path string, cfg *Config) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		// A missing config file is the common case, not an error.
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}

	var fc fileConfig
	if err := toml.Unmarshal(data, &fc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	setIfEmpty(&cfg.ServerURL, fc.Server.URL, fc.Remote.URL, fc.Auth.ServerURL)
	setIfEmpty(&cfg.RepoID, fc.Server.RepoID, fc.Remote.RepoID)
	credentialToken := ""
	for _, credential := range fc.Credentials {
		if strings.TrimRight(strings.TrimSpace(credential.ServerURL), "/") == strings.TrimRight(cfg.ServerURL, "/") {
			credentialToken = credential.Token
			break
		}
	}
	legacyToken := ""
	legacyServerURL := fc.Auth.ServerURL
	if legacyServerURL == "" {
		legacyServerURL = fc.Server.URL
	}
	if legacyServerURL != "" && strings.TrimRight(strings.TrimSpace(legacyServerURL), "/") == strings.TrimRight(cfg.ServerURL, "/") {
		legacyToken = fc.Auth.Token
	}
	serverToken := ""
	if strings.TrimRight(strings.TrimSpace(fc.Server.URL), "/") == strings.TrimRight(cfg.ServerURL, "/") {
		serverToken = fc.Server.Token
	}
	setIfEmpty(&cfg.Token, serverToken, credentialToken, legacyToken)
	if cfg.Timeout == 0 && strings.TrimSpace(fc.Server.Timeout) != "" {
		d, err := time.ParseDuration(strings.TrimSpace(fc.Server.Timeout))
		if err != nil {
			return fmt.Errorf("parse %s: invalid server.timeout %q: %w", path, fc.Server.Timeout, err)
		}
		cfg.Timeout = d
	}
	return nil
}

// setIfEmpty sets *dst to the first non-empty, trimmed candidate, but only when
// *dst is currently empty. This is the primitive behind precedence layering.
func setIfEmpty(dst *string, candidates ...string) {
	if *dst != "" {
		return
	}
	for _, c := range candidates {
		if v := strings.TrimSpace(c); v != "" {
			*dst = v
			return
		}
	}
}

func clampTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultTimeout
	}
	if d > maxTimeout {
		return maxTimeout
	}
	return d
}

// DefaultConfigPath returns ~/.regent/config.toml, or "" when the home
// directory cannot be determined.
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".regent", "config.toml")
}

// CacheDirFor returns the machine-local cache directory backing server mode.
//
// The cache lives outside the working tree on purpose: in server mode the repo
// must not need a .regent/ directory at all. The cache is disposable — every
// object and ref in it is either already on the server or listed in the spool.
//
// The path is keyed by the server as well as the project. It used to be keyed
// by project alone, which meant one cache — one object store, one index, one
// set of upload watermarks — shared by every server a project was ever pointed
// at. Each machine then held a blend of two histories and told each server the
// other's watermarks. Identity coming from the git remote makes that ordinary
// rather than rare: a repository connected to staging and to production
// derives the same id for both, correctly, because it is the same repository.
// The id is meant to match. The cache is not.
func CacheDirFor(cfg Config) (string, error) {
	if err := ValidateRepoID(cfg.RepoID); err != nil {
		return "", err
	}
	base := cfg.CacheDir
	if base == "" {
		userCache, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("locate user cache dir: %w", err)
		}
		base = filepath.Join(userCache, "regent")
	}
	return filepath.Join(base, "repos", serverCacheKey(cfg.ServerURL), cfg.RepoID), nil
}

// serverCacheKey turns a server address into one path segment.
//
// A hash rather than the address itself: a URL contains characters that are not
// safe in a path on every platform, and the readable part of this path is the
// repo id sitting underneath it. Stability is what matters — the same binding
// must resolve to the same directory on every run, or each run starts from an
// empty cache and re-uploads everything.
func serverCacheKey(serverURL string) string {
	if serverURL == "" {
		// A binding with no server: one shared bucket, which is what these have
		// always had.
		return "local"
	}
	sum := blake3.Sum256([]byte(strings.TrimRight(serverURL, "/")))
	return hex.EncodeToString(sum[:])[:12]
}

// Redact renders a token safe to log: it never reveals more than a short prefix.
func Redact(token string) string {
	if token == "" {
		return ""
	}
	if len(token) < 8 {
		return "****"
	}
	return token[:4] + "****"
}
