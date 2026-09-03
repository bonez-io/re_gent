package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/server"
	"github.com/bonez-io/re_gent/internal/store"
)

func runInsight(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := InsightCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func insightTestRepo(t *testing.T) *store.Store {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	withWorkingDir(t, root)
	s, err := store.Init(root)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}
	return s
}

func TestInsightEnable_WritesConfigIndexesAndReportsMissingProvider(t *testing.T) {
	s := insightTestRepo(t)
	if err := s.WriteRepoConfig(store.RepoConfig{Capture: store.CaptureConfig{Root: "project"}}); err != nil {
		t.Fatal(err)
	}
	idx, err := index.Open(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.AppendMessage(index.Message{ID: "m1", SessionID: "s1", Timestamp: 1, MessageType: "user", ContentText: "hello search"}); err != nil {
		t.Fatal(err)
	}
	_ = idx.Close()

	out, err := runInsight(t, "enable")
	if err != nil {
		t.Fatalf("enable: %v\n%s", err, out)
	}
	cfg, err := s.ReadRepoConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Insight.Enabled || cfg.Capture.Root != "project" {
		t.Fatalf("config after enable: %#v", cfg)
	}
	for _, want := range []string{"Insight enabled", "No model provider", "[insight.model]", "1 of 1 messages"} {
		if !strings.Contains(out, want) {
			t.Errorf("enable output missing %q:\n%s", want, out)
		}
	}

	// The committed file carries only policy, never a provider endpoint.
	raw, err := os.ReadFile(filepath.Join(s.Root, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "[insight]") || strings.Contains(string(raw), "api_key") {
		t.Fatalf("committed config:\n%s", raw)
	}

	out, err = runInsight(t, "disable")
	if err != nil || !strings.Contains(out, "Insight disabled") {
		t.Fatalf("disable: %v\n%s", err, out)
	}
	cfg, _ = s.ReadRepoConfig()
	if cfg.Insight.Enabled {
		t.Fatal("disable should clear enabled")
	}
	out, _ = runInsight(t, "disable")
	if !strings.Contains(out, "already off") {
		t.Fatalf("second disable:\n%s", out)
	}
}

func TestInsightServerMode_TalksToTheServer(t *testing.T) {
	s := insightTestRepo(t)
	dataDir := t.TempDir()
	srv, err := server.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	body, _ := json.Marshal(map[string]string{"repo_id": "proj"})
	resp, err := http.Post(ts.URL+"/repos", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if err := s.WriteRepoConfig(store.RepoConfig{Remote: store.RemoteConfig{URL: ts.URL, RepoID: "proj"}}); err != nil {
		t.Fatal(err)
	}

	out, err := runInsight(t, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Insight: off") || !strings.Contains(out, "project") {
		t.Fatalf("server status:\n%s", out)
	}
	out, err = runInsight(t, "enable")
	if err != nil {
		t.Fatalf("enable: %v\n%s", err, out)
	}
	if !strings.Contains(out, "enabled on the server") || !strings.Contains(out, "insight.toml") {
		t.Fatalf("enable output:\n%s", out)
	}
	cfg, _ := s.ReadRepoConfig()
	if cfg.Insight.Enabled {
		t.Fatal("server mode must not write the local switch")
	}
	out, _ = runInsight(t, "status")
	if !strings.Contains(out, "enabled=true") {
		t.Fatalf("status after enable:\n%s", out)
	}
	out, err = runCmd(t, WorkCmd(), "list")
	if err != nil || !strings.Contains(out, "No work items yet") {
		t.Fatalf("work list: %v\n%s", err, out)
	}
	out, err = runCmd(t, SearchCmd(), "anything")
	if err != nil || !strings.Contains(out, "Nothing matched") {
		t.Fatalf("search: %v\n%s", err, out)
	}
	out, err = runInsight(t, "disable")
	if err != nil || !strings.Contains(out, "disabled on the server") {
		t.Fatalf("disable: %v\n%s", err, out)
	}
}

func TestInsightStatus_OffByDefault(t *testing.T) {
	insightTestRepo(t)
	out, err := runInsight(t, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	for _, want := range []string{"Insight: off", "queue        empty", "0 work items"} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}
}

func TestInsightRebuild_QueuesEverySession(t *testing.T) {
	s := insightTestRepo(t)
	idx, err := index.Open(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if err := idx.UpsertSession(index.SessionUpdate{ID: id, Origin: "claude_code"}); err != nil {
			t.Fatal(err)
		}
	}
	_ = idx.Close()

	out, err := runInsight(t, "rebuild")
	if err != nil {
		t.Fatalf("rebuild: %v\n%s", err, out)
	}
	if !strings.Contains(out, "3 sessions queued") || !strings.Contains(out, "not active") {
		t.Fatalf("rebuild output:\n%s", out)
	}
	out, _ = runInsight(t, "status")
	if !strings.Contains(out, "3 queued") {
		t.Fatalf("status after rebuild:\n%s", out)
	}
}

func TestInsightRun_InactiveIsAnError(t *testing.T) {
	insightTestRepo(t)
	_, err := runInsight(t, "run")
	if err == nil || !strings.Contains(err.Error(), "not active") {
		t.Fatalf("expected inactive error, got %v", err)
	}
}
