package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bonez-io/re_gent/internal/store"
)

// wireProject makes a project that looks connected: a store with a remote and
// re_gent's hooks installed the same way connect installs them.
func wireProject(t *testing.T, root string) {
	t.Helper()
	s, err := store.Init(root)
	if err != nil {
		t.Fatalf("store init: %v", err)
	}
	cfg, err := s.ReadRepoConfig()
	if err != nil {
		t.Fatalf("read repo config: %v", err)
	}
	cfg.Remote.URL = "http://example.test:7654"
	cfg.Remote.RepoID = filepath.Base(root)
	if err := s.WriteRepoConfig(cfg); err != nil {
		t.Fatalf("write repo config: %v", err)
	}
	if _, err := installClaudeHook(root); err != nil {
		t.Fatalf("install hooks: %v", err)
	}
}

// TestDisconnectRemovesWiring covers the whole point: after disconnecting, the
// project sends nothing and captures nothing.
func TestDisconnectRemovesWiring(t *testing.T) {
	root := t.TempDir()
	wireProject(t, root)

	if err := disconnectProject(root); err != nil {
		t.Fatalf("disconnectProject: %v", err)
	}

	s, err := store.Open(filepath.Join(root, ".regent"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cfg, err := s.ReadRepoConfig()
	if err != nil {
		t.Fatalf("read repo config: %v", err)
	}
	if cfg.Remote.URL != "" || cfg.Remote.RepoID != "" {
		t.Errorf("remote should be cleared, got %+v", cfg.Remote)
	}

	if data, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json")); err == nil {
		if strings.Contains(string(data), "rgt ") {
			t.Errorf("re_gent hooks should be gone, settings still has:\n%s", data)
		}
	}

	// Disconnecting twice must report rather than pretend it did something.
	if err := disconnectProject(root); err == nil {
		t.Error("disconnecting an unconnected project should report it")
	}
}

// TestDisconnectPreservesOtherHooks is the safety property: settings.json is
// shared with every other tool the user runs, so removal must be surgical.
func TestDisconnectPreservesOtherHooks(t *testing.T) {
	root := t.TempDir()
	wireProject(t, root)

	// Someone else's hook and setting, added after re_gent's.
	settingsPath := filepath.Join(root, ".claude", "settings.json")
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	hooks := settings["hooks"].(map[string]interface{})
	hooks["Stop"] = append(normalizeHookGroups(hooks["Stop"]), map[string]interface{}{
		"matcher": "",
		"hooks":   []interface{}{map[string]interface{}{"type": "command", "command": "my-own-tool --notify"}},
	})
	settings["model"] = "opus"
	out, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.WriteFile(settingsPath, out, 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	if err := disconnectProject(root); err != nil {
		t.Fatalf("disconnectProject: %v", err)
	}

	after, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings.json must survive when it still holds other config: %v", err)
	}
	got := string(after)
	if !strings.Contains(got, "my-own-tool --notify") {
		t.Errorf("another tool's hook must be preserved; got:\n%s", got)
	}
	if !strings.Contains(got, "opus") {
		t.Errorf("unrelated settings must be preserved; got:\n%s", got)
	}
	if strings.Contains(got, "rgt ") {
		t.Errorf("re_gent hooks should be gone; got:\n%s", got)
	}
}

// TestDisconnectRemovesEmptySettings: when re_gent's hooks were the only thing
// in the file, clean up rather than leaving an empty husk.
func TestDisconnectRemovesEmptySettings(t *testing.T) {
	root := t.TempDir()
	wireProject(t, root)

	if err := disconnectProject(root); err != nil {
		t.Fatalf("disconnectProject: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Error("a settings.json holding only re_gent hooks should be removed")
	}
}
