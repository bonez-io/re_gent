package store

import (
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
	old := []byte("{\n  \"name\": \"job-hunter\",\n  \"version\": \"0.1.0\",\n  \"description\": \"agent\",\n  \"main\": \"dist/index.js\"\n}\n")
	new_ := []byte("{\n  \"name\": \"job-hunter\",\n  \"version\": \"0.1.1\",\n  \"description\": \"agent\",\n  \"main\": \"dist/index.js\"\n}\n")

	const first, second = Hash("aaaa"), Hash("bbbb")
	oldBlame := &BlameMap{Lines: []Hash{first, first, first, first, first, first}}

	got := ComputeBlame(old, new_, oldBlame, second)

	if len(got.Lines) != 6 {
		t.Fatalf("blame has %d lines, want 6", len(got.Lines))
	}
	// Line 3 (index 2) is the only line that changed.
	if got.Lines[2] != second {
		t.Errorf("the changed line (\"version\") is attributed to %q, want the new step %q", got.Lines[2], second)
	}
	for _, i := range []int{0, 1, 3, 4, 5} {
		if got.Lines[i] != first {
			t.Errorf("line %d did not change but is attributed to %q, want %q", i+1, got.Lines[i], first)
		}
	}
}
