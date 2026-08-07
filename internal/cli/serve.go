package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/regent-vcs/regent/internal/server"
	"github.com/regent-vcs/regent/internal/style"
	"github.com/spf13/cobra"
)

// DefaultServeAddr is the address rgt serve binds when none is given.
const DefaultServeAddr = "127.0.0.1:7654"

type serveParams struct {
	Addr          string
	DataDir       string
	MaxObjectSize int64
	BinariesDir   string
}

// ServeCmd creates the serve command: one server, many repos.
func ServeCmd() *cobra.Command {
	p := serveParams{Addr: DefaultServeAddr, MaxObjectSize: server.DefaultMaxObjectBytes}

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve re_gent repositories over HTTP",
		Long: "Runs an object/ref server that hosts any number of repositories.\n" +
			"Each repo is addressed by id (rgt push --repo <id>) and is stored\n" +
			"separately, so histories and refs of different repos never mix.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runServe(ctx, p)
		},
	}

	cmd.Flags().StringVar(&p.Addr, "addr", p.Addr, "address to listen on")
	cmd.Flags().StringVar(&p.DataDir, "data", "", "directory holding served repos (default ~/.regent-server)")
	cmd.Flags().Int64Var(&p.MaxObjectSize, "max-object-size", p.MaxObjectSize, "maximum accepted object size in bytes")
	cmd.Flags().StringVar(&p.BinariesDir, "binaries-dir", "",
		"directory of prebuilt per-OS rgt binaries (rgt_<os>_<arch>[.exe]) served by /install (also read from env REGENT_BINARIES_DIR); empty serves only the server's own binary")

	return cmd
}

// resolveDataDir returns the directory the server stores repos in.
func resolveDataDir(dir string) (string, error) {
	if dir != "" {
		return filepath.Abs(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".regent-server"), nil
}

// runServe starts the server and blocks until ctx is cancelled.
func runServe(ctx context.Context, p serveParams) error {
	dataDir, err := resolveDataDir(p.DataDir)
	if err != nil {
		return err
	}
	// Flag wins over env so a single process can be overridden without editing
	// shared state.
	binariesDir := p.BinariesDir
	if binariesDir == "" {
		binariesDir = os.Getenv("REGENT_BINARIES_DIR")
	}
	srv, err := server.New(dataDir,
		server.WithMaxObjectBytes(p.MaxObjectSize),
		server.WithBinariesDir(binariesDir))
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", p.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", p.Addr, err)
	}

	httpSrv := &http.Server{
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	repos, err := srv.ListRepos()
	if err != nil {
		return err
	}
	fmt.Printf("%s serving %d repo(s) from %s on http://%s\n",
		style.Brand("re_gent"), len(repos), dataDir, ln.Addr())

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(ln) }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
}
