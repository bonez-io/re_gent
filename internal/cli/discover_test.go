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
	// connect now remembers the server on success, the way setup did. Without
	// this the test would write a throwaway URL into the developer's real
	// config.
	t.Setenv("HOME", t.TempDir())

	srv := newTestServer(t, http.StatusCreated, "picked-repo")
	root := t.TempDir()
	mkProject(t, root, "alpha")
	beta := mkProject(t, root, "beta")

	var out bytes.Buffer
	// The chooser stands in for the picker, which needs a terminal. What is
	// under test is what happens to the projects it returns, not how a person
	// drives the UI.
	chooseBeta := func(string) ([]string, error) { return []string{beta}, nil }
	if err := connectPicked(srv.URL, root, &out, true, chooseBeta, nil); err != nil {
		t.Fatalf("connectPicked: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(beta, ".regent", "config.toml")); statErr != nil {
		t.Errorf("chosen project should be wired: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, "alpha", ".regent")); statErr == nil {
		t.Error("unchosen project must not be wired")
	}
	if !strings.Contains(out.String(), "beta") {
		t.Errorf("the connected project should be named in the output; got:\n%s", out.String())
	}
}

// The `curl | sh` case: with no terminal we must not guess, only report what to
// run — and we must not call that success.
//
// This test previously asserted the opposite. It required a nil error, which
// pinned the behaviour that connect prints a list of suggestions and exits 0
// having connected nothing. That is the same defect the summary bug was in
// init: a command that did nothing, reporting as though it had. An installer
// that runs `rgt connect <url>` from a directory of repositories reads the zero
// exit as "connected" and moves on, and capture silently never happens.
//
// Printing the suggestions is right and stays. Claiming success for it does not.
func TestConnectWithoutTerminalReportsAndFails(t *testing.T) {
	root := t.TempDir()
	alpha := mkProject(t, root, "alpha")

	var out bytes.Buffer
	err := connectPicked("http://example.test", root, &out, false, nil, nil)
	if err == nil {
		t.Error("connect exited 0 without connecting anything; a script cannot tell that from success")
	}

	// The guidance is the point of this path, so it has to survive the exit code.
	if !strings.Contains(out.String(), "rgt connect http://example.test") {
		t.Errorf("should print the command to run; got:\n%s", out.String())
	}
	if _, statErr := os.Stat(filepath.Join(alpha, ".regent")); statErr == nil {
		t.Error("nothing may be wired without a terminal to ask on")
	}
}

// Backing out of the picker is not an error. Reporting success for it is.
//
// setup and connect both offer a list of projects and both let you choose
// none, and they disagree about what that means: setup returns "nothing
// selected — no projects were connected", connect prints "Nothing selected."
// and exits 0. Same situation, two answers, and the one a script sees from
// connect is indistinguishable from having wired everything asked for.
//
// This is the disagreement that having two pickers produces. Unifying them on
// one picker is what makes a single answer possible; this test says which
// answer it has to be.
func TestConnectSelectingNothingIsNotSuccess(t *testing.T) {
	root := t.TempDir()
	alpha := mkProject(t, root, "alpha")

	var out bytes.Buffer
	// The picker was shown and closed without a selection.
	chooseNothing := func(string) ([]string, error) { return nil, nil }
	err := connectPicked("http://example.test", root, &out, true, chooseNothing, nil)
	if err == nil {
		t.Error("connect exited 0 after connecting nothing; setup fails for the same choice")
	}

	if _, statErr := os.Stat(filepath.Join(alpha, ".regent")); statErr == nil {
		t.Error("nothing was selected, so nothing may be wired")
	}
}

// Standing inside a project, connect wires that project and nothing else.
//
// Retargeted from setupTargets, which is gone: setup and connect used to make
// this decision in two different places, and this is the promise that survived
// the merge. Scanning and wiring everything found below would let one pasted
// command reach into unrelated repositories nobody chose.
func TestConnectInsideAProjectWiresOnlyThatProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := newTestServer(t, http.StatusCreated, "here-repo")
	root := t.TempDir()
	here := mkProject(t, root, "here")
	sibling := mkProject(t, root, "sibling")

	var out bytes.Buffer
	// canPrompt=false: no terminal, so no share question — the path an
	// installer, a devcontainer or CI actually takes.
	if err := connectHere(srv.URL, here, "", &out, false); err != nil {
		t.Fatalf("connectHere: %v", err)
	}

	if !isConnected(here) {
		t.Errorf("connect ran inside %s but did not wire it", here)
	}
	if isConnected(sibling) {
		t.Errorf("connect wired %s, which the user never named", sibling)
	}
}
