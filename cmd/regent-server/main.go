// regent-server is the production HTTP service.  It intentionally has no
// developer commands: clients install and run rgt; operators run this binary.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bonez-io/re_gent/selfhosted"
	"github.com/bonez-io/re_gent/server"
)

func main() {
	addr := flag.String("addr", "0.0.0.0:7654", "address to listen on")
	data := flag.String("data", "/data", "directory holding served repositories")
	max := flag.Int64("max-object-size", server.DefaultMaxObjectBytes, "maximum accepted object size in bytes")
	binaries := flag.String("binaries-dir", "", "directory of prebuilt rgt binaries served by /install")
	skillsDir := flag.String("skills-dir", "", "directory of published skills (<name>/SKILL.md) served by /api/skills")
	authMode := flag.String("auth-mode", "auto", "authentication mode: auto, self-hosted, or open")
	insecureNoAuth := flag.Bool("insecure-no-auth", false, "allow an unauthenticated non-loopback listener (development or an approved access proxy only)")
	recoverOwnerToken := flag.Bool("recover-owner-token", false, "issue a 24-hour instance-owner recovery token and exit (stop the server first)")
	adminUsername := flag.String("admin-username", "", "username for the initial self-hosted admin (default: admin)")
	adminPassword := flag.String("admin-password", "", "initial self-hosted admin password (default: REGENT_ADMIN_PASSWORD, or a random 20-character password printed on first start)")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "regent-server takes no arguments")
		os.Exit(2)
	}
	if *recoverOwnerToken {
		if err := recoverOwner(*data); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := serve(*addr, *data, *max, *binaries, *skillsDir, *authMode, *adminUsername, *adminPassword, *insecureNoAuth); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func recoverOwner(data string) error {
	if !filepath.IsAbs(data) {
		var err error
		data, err = filepath.Abs(data)
		if err != nil {
			return err
		}
	}
	_, secret, err := selfhosted.RecoverOwnerToken(data)
	if err != nil {
		return fmt.Errorf("recover instance owner: %w", err)
	}
	fmt.Fprintln(os.Stdout, "one-time 24-hour owner recovery token (shown once):")
	fmt.Fprintln(os.Stdout, secret)
	fmt.Fprintln(os.Stdout, "sign in with `rgt auth login`, then create a replacement token and revoke this one")
	return nil
}

func serve(addr, data string, max int64, binaries, skillsDir, authMode, adminUsername, adminPassword string, insecureNoAuth bool) error {
	if !filepath.IsAbs(data) {
		var err error
		data, err = filepath.Abs(data)
		if err != nil {
			return err
		}
	}
	if binaries == "" {
		binaries = os.Getenv("REGENT_BINARIES_DIR")
	}
	if skillsDir == "" {
		skillsDir = os.Getenv("REGENT_SKILLS_DIR")
	}
	if adminPassword == "" {
		adminPassword = os.Getenv("REGENT_ADMIN_PASSWORD")
	}
	mode, err := resolveAuthMode(authMode, insecureNoAuth)
	if err != nil {
		return err
	}
	coreOptions := []server.Option{server.WithMaxObjectBytes(max), server.WithBinariesDir(binaries), server.WithSkillsDir(skillsDir)}
	var handler http.Handler
	var closeHandler func() error
	switch mode {
	case "self-hosted":
		secureServer, setup, err := selfhosted.New(data, adminUsername, adminPassword, coreOptions...)
		if err != nil {
			return err
		}
		handler = secureServer
		closeHandler = secureServer.Close
		// RFC 0005 step 0: print the three-line ready message to stdout only
		// on the run that generated the initial password (the plaintext is
		// never available again afterward — only its Argon2id hash is
		// persisted); a restart against an already-bootstrapped data
		// directory instead reports that the initial password, if still in
		// force, remains unchanged, without ever repeating the value.
		switch {
		case setup.Generated:
			// The address people open is the web origin in front of this
			// process (Compose publishes it on the host), not the bind
			// address inside a container, so Compose sets REGENT_PUBLIC_URL.
			publicURL := strings.TrimSpace(os.Getenv("REGENT_PUBLIC_URL"))
			if publicURL == "" {
				publicURL = "http://" + addr
			}
			fmt.Printf("re_gent is ready at %s\n", publicURL)
			fmt.Printf("Sign in as %s with the initial password: %s\n", setup.AdminUsername, setup.AdminPassword)
			fmt.Println("This password must be replaced on first sign-in.")
		case setup.PasswordChangeRequired:
			fmt.Fprintln(os.Stderr, "re_gent: the initial admin password is still in force")
		}
	case "open":
		if err := validateUnauthenticatedBind(addr, insecureNoAuth); err != nil {
			return err
		}
		openServer, err := server.New(data, coreOptions...)
		if err != nil {
			return err
		}
		handler = openServer
		fmt.Fprintln(os.Stderr, "WARNING: re_gent is running without application authentication")
	}
	if closeHandler != nil {
		defer func() { _ = closeHandler() }()
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	h := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	fmt.Printf("re_gent server listening on http://%s (data: %s, auth: %s)\n", ln.Addr(), data, mode)
	errCh := make(chan error, 1)
	go func() { errCh <- h.Serve(ln) }()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return h.Shutdown(shutdown)
	}
}

func resolveAuthMode(mode string, insecureNoAuth bool) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "", "auto":
		if insecureNoAuth {
			return "open", nil
		}
		return "self-hosted", nil
	case "self-hosted":
		if insecureNoAuth {
			return "", errors.New("--insecure-no-auth cannot be combined with --auth-mode self-hosted")
		}
		return mode, nil
	case "open":
		return mode, nil
	default:
		return "", fmt.Errorf("invalid auth mode %q: use auto, self-hosted, or open", mode)
	}
}

func validateUnauthenticatedBind(addr string, insecureNoAuth bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	if insecureNoAuth {
		return nil
	}
	return fmt.Errorf("refusing unauthenticated non-loopback listener %q; configure authentication or pass --insecure-no-auth only for local development or an approved access proxy", addr)
}
