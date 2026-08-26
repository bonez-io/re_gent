package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/regent-vcs/regent/internal/store"
)

// TestAPIDiffAddedModifiedDeleted builds a parent/child step pair touching
// three files (added, modified, deleted) and asserts each is reported with
// the right status and correct old_number/new_number on every line kind.
func TestAPIDiffAddedModifiedDeleted(t *testing.T) {
	_, _, ts := newTestServer(t)
	const repo = "alpha"

	_, unchangedBlob := putObject(t, ts, repo, []byte("keep me\n"))
	_, modOldBlob := putObject(t, ts, repo, []byte("line one\nline two\nline three\n"))
	_, modNewBlob := putObject(t, ts, repo, []byte("line one\nline TWO\nline three\n"))
	_, deletedBlob := putObject(t, ts, repo, []byte("bye\n"))
	_, addedBlob := putObject(t, ts, repo, []byte("hello\nworld\n"))

	parentTree := putTree(t, ts, repo,
		store.TreeEntry{Path: "unchanged.txt", Blob: unchangedBlob, Mode: 0o644},
		store.TreeEntry{Path: "mod.txt", Blob: modOldBlob, Mode: 0o644},
		store.TreeEntry{Path: "gone.txt", Blob: deletedBlob, Mode: 0o644},
	)
	childTree := putTree(t, ts, repo,
		store.TreeEntry{Path: "unchanged.txt", Blob: unchangedBlob, Mode: 0o644},
		store.TreeEntry{Path: "mod.txt", Blob: modNewBlob, Mode: 0o644},
		store.TreeEntry{Path: "new.txt", Blob: addedBlob, Mode: 0o644},
	)

	parentHash := putStep(t, ts, repo, store.Step{
		Tree: parentTree, SessionID: "s1", Origin: "claude_code",
		TimestampNanos: time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC).UnixNano(),
	})
	childHash := putStep(t, ts, repo, store.Step{
		Tree: childTree, Parent: parentHash, SessionID: "s1", Origin: "claude_code",
		TimestampNanos: time.Date(2026, 8, 2, 9, 5, 0, 0, time.UTC).UnixNano(),
	})

	status, body := getAPI(t, ts, "/"+repo+"/api/diff?step="+string(childHash), "")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	var got diffResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, body)
	}
	if got.StepHash != string(childHash) || got.ParentHash != string(parentHash) {
		t.Fatalf("hashes = %+v", got)
	}
	if got.TotalFiles != 3 {
		t.Fatalf("total_files = %d, want 3 (body %s)", got.TotalFiles, body)
	}

	byPath := map[string]diffFileJSON{}
	for _, f := range got.Files {
		byPath[f.Path] = f
	}

	added, ok := byPath["new.txt"]
	if !ok {
		t.Fatalf("missing added file, got %+v", byPath)
	}
	if added.Status != "added" {
		t.Errorf("added.status = %q, want added", added.Status)
	}
	if len(added.Hunks) != 1 {
		t.Fatalf("added.hunks = %d, want 1", len(added.Hunks))
	}
	if len(added.Hunks[0].Lines) != 2 {
		t.Fatalf("added.hunks[0].lines = %d, want 2", len(added.Hunks[0].Lines))
	}
	for i, ln := range added.Hunks[0].Lines {
		if ln.Kind != "add" {
			t.Errorf("added line %d kind = %q, want add", i, ln.Kind)
		}
		if ln.OldNumber != 0 {
			t.Errorf("added line %d old_number = %d, want 0 (omitted)", i, ln.OldNumber)
		}
		if ln.NewNumber != i+1 {
			t.Errorf("added line %d new_number = %d, want %d", i, ln.NewNumber, i+1)
		}
	}
	if added.Hunks[0].Lines[0].Content != "hello" || added.Hunks[0].Lines[1].Content != "world" {
		t.Errorf("added hunk content = %+v", added.Hunks[0].Lines)
	}

	deleted, ok := byPath["gone.txt"]
	if !ok {
		t.Fatalf("missing deleted file, got %+v", byPath)
	}
	if deleted.Status != "deleted" {
		t.Errorf("deleted.status = %q, want deleted", deleted.Status)
	}
	if len(deleted.Hunks) != 1 || len(deleted.Hunks[0].Lines) != 1 {
		t.Fatalf("deleted hunks = %+v", deleted.Hunks)
	}
	dl := deleted.Hunks[0].Lines[0]
	if dl.Kind != "delete" || dl.OldNumber != 1 || dl.NewNumber != 0 || dl.Content != "bye" {
		t.Errorf("deleted line = %+v", dl)
	}

	modified, ok := byPath["mod.txt"]
	if !ok {
		t.Fatalf("missing modified file, got %+v", byPath)
	}
	if modified.Status != "modified" {
		t.Errorf("modified.status = %q, want modified", modified.Status)
	}
	if len(modified.Hunks) != 1 {
		t.Fatalf("modified.hunks = %d, want 1 (body %s)", len(modified.Hunks), body)
	}
	h := modified.Hunks[0]
	// line one (context,1,1) line two (delete,2) line TWO (add,2) line three (context,3,3)
	wantKinds := []string{"context", "delete", "add", "context"}
	if len(h.Lines) != len(wantKinds) {
		t.Fatalf("modified hunk lines = %+v, want %d lines", h.Lines, len(wantKinds))
	}
	for i, k := range wantKinds {
		if h.Lines[i].Kind != k {
			t.Errorf("modified line %d kind = %q, want %q", i, h.Lines[i].Kind, k)
		}
	}
	if h.Lines[0].OldNumber != 1 || h.Lines[0].NewNumber != 1 || h.Lines[0].Content != "line one" {
		t.Errorf("context line 0 = %+v", h.Lines[0])
	}
	if h.Lines[1].OldNumber != 2 || h.Lines[1].NewNumber != 0 || h.Lines[1].Content != "line two" {
		t.Errorf("delete line = %+v", h.Lines[1])
	}
	if h.Lines[2].OldNumber != 0 || h.Lines[2].NewNumber != 2 || h.Lines[2].Content != "line TWO" {
		t.Errorf("add line = %+v", h.Lines[2])
	}
	if h.Lines[3].OldNumber != 3 || h.Lines[3].NewNumber != 3 || h.Lines[3].Content != "line three" {
		t.Errorf("context line 3 = %+v", h.Lines[3])
	}

	// unchanged.txt has an identical blob in both trees, so treediff never
	// reports it at all.
	if _, ok := byPath["unchanged.txt"]; ok {
		t.Errorf("unchanged.txt should not appear in the diff")
	}
}

