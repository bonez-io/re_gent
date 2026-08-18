package cli

// The machine half of `rgt connect`.  It deliberately uses the user's ssh
// program rather than an SSH library: keys, agents, ProxyJump and aliases stay
// exactly where they already work.

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

type bootstrapper interface {
	PublicURL(target, override string) (string, error)
	Healthy(publicURL string) bool
	WaitHealthy(publicURL string) bool
	HasDocker(target string) (bool, error)
	Run(target string) error
}

type systemBootstrapper struct{}

func (systemBootstrapper) PublicURL(target, override string) (string, error) {
	if override != "" {
		return normalizeServiceURL(override)
	}
	out, err := exec.Command("ssh", "-G", target).Output()
	if err != nil {
		return "", fmt.Errorf("resolve SSH target %q: %w", target, err)
	}
	host := ""
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "hostname" {
			host = fields[1]
			break
		}
	}
	if host == "" {
		return "", fmt.Errorf("ssh -G %q did not report a hostname", target)
	}
	return "http://" + host + ":7654", nil
}

func (systemBootstrapper) Healthy(publicURL string) bool {
	c := &http.Client{Timeout: 5 * time.Second}
	r, err := c.Get(strings.TrimRight(publicURL, "/") + "/healthz")
	if err != nil {
		return false
	}
	defer r.Body.Close()
	return r.StatusCode == http.StatusOK
}

func (b systemBootstrapper) WaitHealthy(publicURL string) bool {
	deadline := time.Now().Add(30 * time.Second)
	for {
		if b.Healthy(publicURL) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Second)
	}
}

func (systemBootstrapper) HasDocker(target string) (bool, error) {
	err := exec.Command("ssh", target, "command -v docker >/dev/null 2>&1").Run()
	if err == nil {
		return true, nil
	}
	// Exit status 1 is the documented probe answer; other ssh failures retain
	// their own diagnostics so users debug the same connection they use daily.
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("probe Docker over ssh: %w", err)
}

func (systemBootstrapper) Run(target string) error {
	cmd := exec.Command("ssh", target, "sh -s")
	cmd.Stdin = strings.NewReader(remoteBootstrapScript)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("bootstrap server: %w", err)
	}
	return nil
}

// The script is intentionally convergent.  A failed run leaves its state in
// place; the next run probes first and can safely finish the remaining steps.
const remoteBootstrapScript = `set -eu
if ! command -v docker >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update && apt-get install -y docker.io docker-compose-plugin
  else
    echo "Docker is not installed and this host is not apt-based" >&2; exit 1
  fi
fi
mkdir -p /opt/regent
cat >/opt/regent/compose.yml <<'EOF'
services:
  server:
    image: ghcr.io/regent-vcs/regent-server:latest
    ports: ["7654:7654"]
    volumes: ["regent-data:/data"]
    restart: unless-stopped
volumes:
  regent-data:
EOF
docker compose -f /opt/regent/compose.yml up -d
`

func normalizeServiceURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("%q is not an http(s) server URL", raw)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func isServiceURL(raw string) bool { _, err := normalizeServiceURL(raw); return err == nil }

func confirmBootstrap(in io.Reader, out io.Writer, yes bool) (bool, error) {
	if yes {
		return true, nil
	}
	fmt.Fprint(out, "Continue? [y/N]: ")
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && len(line) == 0 {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes"), nil
}

// prepareMachine proves the public endpoint before returning.  Callers must do
// this before creating .regent or writing hooks/configuration.
func prepareMachine(target, override string, yes bool, in io.Reader, out io.Writer, b bootstrapper) (string, error) {
	publicURL, err := b.PublicURL(target, override)
	if err != nil {
		return "", err
	}
	if b.Healthy(publicURL) {
		fmt.Fprintf(out, "  - Server already healthy at %s; skipping provisioning.\n", publicURL)
		return publicURL, nil
	}
	hasDocker, err := b.HasDocker(target)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(out, "Server at %s is not healthy. Planned remote changes on %s:\n", publicURL, target)
	if !hasDocker {
		fmt.Fprintln(out, "  - install Docker (apt-based Linux host)")
	} else {
		fmt.Fprintln(out, "  - Docker already installed")
	}
	fmt.Fprintln(out, "  - write /opt/regent/compose.yml and start regent-server")
	ok, err := confirmBootstrap(in, out, yes)
	if err != nil {
		return "", fmt.Errorf("confirm provisioning: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("provisioning declined; install Docker yourself then re-run with --yes when ready")
	}
	if err := b.Run(target); err != nil {
		return "", err
	}
	if !b.WaitHealthy(publicURL) {
		return "", fmt.Errorf("server was started but %s/healthz is unreachable from this machine (check port 7654/firewall); project was not changed", publicURL)
	}
	return publicURL, nil
}
