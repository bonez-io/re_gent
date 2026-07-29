package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/regent-vcs/regent/internal/config"
	"github.com/regent-vcs/regent/internal/remote"
	"github.com/spf13/cobra"
)

// PushCmd returns the `rgt push` command.
//
// After the server cutover (RE-14) the server is the source of truth and the
// transport lives in internal/remote (spool-backed HTTP client). `rgt push`
// delivers local session history to the configured server, reusing the same
// push path as `rgt sync`.
//
// Push is auth-gated: an unauthenticated push fails closed with a clear
// client-side error before any network work, so a live agent turn is never
// surprised by a server-side 401.
func PushCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "push [ref]",
		Short: "Push local session history to the configured re_gent server",
		Long: "Uploads local session steps and refs to the re_gent server configured\n" +
			"for this repo (see 'rgt connect') and advances the server's refs.\n\n" +
			"Work is spooled and retried when the server is unreachable, so an\n" +
			"interrupted push is always safe to re-run.\n\n" +
			"Authentication is required: run 'rgt login <server-url>' first.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Auth gate (RE-9): fail closed before any network operation.
			ucfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if err := requirePushAuth(ucfg); err != nil {
				return err
			}

			// Transport via the RE-14 server-mode push path (canonical after
			// the cutover). runSync with default options performs a push.
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			cfg, err := remote.LoadConfigForCWD(remote.OSEnv, cwd)
			if err != nil {
				return err
			}
			opts := syncOptions{}
			if len(args) == 1 {
				opts.ref = args[0]
			}
			return runSync(cmd.OutOrStdout(), cfg, opts)
		},
	}
	return cmd
}

// requirePushAuth enforces that the user is signed in before a push is
// attempted. It never panics, making it safe on hook-adjacent code paths.
// ErrNotSignedIn is surfaced with a hint; ErrTokenInvalid is passed through.
func requirePushAuth(cfg *config.UserConfig) error {
	if err := config.CheckAuth(cfg); err != nil {
		if errors.Is(err, config.ErrNotSignedIn) {
			return fmt.Errorf("%w\n\nAuthenticate first:\n  rgt login <server-url>", err)
		}
		return fmt.Errorf("auth check failed: %w", err)
	}
	return nil
}
