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
	"syscall"
	"time"

	"github.com/regent-vcs/regent/internal/server"
)

func main() {
	addr := flag.String("addr", "0.0.0.0:7654", "address to listen on")
	data := flag.String("data", "/data", "directory holding served repositories")
	max := flag.Int64("max-object-size", server.DefaultMaxObjectBytes, "maximum accepted object size in bytes")
	binaries := flag.String("binaries-dir", "", "directory of prebuilt rgt binaries served by /install")
	skillsDir := flag.String("skills-dir", "", "directory of published skills (<name>/SKILL.md) served by /api/skills")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "regent-server takes no arguments")
		os.Exit(2)
	}
	if err := serve(*addr, *data, *max, *binaries, *skillsDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func serve(addr, data string, max int64, binaries, skillsDir string) error {
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
	srv, err := server.New(data, server.WithMaxObjectBytes(max), server.WithBinariesDir(binaries), server.WithSkillsDir(skillsDir))
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	h := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	fmt.Printf("re_gent server listening on http://%s (data: %s)\n", ln.Addr(), data)
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
