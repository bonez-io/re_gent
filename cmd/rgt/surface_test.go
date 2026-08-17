package main

import (
	"bytes"
	"strings"
	"testing"
)

// The command list is a promise about what the tool does. Right now it makes
// three promises it cannot keep: `login` and `whoami` imply the server
// authenticates someone when it authenticates nobody, and `setup` and `connect`
// are two commands for one job that disagree about what "connected" means.
//
// Removing them is only half the work. People have these in shell history and
// in scripts, and cobra's answer to a name it no longer knows is "unknown
// command", which tells you nothing about what to run instead. A removed
// command has to name its replacement.

// runRoot executes the real command tree with the given args and returns
// everything the user would see, plus the error that sets the exit code.
func runRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	if err != nil {
		out.WriteString(err.Error())
	}
	return out.String(), err
}

func TestRemovedCommandsNameTheirReplacement(t *testing.T) {
	cases := []struct {
		command string
		expect  string
		why     string
	}{
		{"setup", "connect", "setup was folded into connect; connect does everything it did"},
		{"login", "no authentication", "the server authenticates nobody, so there is nothing to sign in to"},
		{"whoami", "git", "identity comes from git config, not from a sign-in"},
	}

	for _, tc := range cases {
		out, err := runRoot(t, tc.command)

		// A script that had this command must see it fail, not silently succeed.
		if err == nil {
			t.Errorf("`rgt %s` exited 0; a script cannot tell the command is gone (%s)", tc.command, tc.why)
		}

		// Cobra's default is the thing we are specifically refusing to ship.
		if strings.Contains(strings.ToLower(out), "unknown command") {
			t.Errorf("`rgt %s` reported \"unknown command\" instead of naming what to run:\n%s", tc.command, out)
		}

		if !strings.Contains(strings.ToLower(out), tc.expect) {
			t.Errorf("`rgt %s` never mentions %q — %s\ngot:\n%s", tc.command, tc.expect, tc.why, out)
		}
	}
}

// The removed names must also be gone from help. Leaving them listed would
// advertise the capability the removal exists to stop advertising.
func TestRemovedCommandsAreNotAdvertisedInHelp(t *testing.T) {
	out, err := runRoot(t, "--help")
	if err != nil {
		t.Fatalf("rgt --help: %v", err)
	}

	for _, gone := range []string{"login", "whoami", "setup"} {
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), gone+" ") {
				t.Errorf("help still lists %q:\n  %s", gone, strings.TrimSpace(line))
			}
		}
	}
}

// cat dumps a raw object by hash. It is a debugging tool, not part of the
// advertised surface — but hiding a command is not the same as deleting it, and
// anyone who knows the name must still be able to run it.
func TestObjectDumpIsHiddenFromHelpButStillRunnable(t *testing.T) {
	out, err := runRoot(t, "--help")
	if err != nil {
		t.Fatalf("rgt --help: %v", err)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "cat ") {
			t.Errorf("cat is still advertised in help:\n  %s", strings.TrimSpace(line))
		}
	}

	// Still reachable: invoked with no hash it must complain about the missing
	// argument, which is proof the command ran rather than being unrecognised.
	catOut, catErr := runRoot(t, "cat")
	if catErr == nil {
		t.Error("`rgt cat` with no argument exited 0; expected an argument error")
	}
	if strings.Contains(strings.ToLower(catOut), "unknown command") {
		t.Errorf("cat was hidden by deleting it; it must stay runnable:\n%s", catOut)
	}
}

// `rgt repair blame` is the answer to blame maps written by the old, broken
// line diff. It is only an answer if it is reachable from the command tree —
// and if it says which derived data a repair can and cannot reach, because the
// same fix arrived two different ways: `rgt show` diffs at query time and was
// corrected by the rebuild, while blame is stored and had to be rewritten.
func TestRepairBlameIsReachableAndNamesWhatItCannotReach(t *testing.T) {
	out, err := runRoot(t, "repair", "--help")
	if err != nil {
		t.Fatalf("`rgt repair --help`: %v\n%s", err, out)
	}
	for _, want := range []string{"blame", "rgt show", "query time"} {
		if !strings.Contains(out, want) {
			t.Errorf("`rgt repair --help` never mentions %q:\n%s", want, out)
		}
	}

	out, err = runRoot(t, "repair", "blame", "--help")
	if err != nil {
		t.Fatalf("`rgt repair blame --help`: %v\n%s", err, out)
	}
	if !strings.Contains(out, "session ref") {
		t.Errorf("`rgt repair blame --help` does not say what it walks:\n%s", out)
	}
}
