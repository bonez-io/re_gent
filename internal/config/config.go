// Package config manages the per-user re_gent configuration stored at
// ~/.regent/config.toml with mode 0600.  Only the owner can read the file,
// which keeps auth tokens off world-readable paths.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// ErrNotSignedIn is returned by CheckAuth when no valid token is stored.
// Callers that display this error should tell the user how to sign in.
var ErrNotSignedIn = errors.New("not signed in; run: rgt login <server-url>")

// ErrTokenInvalid is returned when a token is present but fails format validation.
var ErrTokenInvalid = errors.New("token is invalid")

// minTokenLen is the minimum number of characters a token must have to be
// considered syntactically valid.  Actual validity is verified by the server.
const minTokenLen = 16

// UserConfig is the top-level user configuration structure.
type UserConfig struct {
	Auth   Auth   `toml:"auth"`
	Server Server `toml:"server"`
}

// Server remembers the team server this machine was last set up against, so
// `rgt` on its own can offer to wire more projects without being told the URL
// again. It holds no credential — an open server needs none.
type Server struct {
	URL string `toml:"url"`
}

// Auth holds authentication credentials for a re_gent server.
type Auth struct {
	ServerURL string `toml:"server_url"`
	// Token is a bearer token.  It is never printed in full — callers must
	// redact it before display.  It is stored on disk with mode 0600.
	Token string `toml:"token"`
}

// DefaultPath returns the path to the default per-user config file:
// ~/.regent/config.toml.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".regent", "config.toml"), nil
}

// Load reads the default per-user config.  If the file does not exist it
// returns an empty UserConfig and no error — the caller must use CheckAuth
// to detect the "not signed in" condition.
func Load() (*UserConfig, error) {
	path, err := DefaultPath()
	if err != nil {
		return &UserConfig{}, nil
	}
	return LoadFrom(path)
}

// LoadFrom reads a UserConfig from an explicit path.  A missing file is not an
// error; it returns an empty config.
func LoadFrom(path string) (*UserConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &UserConfig{}, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg UserConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return &cfg, nil
}

// Save writes cfg to the default per-user config path with mode 0600.
func Save(cfg *UserConfig) error {
	path, err := DefaultPath()
	if err != nil {
		return err
	}
	return SaveTo(path, cfg)
}

// SaveTo writes cfg to path with mode 0600 (owner read/write only).
// The parent directory is created with mode 0700 if absent.
func SaveTo(path string, cfg *UserConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	// Write atomically: temp file then rename so readers never see partial content.
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".regent-config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install config: %w", err)
	}
	return nil
}

// CheckAuth returns nil when cfg contains a valid token, ErrNotSignedIn when
// the token is absent, and ErrTokenInvalid when the token fails format checks.
// It never panics, making it safe to call from hook code paths.
func CheckAuth(cfg *UserConfig) error {
	if cfg == nil || cfg.Auth.Token == "" {
		return ErrNotSignedIn
	}
	if len(cfg.Auth.Token) < minTokenLen {
		return ErrTokenInvalid
	}
	return nil
}

// Redact returns a display-safe version of tok: the first four characters
// followed by asterisks.  Returns "<no token>" for an empty token.
func Redact(tok string) string {
	if tok == "" {
		return "<no token>"
	}
	if len(tok) <= 4 {
		return "****"
	}
	return tok[:4] + "****"
}
