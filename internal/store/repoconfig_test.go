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

// TestWriteRepoConfig_ProjectID_RoundTrip pins the RFC 0004 binding gap:
// RemoteConfig has to carry ProjectID through both directions of the
// [remote] table, not just RepoID, or every read-modify-write caller
// (recordCaptureRoot in internal/cli/init.go is the one in this codebase
// today) silently drops a project-id binding the moment it rewrites any
// other field in the same file.
func TestWriteRepoConfig_ProjectID_RoundTrip(t *testing.T) {
	s, err := Init(t.TempDir())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	want := RepoConfig{
		Remote:  RemoteConfig{URL: "https://example.com", ProjectID: "prj_deadbeefcafebabe"},
		Capture: CaptureConfig{Root: "project"},
	}
	if err := s.WriteRepoConfig(want); err != nil {
		t.Fatalf("WriteRepoConfig: %v", err)
	}
	got, err := s.ReadRepoConfig()
	if err != nil {
		t.Fatalf("ReadRepoConfig: %v", err)
	}
	if got.Remote.ProjectID != want.Remote.ProjectID {
		t.Errorf("ProjectID: got %q, want %q", got.Remote.ProjectID, want.Remote.ProjectID)
	}
	if got.Remote.RepoID != "" {
		t.Errorf("RepoID: got %q, want empty for a project-id binding", got.Remote.RepoID)
	}

	// The read-modify-write a caller like recordCaptureRoot performs must not
	// lose the binding just read: change an unrelated field and write the
	// whole struct back, the way every real caller does.
	got.Capture.Root = "workspace"
	if err := s.WriteRepoConfig(got); err != nil {
		t.Fatalf("WriteRepoConfig (read-modify-write): %v", err)
	}
	after, err := s.ReadRepoConfig()
	if err != nil {
		t.Fatalf("ReadRepoConfig after read-modify-write: %v", err)
	}
	if after.Remote.ProjectID != want.Remote.ProjectID {
		t.Errorf("project_id lost after an unrelated read-modify-write: got %q, want %q", after.Remote.ProjectID, want.Remote.ProjectID)
	}
	if after.Remote.URL != want.Remote.URL {
		t.Errorf("url lost after an unrelated read-modify-write: got %q, want %q", after.Remote.URL, want.Remote.URL)
	}
	if after.Capture.Root != "workspace" {
		t.Errorf("Capture.Root: got %q, want %q", after.Capture.Root, "workspace")
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
