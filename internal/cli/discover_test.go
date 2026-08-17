package cli

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
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

// TestConnectingAnAlreadyConnectedProjectStaysConnected is the promise that
// `rgt disconnect` is the only way to disconnect.
//
// Connecting used to be a toggle. In the picker, marking a project expressed
// the state you wanted, so marking a connected one meant "disconnect it" — and
// that reading leaked into the shared apply step. A project could therefore
// lose its wiring and its hooks as a side effect of being *selected*, which is
// what made a full-screen list over someone's filesystem destructive (#28).
//
// `rgt connect` is an imperative. Running it on a project that is already
// connected must leave it connected.
func TestConnectingAnAlreadyConnectedProjectStaysConnected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := newTestServer(t, http.StatusCreated, "twice-repo")
	project := mkProject(t, t.TempDir(), "twice")
	hooks := filepath.Join(project, ".claude", "settings.json")

	var out bytes.Buffer
	if err := connectHere(srv.URL, project, "", &out, false); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	if !isConnected(project) {
		t.Fatalf("first connect did not wire the project; output:\n%s", out.String())
	}
	if _, err := os.Stat(hooks); err != nil {
		t.Fatalf("first connect wrote no hooks: %v", err)
	}

	out.Reset()
	if err := connectHere(srv.URL, project, "", &out, false); err != nil {
		t.Fatalf("second connect: %v", err)
	}

	if !isConnected(project) {
		t.Errorf("connecting an already-connected project disconnected it; output:\n%s", out.String())
	}
	if _, err := os.Stat(hooks); err != nil {
		t.Errorf("connecting an already-connected project removed its hooks: %v", err)
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
