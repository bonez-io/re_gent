package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRemoteBindingMissingFile(t *testing.T) {
	b, err := LoadRemoteBinding(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("LoadRemoteBinding missing file: %v", err)
	}
	if b.URL != "" || b.ProjectID != "" || b.RepoID != "" || b.Connected() {
		t.Fatalf("expected zero binding, got %+v", b)
	}
}

// Legacy shape: only repo_id. Key() must resolve to it.
func TestLoadRemoteBindingLegacyRepoIDShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	write(t, path, "[remote]\nurl = \"https://regent.example.com\"\nrepo_id = \"github.com-acme-api\"\n")

	b, err := LoadRemoteBinding(path)
	if err != nil {
		t.Fatalf("LoadRemoteBinding: %v", err)
	}
	if b.RepoID != "github.com-acme-api" {
		t.Errorf("RepoID = %q", b.RepoID)
	}
	if b.ProjectID != "" {
		t.Errorf("ProjectID = %q, want empty for a legacy binding", b.ProjectID)
	}
	if got := b.Key(); got != "github.com-acme-api" {
		t.Errorf("Key() = %q, want the legacy repo_id", got)
	}
	if !b.Connected() {
		t.Errorf("legacy binding should read as connected")
	}
}

// New shape: only project_id. Key() must resolve to it.
func TestLoadRemoteBindingProjectIDShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	write(t, path, "[remote]\nurl = \"https://app.regent.dev\"\nproject_id = \"prj_2f9c1a4b7d3e6081\"\n")

	b, err := LoadRemoteBinding(path)
	if err != nil {
		t.Fatalf("LoadRemoteBinding: %v", err)
	}
	if b.ProjectID != "prj_2f9c1a4b7d3e6081" {
		t.Errorf("ProjectID = %q", b.ProjectID)
	}
	if b.RepoID != "" {
		t.Errorf("RepoID = %q, want empty for a project-id binding", b.RepoID)
	}
	if got := b.Key(); got != "prj_2f9c1a4b7d3e6081" {
		t.Errorf("Key() = %q, want the project id", got)
	}
}

// A file that somehow carries both must resolve to one answer: project_id
// wins, because it is the more recent, server-generated, immutable identity.
func TestLoadRemoteBindingBothPresentProjectIDWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	write(t, path, "[remote]\nurl = \"https://app.regent.dev\"\nproject_id = \"prj_new\"\nrepo_id = \"legacy-id\"\n")

	b, err := LoadRemoteBinding(path)
	if err != nil {
		t.Fatalf("LoadRemoteBinding: %v", err)
	}
	if got := b.Key(); got != "prj_new" {
		t.Errorf("Key() = %q, want project_id to win over repo_id", got)
	}
}

func TestSaveRemoteBindingRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	want := RemoteBinding{URL: "https://app.regent.dev", ProjectID: "prj_abc"}
	if err := SaveRemoteBinding(path, want); err != nil {
		t.Fatalf("SaveRemoteBinding: %v", err)
	}
	got, err := LoadRemoteBinding(path)
	if err != nil {
		t.Fatalf("LoadRemoteBinding: %v", err)
	}
	if got != want {
		t.Errorf("round trip: got %+v, want %+v", got, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "repo_id") {
		t.Errorf("a project-id binding must not also write repo_id:\n%s", data)
	}
}

// Writing the [remote] table must not disturb [capture] or any other table
// already in the file — that is the whole reason this package merges instead
// of marshaling a fixed struct.
func TestSaveRemoteBindingPreservesOtherTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	write(t, path, "[capture]\nroot = \"project\"\n")

	if err := SaveRemoteBinding(path, RemoteBinding{URL: "https://app.regent.dev", ProjectID: "prj_abc"}); err != nil {
		t.Fatalf("SaveRemoteBinding: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[capture]") || !strings.Contains(string(data), "root") {
		t.Errorf("SaveRemoteBinding dropped the [capture] table:\n%s", data)
	}
	b, err := LoadRemoteBinding(path)
	if err != nil {
		t.Fatalf("LoadRemoteBinding: %v", err)
	}
	if b.ProjectID != "prj_abc" {
		t.Errorf("ProjectID = %q", b.ProjectID)
	}
}

func TestClearRemoteBindingRemovesRemoteKeepsCapture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	write(t, path, "[remote]\nurl = \"https://app.regent.dev\"\nproject_id = \"prj_abc\"\n\n[capture]\nroot = \"project\"\n")

	if err := ClearRemoteBinding(path); err != nil {
		t.Fatalf("ClearRemoteBinding: %v", err)
	}

	b, err := LoadRemoteBinding(path)
	if err != nil {
		t.Fatalf("LoadRemoteBinding: %v", err)
	}
	if b.Connected() {
		t.Errorf("binding still reads as connected after ClearRemoteBinding: %+v", b)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[capture]") || !strings.Contains(string(data), "root") {
		t.Errorf("ClearRemoteBinding dropped the [capture] table:\n%s", data)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
