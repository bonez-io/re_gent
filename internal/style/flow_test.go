package style

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestFlowFallsBackToStablePlainText(t *testing.T) {
	var out bytes.Buffer
	flow := NewFlow(&out)
	flow.Header("connect", "girlfriend-assistant")
	if err := flow.Run("Installing project integration", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	flow.Detail("Server", "http://127.0.0.1:7654")
	flow.Complete("Ready to capture")
	flow.Next("Restart your agent")

	got := out.String()
	for _, want := range []string{
		"re_gent / connect",
		"girlfriend-assistant",
		"✓ Installing project integration",
		"Server    http://127.0.0.1:7654",
		"✓ Ready to capture",
		"Next Restart your agent",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plain flow missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("plain flow leaked terminal control sequences:\n%q", got)
	}
}

func TestFlowReportsTaskFailure(t *testing.T) {
	var out bytes.Buffer
	want := errors.New("server unavailable")
	err := NewFlow(&out).Run("Connecting to server", func() error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("Run error = %v, want %v", err, want)
	}
	if !strings.Contains(out.String(), "✗ Connecting to server") {
		t.Fatalf("failed task was not rendered:\n%s", out.String())
	}
}
