package architecture

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var forbiddenStorePolicyImports = map[string]bool{
	"github.com/bonez-io/re_gent/internal/remote": true,
	"github.com/bonez-io/re_gent/internal/server": true,
}

// TestStoreChoiceStaysAtTheCommandEdge prevents the historical regression
// where reads and capture each made their own remote-cache choice. The command
// edge may import remote; these data-plane packages may not.
func TestStoreChoiceStaysAtTheCommandEdge(t *testing.T) {
	root := repoRoot(t)
	paths := []string{filepath.Join(root, "internal", "capture")}
	for _, name := range []string{"log.go", "show.go", "blame.go", "sessions.go", "status.go", "cat.go", "rewind.go", "repair.go"} {
		paths = append(paths, filepath.Join(root, "internal", "cli", name))
	}
	for _, path := range paths {
		if violations := forbiddenImports(path); len(violations) != 0 {
			t.Errorf("%s bypasses the command-edge store choice by importing %s", path, strings.Join(violations, ", "))
		}
	}
}

// This proves the guard tests source imports rather than merely documenting a
// convention: the exact forbidden mutation reported in the issue is rejected.
func TestForbiddenStorePolicyImportIsRejected(t *testing.T) {
	file := filepath.Join(t.TempDir(), "reader.go")
	if err := os.WriteFile(file, []byte("package reader\nimport \"github.com/bonez-io/re_gent/internal/remote\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := forbiddenImports(file); len(got) != 1 || got[0] != "github.com/bonez-io/re_gent/internal/remote" {
		t.Fatalf("forbidden import = %v, want remote rejected", got)
	}
}

func forbiddenImports(path string) []string {
	info, err := os.Stat(path)
	if err != nil {
		return []string{err.Error()}
	}
	files := []string{path}
	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return []string{err.Error()}
		}
		files = files[:0]
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".go") && !strings.HasSuffix(entry.Name(), "_test.go") {
				files = append(files, filepath.Join(path, entry.Name()))
			}
		}
	}
	var found []string
	for _, file := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			return []string{err.Error()}
		}
		for _, spec := range parsed.Imports {
			path := strings.Trim(spec.Path.Value, "\"")
			if forbiddenStorePolicyImports[path] {
				found = append(found, path)
			}
		}
	}
	return found
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		next := filepath.Dir(dir)
		if next == dir {
			t.Fatal("repository root not found")
		}
		dir = next
	}
}
