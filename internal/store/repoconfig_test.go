package store

import (
	"testing"
)

func TestWriteRepoConfig_ReadRepoConfig_RoundTrip(t *testing.T) {
	s, err := Init(t.TempDir())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	want := RepoConfig{
		Remote:  RemoteConfig{URL: "https://example.com", RepoID: "abc-123"},
		Capture: CaptureConfig{Root: "project"},
	}
	if err := s.WriteRepoConfig(want); err != nil {
		t.Fatalf("WriteRepoConfig: %v", err)
	}
	got, err := s.ReadRepoConfig()
	if err != nil {
		t.Fatalf("ReadRepoConfig: %v", err)
	}
	if got.Remote.URL != want.Remote.URL {
		t.Errorf("URL: got %q, want %q", got.Remote.URL, want.Remote.URL)
	}
	if got.Remote.RepoID != want.Remote.RepoID {
		t.Errorf("RepoID: got %q, want %q", got.Remote.RepoID, want.Remote.RepoID)
	}
	if got.Capture.Root != want.Capture.Root {
		t.Errorf("Capture.Root: got %q, want %q", got.Capture.Root, want.Capture.Root)
	}
}

func TestReadRepoConfig_MissingFile(t *testing.T) {
	s, err := Init(t.TempDir())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	cfg, err := s.ReadRepoConfig()
	if err != nil {
		t.Fatalf("want nil error for fresh store, got %v", err)
	}
	if cfg.Remote.URL != "" || cfg.Remote.RepoID != "" {
		t.Errorf("want zero config for fresh store, got %+v", cfg)
	}
}

func TestWriteRepoConfig_Idempotent(t *testing.T) {
	s, err := Init(t.TempDir())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	cfg := RepoConfig{Remote: RemoteConfig{URL: "https://example.com", RepoID: "id1"}}
	if err := s.WriteRepoConfig(cfg); err != nil {
		t.Fatalf("first WriteRepoConfig: %v", err)
	}
	if err := s.WriteRepoConfig(cfg); err != nil {
		t.Fatalf("second WriteRepoConfig: %v", err)
	}
	got, err := s.ReadRepoConfig()
	if err != nil {
		t.Fatalf("ReadRepoConfig: %v", err)
	}
	if got.Remote.RepoID != cfg.Remote.RepoID {
		t.Errorf("RepoID: got %q, want %q", got.Remote.RepoID, cfg.Remote.RepoID)
	}
}
