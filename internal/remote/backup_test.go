package remote

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/bonez-io/re_gent/internal/remotetest"
)

func TestDownloadBackupWritesFileWithMode0600(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)
	content := []byte("fake tar bytes standing in for identity.db + projects.db")
	srv.EnableBackup(content)

	outPath := filepath.Join(t.TempDir(), "backup.tar")
	n, err := DownloadBackup(context.Background(), http.DefaultClient, srv.URL(), "", outPath)
	if err != nil {
		t.Fatalf("DownloadBackup: %v", err)
	}
	if n != int64(len(content)) {
		t.Errorf("wrote %d bytes, want %d", n, len(content))
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat backup file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 0600", info.Mode().Perm())
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", got, content)
	}
}

func TestDownloadBackupNotEnabledIsAnError(t *testing.T) {
	srv := remotetest.New()
	t.Cleanup(srv.Close)

	outPath := filepath.Join(t.TempDir(), "backup.tar")
	if _, err := DownloadBackup(context.Background(), http.DefaultClient, srv.URL(), "", outPath); err == nil {
		t.Fatal("expected an error when the server has no backup route enabled")
	}
	if _, statErr := os.Stat(outPath); statErr == nil {
		t.Error("a file was left behind despite the download failing")
	}
}
