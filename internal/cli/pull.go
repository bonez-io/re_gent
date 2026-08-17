package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/regent-vcs/regent/internal/index"
	"github.com/regent-vcs/regent/internal/remote"
	"github.com/regent-vcs/regent/internal/store"
	"github.com/spf13/cobra"
)

type pullOptions struct {
	ref string
}

// PullCmd fetches the project's recorded history from the server into this
// machine's cache.
//
// It exists because a teammate who clones a connected project could reach none
// of it. `rgt sync --pull` looks like the answer and is not: with no ref it can
// only offer the refs *this machine previously pushed*, read from the local
// spool, which on a fresh clone is empty — and there was no call anywhere that
// asked the server what it holds. So the one person who most needs to pull was
// the one person who could not, and the read commands told them there were no
// sessions while the server held every one.
func PullCmd() *cobra.Command {
	opts := pullOptions{}

	cmd := &cobra.Command{
		Use:   "pull [ref]",
		Short: "Fetch the project's history from the server into this machine's cache",
		Long: "Fetch the project's recorded history from the re_gent server.\n\n" +
			"With no arguments it asks the server which sessions exist and fetches all of\n" +
			"them, so a fresh clone needs to know nothing in advance. Name a session ref to\n" +
			"fetch just that one. Afterwards 'rgt log', 'rgt show', 'rgt blame' and\n" +
			"'rgt sessions' read the pulled history locally, with no network.\n\n" +
			"A session whose local history is not on the server's is never overwritten:\n" +
			"pull reports it and leaves it alone.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				opts.ref = args[0]
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			cfg, err := remote.LoadConfigForCWD(remote.OSEnv, cwd)
			if err != nil {
				return err
			}
			return runPullCommand(cmd.OutOrStdout(), cfg, opts)
		},
	}

	return cmd
}

func runPullCommand(out io.Writer, cfg remote.Config, opts pullOptions) error {
	if !cfg.Enabled() {
		return fmt.Errorf("server mode is not configured\n\n" +
			"There is no server to pull from. Connect this project with 'rgt connect <server-url>',\n" +
			"or set REGENT_SERVER_URL and REGENT_REPO_ID")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	cacheDir, err := remote.CacheDirFor(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	cache, err := store.Open(cacheDir)
	if err != nil {
		return fmt.Errorf("open cache store: %w", err)
	}
	spool, err := remote.OpenSpool(filepath.Join(cacheDir, "spool"))
	if err != nil {
		return err
	}

	client, err := remote.NewHTTPClient(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), manualSyncTimeout)
	defer cancel()

	refs, err := pullTargets(ctx, client, opts.ref)
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		fmt.Fprintf(out, "%s holds no history for %s yet.\n", cfg.ServerURL, cfg.RepoID)
		fmt.Fprintln(out, "Nothing has been pushed to this project. Once a teammate records a session, 'rgt pull' will find it.")
		return nil
	}

	idx, err := index.Open(cache)
	if err != nil {
		return fmt.Errorf("open cache index: %w", err)
	}
	defer func() { _ = idx.Close() }()

	var refused []string
	pulled := 0
	for _, refName := range refs {
		res, err := remote.Pull(ctx, cache, client, refName)
		switch {
		case errors.Is(err, remote.ErrDiverged):
			// Reported and skipped rather than fatal: one session this machine
			// happens to disagree about must not withhold everyone else's.
			refused = append(refused, refName)
			fmt.Fprintf(out, "%s: diverged, left alone (server at %s, local at %s)\n",
				refName, shortHash(res.ServerTip), shortHash(res.Tip))
			continue
		case err != nil:
			return fmt.Errorf("pull %s: %w", refName, err)
		}

		switch res.Status {
		case remote.PullLocalAhead:
			// Nothing to pull, but something to say: the undelivered steps are
			// only on this machine until they are pushed.
			fmt.Fprintf(out, "%s: this machine is ahead of the server; nothing to pull (deliver yours with 'rgt sync')\n", refName)
		case remote.PullAlreadyCurrent:
			fmt.Fprintf(out, "%s: already current at %s\n", refName, shortHash(res.Tip))
		default:
			// Only an advanced ref is indexed. The index is what log, show and
			// sessions read, and indexing a chain no ref points at would put
			// history on screen that the store does not consider current.
			indexed, err := rebuildDerived(cache, idx, res.Tip)
			if err != nil {
				return fmt.Errorf("rebuild index for %s: %w", refName, err)
			}
			// The server demonstrably has this tip, so recording it here keeps
			// a later 'rgt sync' from re-uploading history it just handed us.
			if err := spool.RecordPushed(refName, res.Tip); err != nil {
				return err
			}
			pulled++
			fmt.Fprintf(out, "%s: %s (%d step(s), %d object(s) fetched)\n",
				refName, shortHash(res.Tip), indexed, res.Objects)
		}
	}

	printPullFollowUp(out, pulled, refused)

	if len(refused) > 0 {
		return fmt.Errorf("%d session(s) diverged and were not pulled: %v", len(refused), refused)
	}
	return nil
}

// printPullFollowUp names the next command, which is the whole point of having
// pulled: history nobody reads is a file copy.
func printPullFollowUp(out io.Writer, pulled int, refused []string) {
	if pulled > 0 {
		fmt.Fprintf(out, "\nPulled %d session(s). Read them with 'rgt sessions' and 'rgt log'.\n", pulled)
	}
	if len(refused) > 0 {
		fmt.Fprintln(out, "\nThe sessions above have local history the server's does not contain,")
		fmt.Fprintln(out, "so pulling them would discard it. Deliver yours first with 'rgt sync',")
		fmt.Fprintln(out, "which will report the same disagreement from the other side.")
	}
}

// pullTargets decides which refs to fetch: the one the user named, or whatever
// the server says it has.
//
// Discovery is what makes the no-argument form work on a machine that has
// pushed nothing. 'rgt sync --pull' asks the local spool instead, which only
// ever knows what this machine sent — empty on a fresh clone, which is exactly
// the case that needs an answer.
func pullTargets(ctx context.Context, client remote.Client, ref string) ([]string, error) {
	if ref != "" {
		return []string{qualifyRef(ref)}, nil
	}
	return remote.ServerSessionRefs(ctx, client)
}
