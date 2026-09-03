package store

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// RemoteConfig holds the remote server settings for this repo.
//
// ProjectID is the server-assigned, opaque project identifier RFC 0004's
// enrollment API introduced (config.RemoteBinding is the authoritative type
// for it); RepoID is the legacy client-derived identifier written by servers
// that have not adopted the project API. omitempty on ProjectID matters here:
// every WriteRepoConfig caller round-trips through this struct, and without
// it a legacy binding with no project_id would gain an empty
// `project_id = ""` line on every write.
type RemoteConfig struct {
	URL       string `toml:"url"`
	ProjectID string `toml:"project_id,omitempty"`
	RepoID    string `toml:"repo_id,omitempty"`
}

// CaptureConfig records the layout its owner intentionally uses. It is a
// project binding rather than a doctor flag so the answer survives across
// terminals and is visible to teammates in .regent/config.toml.
//
// Root is either "project" (agents are opened in this project) or
// "workspace" (this directory intentionally captures one workspace).
// The empty value means no acknowledgement has been made.
type CaptureConfig struct {
	Root string `toml:"root"`
}

// InsightConfig is the committed [insight] table of .regent/config.toml
// (RFC 0007): the policy that travels with the repository. Providers,
// endpoints, and key names live in the per-user config, never here.
//
// omitempty matters: a repository that never enabled insight must not gain
// an `[insight]` table every time connect or init rewrites the file.
type InsightConfig struct {
	// Enabled turns the derived layer on for this repository. It is the
	// repository's half of the switch; a contributor with no provider
	// configured in ~/.regent/config.toml runs nothing.
	Enabled bool `toml:"enabled"`
	// WorkItemIdle is how long a session may be silent before its open work
	// item is closed as wip without a model call, e.g. "2h". Empty means the
	// default.
	WorkItemIdle string `toml:"work_item_idle,omitempty"`
	// Scrub is what capture stores and what every provider request is
	// cleaned with.
	Scrub InsightScrubConfig `toml:"scrub,omitempty"`
	// Model may pin a provider *name* for this repository only, so a public
	// repository can say "local" without carrying a URL.
	Model InsightModelOverride `toml:"model,omitempty"`
}

// InsightScrubConfig is [insight.scrub].
type InsightScrubConfig struct {
	// Capture is what the hooks store: "off" (raw bytes, today's behaviour),
	// "secrets" (tool I/O and messages rewritten through redact), or
	// "secrets+paths" (also home directories and usernames). Files are never
	// rewritten.
	Capture string `toml:"capture,omitempty"`
	// Patterns are regular expressions scrubbed at capture (when on) and on
	// every provider request (always).
	Patterns []string `toml:"patterns,omitempty"`
}

// InsightModelOverride is the committed [insight.model] table: the provider
// name only.
type InsightModelOverride struct {
	Provider string `toml:"provider,omitempty"`
}

// RepoConfig is the machine-written section of .regent/config.toml.
type RepoConfig struct {
	Remote  RemoteConfig  `toml:"remote"`
	Capture CaptureConfig `toml:"capture"`
	Insight InsightConfig `toml:"insight,omitempty"`
}

// ReadRepoConfig reads the re_gent-managed sections of .regent/config.toml.
// A missing or empty file returns a zero RepoConfig without error.
func (s *Store) ReadRepoConfig() (RepoConfig, error) {
	path := filepath.Join(s.Root, "config.toml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return RepoConfig{}, nil
	}
	if err != nil {
		return RepoConfig{}, fmt.Errorf("read repo config: %w", err)
	}
	var cfg RepoConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return RepoConfig{}, fmt.Errorf("parse repo config: %w", err)
	}
	return cfg, nil
}

// WriteRepoConfig writes cfg to .regent/config.toml, replacing any existing
// content.
func (s *Store) WriteRepoConfig(cfg RepoConfig) error {
	path := filepath.Join(s.Root, "config.toml")
	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal repo config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write repo config: %w", err)
	}
	return nil
}
