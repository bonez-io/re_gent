package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bonez-io/re_gent/internal/cli"
	"github.com/bonez-io/re_gent/internal/remote"
	"github.com/bonez-io/re_gent/internal/store"
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
	var verbose bool
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
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			cli.SetVerbose(verbose)
			// The application edge chooses the backing store once, before a
			// command runs. Read and capture packages only receive an opener;
			// neither can silently decide to switch to a server cache.
			cli.SetStoreOpener(commandStore)
			cli.SetNotPulledReporter(commandNotPulledReporter)
			return nil
		},
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
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Show setup diagnostics and subprocess output")
	// Make `rgt --version` print the same line as `rgt version`.
	rootCmd.SetVersionTemplate(cli.VersionString() + "\n")

	// Add commands in desired help order (init first, then common commands)
	rootCmd.AddCommand(cli.InitCmd())
	rootCmd.AddCommand(cli.DoctorCmd())
	rootCmd.AddCommand(cli.PushCmd())
	rootCmd.AddCommand(cli.PullCmd())
	rootCmd.AddCommand(cli.ConnectCmd())
	rootCmd.AddCommand(cli.DisconnectCmd())
	rootCmd.AddCommand(cli.AuthCmd())
	rootCmd.AddCommand(cli.AdminCmd())
	rootCmd.AddCommand(cli.LogCmd())
	rootCmd.AddCommand(cli.StatusCmd())
	rootCmd.AddCommand(cli.BlameCmd())
	rootCmd.AddCommand(cli.ShowCmd())
	rootCmd.AddCommand(cli.SessionsCmd())
	rootCmd.AddCommand(cli.RewindCmd())
	rootCmd.AddCommand(cli.RepairCmd())
	rootCmd.AddCommand(cli.InsightCmd())
	rootCmd.AddCommand(cli.SkillCmd())
	rootCmd.AddCommand(cli.SyncCmd())
	rootCmd.AddCommand(cli.HookCmd())
	rootCmd.AddCommand(cli.GitHookCmd())
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
	rootCmd.AddCommand(removedCmd("whoami",
		"Use the auth status command for server identity, and git config for capture authorship.\n\n  rgt auth status\n  git config user.name\n  git config user.email"))
	rootCmd.AddCommand(removedCmd("serve",
		"The server now has its own operator binary.\n\n  regent-server --addr 0.0.0.0:7654 --data /data"))

	// Disable alphabetical sorting to preserve our order
	rootCmd.CompletionOptions.DisableDefaultCmd = false
	cobra.EnableCommandSorting = false

	return rootCmd
}

func commandStore(cwd string) (*store.Store, error) {
	cfg, err := remote.LoadConfigForCWD(remote.OSEnv, cwd)
	if err != nil {
		cli.Verbosef(os.Stderr, "warning: server-mode config could not be loaded, using local store: %v\n", err)
		return store.OpenFromDir(cwd)
	}
	if !cfg.Enabled() {
		return store.OpenFromDir(cwd)
	}
	if err := cfg.Validate(); err != nil {
		cli.Verbosef(os.Stderr, "warning: server-mode config could not be loaded, using local store: %v\n", err)
		return store.OpenFromDir(cwd)
	}
	cacheDir, err := remote.CacheDirFor(cfg)
	if err != nil {
		return nil, err
	}
	s, err := store.Open(cacheDir)
	if err != nil {
		return nil, &cli.NotPulledError{Message: fmt.Sprintf("This machine has no cached history for %s. Run 'rgt status' to check the server, then 'rgt pull' when history is available.", cfg.Key())}
	}
	return s, nil
}

func commandNotPulledReporter(w io.Writer) bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	cfg, err := remote.LoadConfigForCWD(remote.OSEnv, cwd)
	if err != nil || !cfg.Enabled() || cfg.Validate() != nil {
		return false
	}
	reportServerModeCache(w, cfg, jsonOutputRequested())
	return true
}

func connectedNotPulledReport(cfg remote.Config) string {
	return fmt.Sprintf("Connected to %s as %s, not yet pulled.\nThis project's history is recorded on the server; none of it is on this machine yet.\n  - Fetch it: rgt pull", cfg.ServerURL, cfg.Key())
}

