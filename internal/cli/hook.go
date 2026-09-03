package cli

import (
	"os"

	"github.com/bonez-io/re_gent/internal/hook"
	"github.com/spf13/cobra"
)

// HookCmd creates the legacy Claude Code PostToolUse hook command.
func HookCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "hook",
		Short:  "Process a legacy PostToolUse hook (internal)",
		Long:   "Internal legacy command invoked by older Claude Code hook setups.",
		Hidden: true, // Don't show in help (internal use only)
		RunE: func(cmd *cobra.Command, args []string) error {
			return hook.Run(os.Stdin, os.Stdout)
		},
	}
}
