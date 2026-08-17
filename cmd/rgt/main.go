package main

import (
	"fmt"
	"os"

	"github.com/regent-vcs/regent/internal/cli"
	"github.com/spf13/cobra"
)

func main() {
	if err := newRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// newRootCommand builds the full command tree.
//
// Extracted from main so the command surface itself can be tested: which
// commands exist, which are hidden, and what a removed one says. None of that
// was reachable while the tree was assembled inside main and executed in the
// same breath.
// removedCmd builds a tombstone: a hidden command that fails with an
// explanation naming what to run instead. Deleting the name outright would make
// a script that used it print "unknown command" and stop there.
func removedCmd(name, guidance string) *cobra.Command {
	return &cobra.Command{
		Use:    name,
		Hidden: true,
		Short:  "(removed)",
		// Accept whatever arguments the old command took, so the guidance is
		// what the user sees rather than an arity complaint about a command
		// that no longer exists.
		Args:         cobra.ArbitraryArgs,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("`rgt %s` has been removed.\n\n%s", name, guidance)
		},
	}
}

func newRootCommand() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:          "rgt",
		Short:        "re_gent - version control for AI agent activity",
		Long:         "re_gent is a content-addressed version control system for AI agent activity.\nIt captures what an agent did, why, and lets you blame, log, and inspect steps across sessions.",
		Version:      cli.Version,
		SilenceUsage: true,
		// main prints the error and sets the exit code, so cobra must not print
		// it too. Both did, and every failure in the CLI came out twice. Set on
		// the root because cobra checks the root as well as the command that
		// failed, so this covers the whole tree rather than one command.
		SilenceErrors: true,
		// Bare `rgt` prints help and does nothing else.
		//
		// It used to run the project picker: typing the bare command — the first
		// thing anyone does with an unfamiliar CLI — opened a full-screen
		// multi-select over the filesystem, listing the projects it found below
		// the current directory. Marking one that was already connected meant
		// *disconnect*, so a destructive change to a working project sat one
		// space keypress inside a screen you could reach by accident (#28).
		// A command invoked with no arguments should say what it can do.
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	// Make `rgt --version` print the same line as `rgt version`.
	rootCmd.SetVersionTemplate(cli.VersionString() + "\n")

	// Add commands in desired help order (init first, then common commands)
	rootCmd.AddCommand(cli.InitCmd())
	rootCmd.AddCommand(cli.DoctorCmd())
	rootCmd.AddCommand(cli.PushCmd())
	rootCmd.AddCommand(cli.PullCmd())
	rootCmd.AddCommand(cli.ConnectCmd())
	rootCmd.AddCommand(cli.DisconnectCmd())
	rootCmd.AddCommand(cli.LogCmd())
	rootCmd.AddCommand(cli.StatusCmd())
	rootCmd.AddCommand(cli.BlameCmd())
	rootCmd.AddCommand(cli.ShowCmd())
	rootCmd.AddCommand(cli.SessionsCmd())
	rootCmd.AddCommand(cli.RewindCmd())
	rootCmd.AddCommand(cli.RepairCmd())
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

	// Tombstones for commands that no longer exist. They are hidden, so they do
	// not advertise anything, but they still answer — because people have these
	// in shell history and in scripts, and cobra's reply to a name it does not
	// know is "unknown command", which names no way forward.
	rootCmd.AddCommand(removedCmd("setup",
		"connect now does everything setup did.\n\n  rgt connect <server-url>   wire this project\n  rgt connect                use the server this machine already knows"))
	rootCmd.AddCommand(removedCmd("login",
		"There is no authentication to sign in to: the server performs no authentication,\nand a sign-in command implied otherwise.\n\n  rgt connect <server-url>   connect a project"))
	rootCmd.AddCommand(removedCmd("whoami",
		"Identity comes from your git config, not from a sign-in.\n\n  git config user.name\n  git config user.email\n  rgt doctor                 check what re_gent will record"))

	// Disable alphabetical sorting to preserve our order
	rootCmd.CompletionOptions.DisableDefaultCmd = false
	cobra.EnableCommandSorting = false

	return rootCmd
}
