package store

import (
	"strings"
	"testing"
)

func TestComputeBlame_NewFile(t *testing.T) {
	// New file (no old content/blame)
	newContent := []byte("line1\nline2\nline3\n")
	currentStep := Hash("step123")

	blame := ComputeBlame(nil, newContent, nil, currentStep)

	if len(blame.Lines) != 3 {
		t.Fatalf("Expected 3 lines, got %d", len(blame.Lines))
	}

	for i, h := range blame.Lines {
		if h != currentStep {
			t.Errorf("Line %d: expected %s, got %s", i+1, currentStep, h)
		}
	}
}

func TestComputeBlame_ModifyLine(t *testing.T) {
	oldContent := []byte("line1\nold line\nline3\n")
	oldBlame := &BlameMap{
		Lines: []Hash{"stepA", "stepA", "stepA"},
	}

	newContent := []byte("line1\nnew line\nline3\n")
	currentStep := Hash("stepB")

	blame := ComputeBlame(oldContent, newContent, oldBlame, currentStep)

	if len(blame.Lines) != 3 {
		t.Fatalf("Expected 3 lines, got %d", len(blame.Lines))
	}

	// Line 1 and 3 should keep stepA, line 2 should be stepB
	if blame.Lines[0] != "stepA" {
		t.Errorf("Line 1 should keep old attribution, got %s", blame.Lines[0])
	}
	if blame.Lines[1] != "stepB" {
		t.Errorf("Line 2 should be attributed to currentStep, got %s", blame.Lines[1])
	}
	if blame.Lines[2] != "stepA" {
		t.Errorf("Line 3 should keep old attribution, got %s", blame.Lines[2])
	}
}

func TestComputeBlame_InsertLine(t *testing.T) {
	oldContent := []byte("line1\nline3\n")
	oldBlame := &BlameMap{
		Lines: []Hash{"stepA", "stepA"},
	}

	newContent := []byte("line1\nline2\nline3\n")
	currentStep := Hash("stepB")

	blame := ComputeBlame(oldContent, newContent, oldBlame, currentStep)

	if len(blame.Lines) != 3 {
		t.Fatalf("Expected 3 lines, got %d", len(blame.Lines))
	}

	// Line 1: stepA, Line 2: stepB (inserted), Line 3: stepA
	if blame.Lines[0] != "stepA" {
		t.Errorf("Line 1 should be stepA, got %s", blame.Lines[0])
	}
	if blame.Lines[1] != "stepB" {
		t.Errorf("Inserted line should be attributed to currentStep, got %s", blame.Lines[1])
	}
	if blame.Lines[2] != "stepA" {
		t.Errorf("Line 3 should be stepA, got %s", blame.Lines[2])
	}
}

func TestComputeBlame_DeleteLine(t *testing.T) {
	oldContent := []byte("line1\nline2\nline3\n")
	oldBlame := &BlameMap{
		Lines: []Hash{"stepA", "stepB", "stepA"},
	}

	newContent := []byte("line1\nline3\n")
	currentStep := Hash("stepC")

	blame := ComputeBlame(oldContent, newContent, oldBlame, currentStep)

	if len(blame.Lines) != 2 {
		t.Fatalf("Expected 2 lines, got %d", len(blame.Lines))
	}

	// Line 1 and 2 should be stepA (old line 2 deleted)
	if blame.Lines[0] != "stepA" {
		t.Errorf("Line 1 should be stepA, got %s", blame.Lines[0])
	}
	if blame.Lines[1] != "stepA" {
		t.Errorf("Line 2 should be stepA, got %s", blame.Lines[1])
	}
}

func TestComputeBlame_EmptyFile(t *testing.T) {
	newContent := []byte("")
	currentStep := Hash("step123")

	blame := ComputeBlame(nil, newContent, nil, currentStep)

	if len(blame.Lines) != 0 {
		t.Errorf("Expected 0 lines for empty file, got %d", len(blame.Lines))
	}
}

func TestComputeBlame_NoOldBlame(t *testing.T) {
	// File existed but was created before blame tracking (Phase 2)
	oldContent := []byte("line1\nline2\n")
	newContent := []byte("line1\nmodified\n")
	currentStep := Hash("stepB")

	blame := ComputeBlame(oldContent, newContent, nil, currentStep)

	if len(blame.Lines) != 2 {
		t.Fatalf("Expected 2 lines, got %d", len(blame.Lines))
	}

	// Equal line gets attributed to currentStep (no old blame)
	if blame.Lines[0] != "stepB" {
		t.Errorf("Line 1 should be stepB (no old blame), got %s", blame.Lines[0])
	}
	// Modified line gets currentStep
	if blame.Lines[1] != "stepB" {
		t.Errorf("Line 2 should be stepB, got %s", blame.Lines[1])
	}
}

func TestReadWriteBlame(t *testing.T) {
	// Integration test: write and read back
	workspace := t.TempDir()
	s, err := Init(workspace)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	original := &BlameMap{
		Lines: []Hash{"step1", "step2", "step3"},
	}

	hash, err := s.WriteBlame(original)
	if err != nil {
		t.Fatalf("WriteBlame failed: %v", err)
	}

	retrieved, err := s.ReadBlame(hash)
	if err != nil {
		t.Fatalf("ReadBlame failed: %v", err)
	}

	if len(retrieved.Lines) != len(original.Lines) {
		t.Fatalf("Expected %d lines, got %d", len(original.Lines), len(retrieved.Lines))
	}

	for i := range original.Lines {
		if retrieved.Lines[i] != original.Lines[i] {
			t.Errorf("Line %d: expected %s, got %s", i, original.Lines[i], retrieved.Lines[i])
		}
	}
}

