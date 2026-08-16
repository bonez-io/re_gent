package cli

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// mkProject creates dir under root and marks it as a project.
func mkProject(t *testing.T, root, dir string) string {
	t.Helper()
	full := filepath.Join(root, dir)
	if err := os.MkdirAll(filepath.Join(full, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", full, err)
	}
	return full
}

func TestDiscoverProjectsFindsNestedProjects(t *testing.T) {
	root := t.TempDir()
	api := mkProject(t, root, "api-service")
	web := mkProject(t, root, "acme/web-frontend") // one org level down

	// Noise that must never reach the menu.
	if err := os.MkdirAll(filepath.Join(root, "notes", "drafts"), 0o755); err != nil {
		t.Fatalf("mkdir notes: %v", err)
	}
	mkProject(t, root, "api-service/vendor/lib") // inside a skip dir
	mkProject(t, root, "api-service/sub")        // inside an already-found project
	mkProject(t, root, "a/b/c/too-deep")         // past maxDepth

	got := discoverProjects(root, maxDiscoveryDepth)
	// Sorted, so the org-nested project sorts before the top-level one.
	want := []string{web, api}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("discoverProjects:\n got %v\nwant %v", got, want)
	}
}

func TestDiscoverProjectsIncludesRootItself(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	mkProject(t, root, "nested")

	got := discoverProjects(root, maxDiscoveryDepth)
	// Standing inside a project, that project is the only sensible answer;
	// nested checkouts belong to it.
	if len(got) != 1 || got[0] != root {
		t.Errorf("discoverProjects = %v, want just the root %q", got, root)
	}
}

// TestConnectPickedWiresChosenProjects drives the picker end to end: two
// projects offered, one chosen, only that one wired.
func TestConnectPickedWiresChosenProjects(t *testing.T) {
	srv := newTestServer(t, http.StatusCreated, "picked-repo")
	root := t.TempDir()
	mkProject(t, root, "alpha")
	beta := mkProject(t, root, "beta")

	var out bytes.Buffer
	// "2" selects beta: discovery is sorted, so alpha is 1 and beta is 2.
	err := connectPicked(srv.URL, root, &out, strings.NewReader("2\n"), true)
	if err != nil {
		t.Fatalf("connectPicked: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(beta, ".regent", "config.toml")); statErr != nil {
		t.Errorf("chosen project should be wired: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, "alpha", ".regent")); statErr == nil {
		t.Error("unchosen project must not be wired")
	}
	if !strings.Contains(out.String(), "alpha") || !strings.Contains(out.String(), "beta") {
		t.Errorf("both projects should be listed; got:\n%s", out.String())
	}
}

// TestConnectPickedWithoutTerminalOnlyPrints guards the `curl | sh` case: with
// no terminal we must not guess, only report what to run.
func TestConnectPickedWithoutTerminalOnlyPrints(t *testing.T) {
	root := t.TempDir()
	alpha := mkProject(t, root, "alpha")

	var out bytes.Buffer
	if err := connectPicked("http://example.test", root, &out, strings.NewReader(""), false); err != nil {
		t.Fatalf("connectPicked: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(alpha, ".regent")); statErr == nil {
		t.Error("nothing may be wired without a terminal to ask on")
	}
	if !strings.Contains(out.String(), "rgt connect http://example.test") {
		t.Errorf("should print the command to run; got:\n%s", out.String())
	}
}

func TestParseSelection(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		n     int
		want  []int
		isErr bool
	}{
		{name: "single", in: "2", n: 3, want: []int{1}},
		{name: "comma separated", in: "1,3", n: 3, want: []int{0, 2}},
		{name: "space separated", in: "2 3", n: 3, want: []int{1, 2}},
		{name: "all keyword", in: "all", n: 3, want: []int{0, 1, 2}},
		{name: "a shorthand", in: "A", n: 2, want: []int{0, 1}},
		{name: "duplicates collapse", in: "2,2", n: 3, want: []int{1}},
		{name: "empty backs out", in: "  ", n: 3, want: nil},
		{name: "zero rejected", in: "0", n: 3, isErr: true},
		{name: "past end rejected", in: "4", n: 3, isErr: true},
		{name: "not a number", in: "x", n: 3, isErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSelection(tc.in, tc.n)
			if tc.isErr {
				if err == nil {
					t.Fatalf("parseSelection(%q) = %v, want an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSelection(%q): %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseSelection(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