// TestAPIDiffNoParent covers the first step of a session: parent_hash must be
// the empty string and every file in the tree is reported as added.
func TestAPIDiffNoParent(t *testing.T) {
	_, _, ts := newTestServer(t)
	const repo = "alpha"

	_, blob := putObject(t, ts, repo, []byte("first content\n"))
	tree := putTree(t, ts, repo, store.TreeEntry{Path: "a.txt", Blob: blob, Mode: 0o644})
	rootHash := putStep(t, ts, repo, store.Step{
		Tree: tree, SessionID: "s1", Origin: "claude_code",
		TimestampNanos: time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC).UnixNano(),
	})

	status, body := getAPI(t, ts, "/"+repo+"/api/diff?step="+string(rootHash), "")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	var got diffResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, body)
	}
	if got.ParentHash != "" {
		t.Errorf("parent_hash = %q, want empty", got.ParentHash)
	}
	if got.TotalFiles != 1 || got.Files[0].Status != "added" {
		t.Fatalf("files = %+v, want one added file", got.Files)
	}
}

// TestAPIDiffContextMerging asserts the standard unified-diff grouping rule:
// two changes far enough apart (more than 2*context unchanged lines between
// them) produce two hunks, while two changes close together share one.
func TestAPIDiffContextMerging(t *testing.T) {
	_, _, ts := newTestServer(t)
	const repo = "alpha"

	// 20 unchanged lines between the two edits: far more than 2*3, so they
	// must land in separate hunks.
	oldLines := make([]string, 0, 24)
	oldLines = append(oldLines, "change-a-old")
	for i := 0; i < 20; i++ {
		oldLines = append(oldLines, fmt.Sprintf("filler-%d", i))
	}
	oldLines = append(oldLines, "change-b-old")
	oldContent := strings.Join(oldLines, "\n") + "\n"

	newLines := make([]string, len(oldLines))
	copy(newLines, oldLines)
	newLines[0] = "change-a-new"
	newLines[len(newLines)-1] = "change-b-new"
	newContent := strings.Join(newLines, "\n") + "\n"

	_, oldBlob := putObject(t, ts, repo, []byte(oldContent))
	_, newBlob := putObject(t, ts, repo, []byte(newContent))
	parentTree := putTree(t, ts, repo, store.TreeEntry{Path: "f.txt", Blob: oldBlob, Mode: 0o644})
	childTree := putTree(t, ts, repo, store.TreeEntry{Path: "f.txt", Blob: newBlob, Mode: 0o644})
	parentHash := putStep(t, ts, repo, store.Step{Tree: parentTree, SessionID: "s1", Origin: "claude_code", TimestampNanos: 1})
	childHash := putStep(t, ts, repo, store.Step{Tree: childTree, Parent: parentHash, SessionID: "s1", Origin: "claude_code", TimestampNanos: 2})

	status, body := getAPI(t, ts, "/"+repo+"/api/diff?step="+string(childHash), "")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	var got diffResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, body)
	}
	if len(got.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(got.Files))
	}
	if far := got.Files[0]; len(far.Hunks) != 2 {
		t.Fatalf("far-apart edits: hunks = %d, want 2 (body %s)", len(far.Hunks), body)
	}

	// Now move the edits close together: one line apart, well within 2*context,
	// so the two changes must merge into a single hunk.
	closeOld := []string{"a-old", "mid1", "mid2", "b-old"}
	closeNew := []string{"a-new", "mid1", "mid2", "b-new"}
	_, closeOldBlob := putObject(t, ts, repo, []byte(strings.Join(closeOld, "\n")+"\n"))
	_, closeNewBlob := putObject(t, ts, repo, []byte(strings.Join(closeNew, "\n")+"\n"))
	closeParentTree := putTree(t, ts, repo, store.TreeEntry{Path: "g.txt", Blob: closeOldBlob, Mode: 0o644})
	closeChildTree := putTree(t, ts, repo, store.TreeEntry{Path: "g.txt", Blob: closeNewBlob, Mode: 0o644})
	closeParentHash := putStep(t, ts, repo, store.Step{Tree: closeParentTree, SessionID: "s2", Origin: "claude_code", TimestampNanos: 1})
	closeChildHash := putStep(t, ts, repo, store.Step{Tree: closeChildTree, Parent: closeParentHash, SessionID: "s2", Origin: "claude_code", TimestampNanos: 2})

	status, body = getAPI(t, ts, "/"+repo+"/api/diff?step="+string(closeChildHash), "")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	var gotClose diffResponse
	if err := json.Unmarshal(body, &gotClose); err != nil {
		t.Fatalf("decode: %v (body %s)", err, body)
	}
	if len(gotClose.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(gotClose.Files))
	}
	if near := gotClose.Files[0]; len(near.Hunks) != 1 {
		t.Fatalf("close-together edits: hunks = %d, want 1 (body %s)", len(near.Hunks), body)
	}
}

