package cli

import (
	"os/exec"
	"strings"
	"testing"
)

func TestSetupCommandHidesChildDiagnosticsByDefault(t *testing.T) {
	SetVerbose(false)
	t.Setenv("REGENT_VERBOSE", "")
	err := runSetupCommand(exec.Command("sh", "-c", "echo npm-internal-noise >&2; exit 1"), "install integration")
	if err == nil {
		t.Fatal("failed setup command returned nil")
	}
	if strings.Contains(err.Error(), "npm-internal-noise") {
		t.Fatalf("default error leaked child output: %v", err)
	}
	if !strings.Contains(err.Error(), "--verbose") {
		t.Fatalf("quiet error did not name diagnostic mode: %v", err)
	}
}

func TestVerboseCanBeEnabledByEnvironment(t *testing.T) {
	SetVerbose(false)
	t.Setenv("REGENT_VERBOSE", "1")
	if !Verbose() {
		t.Fatal("REGENT_VERBOSE=1 did not enable diagnostics")
	}
}