// jsonOutputRequested reports whether the command line asked for --json
// output. The reporter installed via cli.SetNotPulledReporter is a bare
// func(io.Writer) bool (see internal/cli/repo.go): it is shared by every
// command that can hit an empty server-mode cache (today: log, sessions,
// status), and it is called after cobra has already parsed that command's
// own flags into unexported locals this package cannot reach — the argument
// list is the one thing still available here that says what was actually
// asked for. `rgt log --json` is the motivating case: a NotPulledError from
// that command must not fall back to a prose report, or a parser piped onto
// `rgt log --json` breaks on a run where the cache happens to be empty.
func jsonOutputRequested() bool {
	for _, arg := range os.Args[1:] {
		if arg == "--json" || arg == "--json=true" || strings.HasPrefix(arg, "--json=1") {
			return true
		}
	}
	return false
}

// notPulledJSON is the document the fallback reporter writes in place of
// prose when jsonOutputRequested is true: the same empty session/step shape
// `rgt log --json` and `rgt sessions --format=json` produce when there is
// genuinely nothing to show, plus a note explaining why. A parser downstream
// of `rgt log --json` sees one JSON document either way, never a plain-text
// sentence mixed into a stream it expects to decode.
type notPulledJSON struct {
	SessionID string   `json:"session_id"`
	Sessions  []string `json:"sessions"`
	Steps     []string `json:"steps"`
	Note      string   `json:"note"`
}

// writeNotPulledJSON writes doc's JSON encoding, or — if that somehow fails —
// a minimal hand-built JSON object carrying the same note, so this path can
// never fall through to unstructured text once JSON was asked for.
func writeNotPulledJSON(w io.Writer, note string) {
	doc := notPulledJSON{Sessions: []string{}, Steps: []string{}, Note: note}
	if err := json.NewEncoder(w).Encode(doc); err != nil {
		fmt.Fprintf(w, "{\"note\":%q}\n", note)
	}
}

// reportServerModeCache asks the live server before making any claim about
// history that is absent from this machine's cache. asJSON selects between
// the prose a person reads and the notPulledJSON document a --json caller
// needs; every branch below states the same fact, just twice.
func reportServerModeCache(w io.Writer, cfg remote.Config, asJSON bool) {
	client, err := remote.NewHTTPClient(cfg)
	if err != nil {
		if asJSON {
			writeNotPulledJSON(w, fmt.Sprintf("cannot check %s for this project's history: %v", cfg.ServerURL, err))
			return
		}
		fmt.Fprintf(w, "Cannot check %s for this project's history: %v.\n", cfg.ServerURL, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	refs, err := client.ListRefs(ctx, "sessions")
	switch {
	case errors.Is(err, remote.ErrNotFound):
		if asJSON {
			writeNotPulledJSON(w, fmt.Sprintf("connected to %s as %s, but the server does not know this project; re-register it with: rgt connect %s", cfg.ServerURL, cfg.Key(), cfg.ServerURL))
			return
		}
		fmt.Fprintf(w,
			"Connected to %s as %s, but the server does not know this project.\n"+
				"  - Re-register it: rgt connect %s\n",
			cfg.ServerURL, cfg.Key(), cfg.ServerURL)
	case err != nil:
		if asJSON {
			writeNotPulledJSON(w, fmt.Sprintf("cannot reach %s to check this project's history; this machine's cache is empty: %v", cfg.ServerURL, err))
			return
		}
		fmt.Fprintf(w,
			"Cannot reach %s to check this project's history; this machine's cache is empty.\n"+
				"  - Check the server connection, then try: rgt pull\n"+
				"  - Detail: %v\n",
			cfg.ServerURL, err)
	case len(refs) == 0:
		if asJSON {
			writeNotPulledJSON(w, fmt.Sprintf("connected to %s as %s; the server knows this project but holds no history yet", cfg.ServerURL, cfg.Key()))
			return
		}
		fmt.Fprintf(w,
			"Connected to %s as %s; the server knows this project but holds no history yet.\n"+
				"  - Record a session here, or ask a teammate to deliver one with: rgt sync\n",
			cfg.ServerURL, cfg.Key())
	default:
		if asJSON {
			writeNotPulledJSON(w, connectedNotPulledReport(cfg))
			return
		}
		fmt.Fprintln(w, connectedNotPulledReport(cfg))
	}
}
