package cli

import (
	"os"

	"github.com/bonez-io/re_gent/internal/remote"
	"github.com/spf13/cobra"
)

// PushCmd returns the `rgt push` command.
//
// After the server cutover (RE-14) the server is the source of truth and the
// transport lives in internal/remote (spool-backed HTTP client). `rgt push`
// delivers local session history to the configured server, reusing the same
// push path as `rgt sync`.
func PushCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "push [ref]",
		Short: "Push local session history to the configured re_gent server",
		Long: "Uploads local session steps and refs to the re_gent server configured\n" +
			"for this repo (see 'rgt connect') and advances the server's refs.\n\n" +
			"Work is spooled and retried when the server is unreachable, so an\n" +
			"interrupted push is always safe to re-run.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
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
