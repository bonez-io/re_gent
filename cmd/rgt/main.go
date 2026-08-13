package main

import (
	"fmt"
	"os"

	"github.com/regent-vcs/regent/internal/cli"
	"github.com/spf13/cobra"
)

func main() {
	rootCmd := &cobra.Command{
		Use:     "rgt",
		Short:   "re_gent - version control for AI agent activity",
		Long:    "re_gent is a content-addressed version control system for AI agent activity.\nIt captures what an agent did, why, and lets you blame, log, and inspect steps across sessions.",
		Version: cli.Version,
		// Bare `rgt` is the front door once a server is known: offer to wire
		// more projects rather than printing help nobody asked for.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cli.RunDefaultSetup(); err != nil {
				// No server yet: help is the useful answer, not an error dump.
				_ = cmd.Help()
				return nil
			}
			return nil
		},
	}
	// Make `rgt --version` print the same line as `rgt version`.
	rootCmd.SetVersionTemplate(cli.VersionString() + "\n")

	// Add commands in desired help order (init first, then common commands)
	rootCmd.AddCommand(cli.InitCmd())
	rootCmd.AddCommand(cli.DoctorCmd())
	rootCmd.AddCommand(cli.LoginCmd())
	rootCmd.AddCommand(cli.WhoamiCmd())
	rootCmd.AddCommand(cli.PushCmd())
	rootCmd.AddCommand(cli.ConnectCmd())
	rootCmd.AddCommand(cli.SetupCmd())
	rootCmd.AddCommand(cli.DisconnectCmd())
	rootCmd.AddCommand(cli.LogCmd())
	rootCmd.AddCommand(cli.StatusCmd())
	rootCmd.AddCommand(cli.BlameCmd())
	rootCmd.AddCommand(cli.ShowCmd())
	rootCmd.AddCommand(cli.SessionsCmd())
	rootCmd.AddCommand(cli.RewindCmd())
	rootCmd.AddCommand(cli.ServeCmd())
	rootCmd.AddCommand(cli.SyncCmd())
	rootCmd.AddCommand(cli.HookCmd())
	rootCmd.AddCommand(MessageHookCmd())
	rootCmd.AddCommand(ToolBatchHookCmd())
	rootCmd.AddCommand(CodexHookCmd())
	rootCmd.AddCommand(OpenCodeHookCmd())
	rootCmd.AddCommand(PiHookCmd())
	rootCmd.AddCommand(cli.CatCmd())
	rootCmd.AddCommand(cli.MergeCmd())
	rootCmd.AddCommand(cli.VersionCmd())

	// Disable alphabetical sorting to preserve our order
	rootCmd.CompletionOptions.DisableDefaultCmd = false
	cobra.EnableCommandSorting = false

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
