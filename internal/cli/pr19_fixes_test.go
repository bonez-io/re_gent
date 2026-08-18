package cli

import (
	"errors"
	"testing"
)

// I1: deriveRepoID must not emit a server-reserved id for a folder that
// literally sanitizes to one (e.g. a directory named "repos"/"aux"/"nul").
func TestDeriveRepoID_AvoidsReservedNames(t *testing.T) {
	for _, dir := range []string{"/home/me/repos", "/x/aux", "/y/nul", "/z/com1"} {
		if got := deriveRepoID(dir); reservedRepoIDs[got] {
			t.Errorf("deriveRepoID(%q) = %q, which is a server-reserved id", dir, got)
		}
	}
	// Normal names must be untouched.
	if got := deriveRepoID("/home/me/girlfriend-assistant"); got != "girlfriend-assistant" {
		t.Errorf("deriveRepoID = %q, want girlfriend-assistant", got)
	}
}

// C1: merge in server mode would write to the disposable local cache without
// reaching the server, silently losing the result. It must refuse instead.
func TestRunMerge_RefusesInServerMode(t *testing.T) {
	t.Setenv("REGENT_SERVER_URL", "http://127.0.0.1:7654")
	t.Setenv("REGENT_REPO_ID", "test-repo")

	err := runMerge(t.TempDir(), "sessions/a", "sessions/b")
	if !errors.Is(err, errServerModeMergeUnsupported) {
		t.Fatalf("runMerge in server mode = %v, want errServerModeMergeUnsupported", err)
	}
}
