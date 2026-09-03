package cli

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/bonez-io/re_gent/internal/config"
	"github.com/bonez-io/re_gent/internal/remote"
	"github.com/spf13/cobra"
)

// AdminCmd returns the `rgt admin` command group: operator actions against a
// self-hosted server's own data, distinct from the per-project commands
// above it (RFC 0005, "Storage": "the beta does ship for durability: rgt
// admin backup").
func AdminCmd() *cobra.Command {
	admin := &cobra.Command{
		Use:   "admin",
		Short: "Operator actions against a self-hosted re_gent server",
		Args:  cobra.NoArgs,
	}
	admin.AddCommand(adminBackupCmd())
	return admin
}

func adminBackupCmd() *cobra.Command {
	var out string
	var force bool
	cmd := &cobra.Command{
		Use:   "backup [server-url]",
		Short: "Download a consistent backup of a self-hosted server's databases",
		Long: `Downloads a tar of identity.db and projects.db, taken with SQLite's online
backup API, so a copy of the data volume is never the only way to recover a
self-hosted instance (RFC 0005, "Storage").

Requires an admin credential already stored by "rgt auth login".`,
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			explicit := ""
			if len(args) == 1 {
				explicit = args[0]
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			serverURL, err := authServerURL(explicit, cfg)
			if err != nil {
				return err
			}
			token := config.TokenForServer(cfg, serverURL)
			if token == "" {
				return ErrNotSignedIn
			}

			outPath := out
			if outPath == "" {
				outPath = fmt.Sprintf("regent-backup-%s.tar", time.Now().UTC().Format("2006-01-02"))
			}
			if !force {
				if _, statErr := os.Stat(outPath); statErr == nil {
					return fmt.Errorf("%s already exists; pass --force to overwrite, or --out to choose a different file", outPath)
				} else if !os.IsNotExist(statErr) {
					return fmt.Errorf("check %s: %w", outPath, statErr)
				}
			}

			client := &http.Client{Timeout: 5 * time.Minute}
			size, err := remote.DownloadBackup(cmd.Context(), client, serverURL, token, outPath)
			if err != nil {
				return fmt.Errorf("download backup: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s (%d bytes) from %s.\n", outPath, size, serverURL)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "output file (default regent-backup-<date>.tar)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing file")
	return cmd
}