// Reproduces a defect seen against a real project: changing one line credited
// the NEXT line to the new step, and left the changed line attributed to the
// step before it. Blame's whole claim is "this line came from that prompt", so
// being off by one makes every answer wrong for edited files.
func TestComputeBlameAttributesTheLineThatActuallyChanged(t *testing.T) {
	// This is deliberately the 36-line package.json fixture that exposed the
	// defect. The broken line-mode round-trip only shifts chunks for a sufficiently
	// large input; a six-line fixture passes under both implementations.
	old := []byte(`{
  "name": "job-hunter",
  "version": "0.1.0",
  "description": "AI-powered job hunting agent",
  "main": "dist/index.js",
  "bin": {
    "job-hunter": "dist/index.js"
  },
  "scripts": {
    "dev": "tsx src/index.ts",
    "build": "tsc",
    "start": "tsx src/index.ts",
    "typecheck": "tsc --noEmit",
    "test": "node --import tsx --test src/**/*.test.ts"
  },
  "dependencies": {
    "openai": "^4.67.0",
    "better-sqlite3": "^9.6.0",
    "chalk": "^4.1.2",
    "commander": "^12.1.0",
    "dotenv": "^16.4.5",
    "inquirer": "^8.2.6",
    "mammoth": "^1.8.0",
    "ora": "^5.4.1",
    "pdf-parse": "^1.1.1",
    "zod": "^3.23.8"
  },
  "devDependencies": {
    "@types/better-sqlite3": "^7.6.11",
    "@types/inquirer": "^8.2.10",
    "@types/node": "^22.7.0",
    "@types/pdf-parse": "^1.1.4",
    "tsx": "^4.19.1",
    "typescript": "^5.6.2"
  }
}
`)
	new_ := []byte(`{
  "name": "job-hunter",
  "version": "0.1.1",
  "description": "AI-powered job hunting agent",
  "main": "dist/index.js",
  "bin": {
    "job-hunter": "dist/index.js"
  },
  "scripts": {
    "dev": "tsx src/index.ts",
    "build": "tsc",
    "start": "tsx src/index.ts",
    "typecheck": "tsc --noEmit",
    "test": "node --import tsx --test src/**/*.test.ts"
  },
  "dependencies": {
    "openai": "^4.67.0",
    "better-sqlite3": "^9.6.0",
    "chalk": "^4.1.2",
    "commander": "^12.1.0",
    "dotenv": "^16.4.5",
    "inquirer": "^8.2.6",
    "mammoth": "^1.8.0",
    "ora": "^5.4.1",
    "pdf-parse": "^1.1.1",
    "zod": "^3.23.8"
  },
  "devDependencies": {
    "@types/better-sqlite3": "^7.6.11",
    "@types/inquirer": "^8.2.10",
    "@types/node": "^22.7.0",
    "@types/pdf-parse": "^1.1.4",
    "tsx": "^4.19.1",
    "typescript": "^5.6.2"
  }
}
`)

	const first, second = Hash("aaaa"), Hash("bbbb")
	oldBlame := &BlameMap{Lines: make([]Hash, 36)}
	for i := range oldBlame.Lines {
		oldBlame.Lines[i] = first
	}

	got := ComputeBlame(old, new_, oldBlame, second)

	if len(got.Lines) != 36 {
		t.Fatalf("blame has %d lines, want 36", len(got.Lines))
	}
	// Line 3 (index 2) is the only line that changed.
	if got.Lines[2] != second {
		t.Errorf("the changed line (\"version\") is attributed to %q, want the new step %q", got.Lines[2], second)
	}
	for i := range got.Lines {
		if i == 2 {
			continue
		}
		if got.Lines[i] != first {
			t.Errorf("line %d did not change but is attributed to %q, want %q", i+1, got.Lines[i], first)
		}
	}
}

func TestWriteBlameForFileNeverShowsAPartialMapToAConcurrentReader(t *testing.T) {
	// `rgt repair blame` rewrites every sidecar in the store, so a run can be
	// killed in the middle of one. A truncate-in-place write leaves half a JSON
	// document behind, and `rgt blame` on that file then fails to decode — a
	// repair that can corrupt the thing it repairs is not a repair. A reader
	// racing the writer stands in for the kill: it must never see anything but
	// a whole map.
	s, err := Init(t.TempDir())
	if err != nil {
		t.Fatalf("init store: %v", err)
	}

	const step, path, lines = Hash("step-atomic"), "src/big.ts", 15000
	mapOf := func(fill string) *BlameMap {
		bm := &BlameMap{Lines: make([]Hash, lines)}
		for i := range bm.Lines {
			bm.Lines[i] = Hash(strings.Repeat(fill, 64))
		}
		return bm
	}
	a, b := mapOf("a"), mapOf("b")

	if err := s.WriteBlameForFile(step, path, a); err != nil {
		t.Fatalf("seed blame: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		for i := 0; i < 20; i++ {
			write := a
			if i%2 == 1 {
				write = b
			}
			if err := s.WriteBlameForFile(step, path, write); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	for reads := 0; ; reads++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("concurrent write: %v", err)
			}
			if reads == 0 {
				t.Fatal("the reader never got a turn; the race this test describes was not exercised")
			}
			return
		default:
		}

		got, err := s.ReadBlameForFile(step, path)
		if err != nil {
			t.Fatalf("read %d saw a map mid-write: %v", reads, err)
		}
		if len(got.Lines) != lines {
			t.Fatalf("read %d saw %d lines, want %d — the file was replaced in place, not atomically", reads, len(got.Lines), lines)
		}
	}
}