// TestAPIDiffBinaryFile asserts a binary file is reported with is_binary=true,
// an empty hunks array, and no attempt to dump its bytes into JSON.
func TestAPIDiffBinaryFile(t *testing.T) {
	_, _, ts := newTestServer(t)
	const repo = "alpha"

	oldBinary := []byte{0x00, 0x01, 0x02, 0xff, 0x00}
	newBinary := []byte{0x00, 0x9, 0x02, 0xff, 0x00, 0x00}
	_, oldBlob := putObject(t, ts, repo, oldBinary)
	_, newBlob := putObject(t, ts, repo, newBinary)
	parentTree := putTree(t, ts, repo, store.TreeEntry{Path: "img.png", Blob: oldBlob, Mode: 0o644})
	childTree := putTree(t, ts, repo, store.TreeEntry{Path: "img.png", Blob: newBlob, Mode: 0o644})
	parentHash := putStep(t, ts, repo, store.Step{Tree: parentTree, SessionID: "s1", Origin: "claude_code", TimestampNanos: 1})
	childHash := putStep(t, ts, repo, store.Step{Tree: childTree, Parent: parentHash, SessionID: "s1", Origin: "claude_code", TimestampNanos: 2})

	status, body := getAPI(t, ts, "/"+repo+"/api/diff?step="+string(childHash), "")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	var got diffResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, body)
	}
	if len(got.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(got.Files))
	}
	f := got.Files[0]
	if !f.IsBinary {
		t.Errorf("is_binary = false, want true")
	}
	if f.Status != "modified" {
		t.Errorf("status = %q, want modified", f.Status)
	}
	if len(f.Hunks) != 0 {
		t.Errorf("hunks = %+v, want empty", f.Hunks)
	}
	if strings.Contains(string(body), "\\u0000") || strings.Contains(string(body), string(rune(0x00))) {
		// The raw NUL byte should never have made it into a hunk's content;
		// this is a best-effort guard, the empty-hunks assertion above is the
		// real one.
		t.Errorf("response may contain raw binary bytes: %s", body)
	}
}

