package store

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// RemoteConfig holds the remote server settings for this repo.
type RemoteConfig struct {
	URL    string `toml:"url"`
	RepoID string `toml:"repo_id"`
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

// RepoConfig is the machine-written section of .regent/config.toml.
type RepoConfig struct {
	Remote  RemoteConfig  `toml:"remote"`
	Capture CaptureConfig `toml:"capture"`
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
