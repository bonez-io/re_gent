package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type fakeBootstrapper struct {
	url            string
	healthy        bool
	waitHealthy    bool
	docker         bool
	runErr         error
	runs, probes   int
	waitHealthRuns int
}

func (f *fakeBootstrapper) PublicURL(_ string, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	return f.url, nil
}
func (f *fakeBootstrapper) Healthy(_ string) bool { f.probes++; return f.healthy }
func (f *fakeBootstrapper) WaitHealthy(_ string) bool {
	f.waitHealthRuns++
	return f.waitHealthy
}
func (f *fakeBootstrapper) HasDocker(_ string) (bool, error) { return f.docker, nil }
func (f *fakeBootstrapper) Run(_ string) error               { f.runs++; return f.runErr }

func TestPrepareMachineHealthySkipsRemoteChanges(t *testing.T) {
	f := &fakeBootstrapper{url: "http://team.example:7654", healthy: true}
	var out bytes.Buffer
	got, err := prepareMachine("team", "", false, strings.NewReader(""), &out, f)
	if err != nil || got != f.url {
		t.Fatalf("prepareMachine = %q, %v", got, err)
	}
	if f.runs != 0 {
		t.Fatal("healthy target must not be provisioned")
	}
	if !strings.Contains(out.String(), "skipping provisioning") {
		t.Errorf("missing convergence output: %s", out.String())
	}
}

func TestPrepareMachineDeclinedChangesNothing(t *testing.T) {
	f := &fakeBootstrapper{url: "http://team.example:7654"}
	_, err := prepareMachine("team", "", false, strings.NewReader("n\n"), &bytes.Buffer{}, f)
	if err == nil || f.runs != 0 {
		t.Fatalf("declined = %v, runs=%d; want no remote mutation", err, f.runs)
	}
}

func TestPrepareMachineBootstrapFailureNamesFailure(t *testing.T) {
	f := &fakeBootstrapper{url: "http://team.example:7654", runErr: errors.New("docker compose failed")}
	_, err := prepareMachine("team", "", true, strings.NewReader(""), &bytes.Buffer{}, f)
	if err == nil || !strings.Contains(err.Error(), "docker compose failed") {
		t.Fatalf("error = %v", err)
	}
	if f.runs != 1 {
		t.Fatalf("runs = %d, want 1", f.runs)
	}
}

func TestPrepareMachineRequiresPublicHealthAfterBootstrap(t *testing.T) {
	f := &fakeBootstrapper{url: "http://team.example:7654"}
	_, err := prepareMachine("team", "https://public.example", true, strings.NewReader(""), &bytes.Buffer{}, f)
	if err == nil || !strings.Contains(err.Error(), "project was not changed") {
		t.Fatalf("error = %v", err)
	}
	if f.runs != 1 {
		t.Fatalf("runs = %d, want 1", f.runs)
	}
}

func TestPrepareMachineWaitsForPublicHealthAfterBootstrap(t *testing.T) {
	f := &fakeBootstrapper{url: "http://team.example:7654", waitHealthy: true}
	got, err := prepareMachine("team", "", true, strings.NewReader(""), &bytes.Buffer{}, f)
	if err != nil || got != f.url {
		t.Fatalf("prepareMachine = %q, %v", got, err)
	}
	if f.runs != 1 || f.waitHealthRuns != 1 {
		t.Fatalf("bootstrap runs = %d, health waits = %d; want one each", f.runs, f.waitHealthRuns)
	}
}

func TestNormalizeServiceURLDistinguishesSSHIntent(t *testing.T) {
	if isServiceURL("root@host") {
		t.Fatal("SSH target was treated as a URL")
	}
	if !isServiceURL("https://regent.example") {
		t.Fatal("https URL was not a service")
	}
}
