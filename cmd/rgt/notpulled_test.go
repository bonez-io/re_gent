package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bonez-io/re_gent/internal/remote"
)

// TestReportServerModeCacheJSON pins the fix for the "connected, not yet
// pulled" fallback reporter: `rgt log --json` must never fall back to a
// prose sentence when the machine's cache is empty, because that breaks
// anything downstream expecting to decode one JSON document. Every branch of
// reportServerModeCache is exercised with asJSON=true and checked for valid,
// decodable JSON carrying a non-empty "note".
func TestReportServerModeCacheJSON(t *testing.T) {
	t.Run("connected, holds history, not yet pulled", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"refs":{"sessions/claude_code--demo":"` + strings.Repeat("a", 64) + `"}}`))
		}))
		defer srv.Close()

		cfg := remote.Config{ServerURL: srv.URL, RepoID: "demo", Timeout: 2 * time.Second}
		var buf bytes.Buffer
		reportServerModeCache(&buf, cfg, true)
		doc := decodeNotPulledJSON(t, buf.Bytes())
		if doc.Note == "" {
			t.Fatalf("empty note in JSON fallback: %s", buf.String())
		}
		if !strings.Contains(doc.Note, "not yet pulled") {
			t.Errorf("note = %q, want it to say not yet pulled", doc.Note)
		}
		if doc.Sessions == nil || doc.Steps == nil {
			t.Errorf("sessions/steps must be empty arrays, not null: %s", buf.String())
		}
	})

	t.Run("server does not know this project", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		}))
		defer srv.Close()

		cfg := remote.Config{ServerURL: srv.URL, RepoID: "demo", Timeout: 2 * time.Second}
		var buf bytes.Buffer
		reportServerModeCache(&buf, cfg, true)
		doc := decodeNotPulledJSON(t, buf.Bytes())
		if !strings.Contains(doc.Note, "does not know this project") {
			t.Errorf("note = %q, want it to name the unregistered project", doc.Note)
		}
	})

	t.Run("server knows project but holds no history", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"refs":{}}`))
		}))
		defer srv.Close()

		cfg := remote.Config{ServerURL: srv.URL, RepoID: "demo", Timeout: 2 * time.Second}
		var buf bytes.Buffer
		reportServerModeCache(&buf, cfg, true)
		doc := decodeNotPulledJSON(t, buf.Bytes())
		if !strings.Contains(doc.Note, "holds no history yet") {
			t.Errorf("note = %q, want it to say the server holds no history yet", doc.Note)
		}
	})

	t.Run("server unreachable", func(t *testing.T) {
		cfg := remote.Config{ServerURL: "http://127.0.0.1:1", RepoID: "demo", Timeout: 500 * time.Millisecond}
		var buf bytes.Buffer
		reportServerModeCache(&buf, cfg, true)
		doc := decodeNotPulledJSON(t, buf.Bytes())
		if !strings.Contains(doc.Note, "cannot reach") {
			t.Errorf("note = %q, want it to say the server cannot be reached", doc.Note)
		}
	})
}

// TestReportServerModeCacheText pins the non-JSON side: the wording a person
// reads on a terminal must be unchanged by the asJSON plumbing added to fix
// the --json case.
func TestReportServerModeCacheText(t *testing.T) {
	cfg := remote.Config{ServerURL: "http://127.0.0.1:1", RepoID: "demo", Timeout: 500 * time.Millisecond}
	var buf bytes.Buffer
	reportServerModeCache(&buf, cfg, false)
	out := buf.String()
	if !strings.Contains(out, "Cannot reach") {
		t.Errorf("plain-text report does not mention the server is unreachable:\n%s", out)
	}
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("plain-text report looks like JSON: %s", out)
	}
}

// TestJSONOutputRequestedDetectsTheLogJSONFlag exercises the one signal
// jsonOutputRequested has available: the raw argument list. It has to say
// yes for `rgt log --json` and no for a bare `rgt log`, since it is what
// stands between an empty server-mode cache and a plain-text sentence
// landing in a JSON stream.
func TestJSONOutputRequestedDetectsTheLogJSONFlag(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"rgt", "log"}, false},
		{[]string{"rgt", "log", "--json"}, true},
		{[]string{"rgt", "log", "--json=true"}, true},
		{[]string{"rgt", "sessions", "--format=json"}, false},
	}
	original := os.Args
	defer func() { os.Args = original }()
	for _, tc := range cases {
		os.Args = tc.args
		if got := jsonOutputRequested(); got != tc.want {
			t.Errorf("jsonOutputRequested() with args %v = %v, want %v", tc.args, got, tc.want)
		}
	}
}

// TestCommandNotPulledReporterEmitsJSONForLogJSON is the end-to-end path:
// the reporter installed via cli.SetNotPulledReporter, invoked the way `rgt
// log --json` invokes it, with the command line actually carrying --json.
func TestCommandNotPulledReporterEmitsJSONForLogJSON(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("REGENT_SERVER_URL", "http://127.0.0.1:1")
	t.Setenv("REGENT_REPO_ID", "demo")

	original := os.Args
	os.Args = []string{"rgt", "log", "--json"}
	defer func() { os.Args = original }()

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWD) }()

	var buf bytes.Buffer
	ok := commandNotPulledReporter(&buf)
	if !ok {
		t.Fatalf("commandNotPulledReporter returned false; want it to handle a configured server-mode project")
	}
	decodeNotPulledJSON(t, buf.Bytes())
}

func decodeNotPulledJSON(t *testing.T, data []byte) notPulledJSON {
	t.Helper()
	var doc notPulledJSON
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("fallback reporter did not emit valid JSON: %v\noutput: %s", err, data)
	}
	return doc
}