// TestAPIDiffTruncatesOversizedFile asserts a file whose diff exceeds the
// per-file line budget is still reported (status, additions, deletions) with
// truncated=true, and the emitted line count never exceeds the cap.
func TestAPIDiffTruncatesOversizedFile(t *testing.T) {
	_, _, ts := newTestServer(t)
	const repo = "alpha"

	// Every line differs, comfortably exceeding maxDiffLinesPerFile once both
	// the delete and add sides are counted.
	const n = maxDiffLinesPerFile
	oldLines := make([]string, n)
	newLines := make([]string, n)
	for i := 0; i < n; i++ {
		oldLines[i] = fmt.Sprintf("old-%d", i)
		newLines[i] = fmt.Sprintf("new-%d", i)
	}
	_, oldBlob := putObject(t, ts, repo, []byte(strings.Join(oldLines, "\n")+"\n"))
	_, newBlob := putObject(t, ts, repo, []byte(strings.Join(newLines, "\n")+"\n"))
	parentTree := putTree(t, ts, repo, store.TreeEntry{Path: "big.txt", Blob: oldBlob, Mode: 0o644})
	childTree := putTree(t, ts, repo, store.TreeEntry{Path: "big.txt", Blob: newBlob, Mode: 0o644})
	parentHash := putStep(t, ts, repo, store.Step{Tree: parentTree, SessionID: "s1", Origin: "claude_code", TimestampNanos: 1})
	childHash := putStep(t, ts, repo, store.Step{Tree: childTree, Parent: parentHash, SessionID: "s1", Origin: "claude_code", TimestampNanos: 2})

	status, body := getAPI(t, ts, "/"+repo+"/api/diff?step="+string(childHash), "")
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	var got diffResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, body)
	}
	if len(got.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(got.Files))
	}
	f := got.Files[0]
	if !f.Truncated {
		t.Errorf("truncated = false, want true")
	}
	if f.Status != "modified" {
		t.Errorf("status = %q, want modified", f.Status)
	}
	total := 0
	for _, h := range f.Hunks {
		total += len(h.Lines)
	}
	if total > maxDiffLinesPerFile {
		t.Errorf("emitted %d lines, want <= %d", total, maxDiffLinesPerFile)
	}
	if total == 0 {
		t.Errorf("emitted 0 lines, want a truncated prefix")
	}
}

// TestAPIDiffErrors covers the 400/404 error contract: missing step param,
// malformed hash, and an unknown (well-formed but nonexistent) step.
func TestAPIDiffErrors(t *testing.T) {
	_, _, ts := newTestServer(t)
	createRepo(t, ts, "alpha")

	t.Run("missing step", func(t *testing.T) {
		status, body := getAPI(t, ts, "/alpha/api/diff", "")
		if status != http.StatusBadRequest {
			t.Fatalf("status %d, want 400 (body %s)", status, body)
		}
		var got map[string]string
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got["error"] != "missing step parameter" {
			t.Errorf("error = %q", got["error"])
		}
	})

	t.Run("malformed hash", func(t *testing.T) {
		status, body := getAPI(t, ts, "/alpha/api/diff?step=deadbeef", "")
		if status != http.StatusBadRequest {
			t.Fatalf("status %d, want 400 (body %s)", status, body)
		}
		var got map[string]string
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got["error"] != "invalid step hash" {
			t.Errorf("error = %q, want %q", got["error"], "invalid step hash")
		}
	})

	t.Run("unknown step", func(t *testing.T) {
		unknown := strings.Repeat("ab", 32)
		status, body := getAPI(t, ts, "/alpha/api/diff?step="+unknown, "")
		if status != http.StatusNotFound {
			t.Fatalf("status %d, want 404 (body %s)", status, body)
		}
		var got map[string]string
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got["error"] != "step not found" {
			t.Errorf("error = %q, want %q", got["error"], "step not found")
		}
	})
}
