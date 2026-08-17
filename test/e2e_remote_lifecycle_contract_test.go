package test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	// This package executes a freshly built rgt binary. Keep the compile-time
	// edge so the Go test cache cannot reuse a binary from before a CLI change.
	_ "github.com/regent-vcs/regent/internal/cli"
)

// RFC 0001 makes the repository binding portable by excluding every machine
// credential. This is true for the legacy repo_id shape as well as the target
// versioned project_id shape, so it can be enforced before the ID migration.
func TestE2ERemoteLifecycleBindingIsPortableAndSecretFree(t *testing.T) {
	rgt := buildTestBinary(t)
	srv := startTestServer(t)
	project := gitProject(t, "portable-binding", "https://github.com/acme/portable-binding.git")

	const secret = "must-never-enter-the-repository"
	env := append(hermeticEnv(t, srv), "REGENT_TOKEN="+secret)
	e2eRunEnv(t, rgt, project, env, nil, "connect", srv.URL)

	path := filepath.Join(project, ".regent", "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read portable binding: %v", err)
	}
	binding := string(data)
	if !strings.Contains(binding, srv.URL) {
		t.Errorf("binding does not identify its server:\n%s", binding)
	}
	if repoIDOf(t, project) == "" {
		t.Errorf("binding has no project identity:\n%s", binding)
	}
	for _, forbidden := range []string{secret, "token", "REGENT_TOKEN", "cache_dir", "timeout"} {
		if strings.Contains(binding, forbidden) {
			t.Errorf("portable binding contains machine-local value %q:\n%s", forbidden, binding)
		}
	}
}

// Registration is the first remote mutation, but it is not permission to
// switch capture modes. If registration fails, connect must not persist a
// binding that sends every later hook to a project the server never created.
func TestE2ERemoteLifecycleRegistrationFailureLeavesNoBinding(t *testing.T) {
	rgt := buildTestBinary(t)
	project := gitProject(t, "failed-registration", "https://github.com/acme/failed-registration.git")

	var registrations int
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/repos" {
			registrations++
		}
		http.Error(w, "registration unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(broken.Close)

	env := []string{"HOME=" + t.TempDir(), "REGENT_SERVER_URL=" + broken.URL}
	out, err := e2eRunEnvRaw(t, rgt, project, env, "connect", broken.URL)
	if err == nil {
		t.Fatalf("connect succeeded although registration failed:\n%s", out)
	}
	if registrations == 0 {
		t.Fatalf("connect failed without attempting registration:\n%s", out)
	}

	path := filepath.Join(project, ".regent", "config.toml")
	data, readErr := os.ReadFile(path)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read config after failed registration: %v", readErr)
	}
	binding := string(data)
	if strings.Contains(binding, broken.URL) || repoIDOf(t, project) != "" {
		t.Errorf("failed registration committed a remote binding:\n%s", binding)
	}
}
