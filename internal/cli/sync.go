package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bonez-io/re_gent/internal/capture"
	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/remote"
	"github.com/bonez-io/re_gent/internal/store"
	"github.com/spf13/cobra"
)

// manualSyncTimeout is generous compared to the hook-path budget: the user is
// waiting on a command they typed, not on an agent turn.
const manualSyncTimeout = 60 * time.Second

type syncOptions struct {
	status    bool
	pull      bool
	repair    bool
	workspace bool
	ref       string
}

// SyncCmd is the manual escape hatch for server mode: it drains work the hooks
// could not deliver, reports how far behind the server is, and rebuilds a lost
// cache from the server.
func SyncCmd() *cobra.Command {
	opts := syncOptions{}

	cmd := &cobra.Command{
		Use:   "sync [ref]",
		Short: "Deliver queued server-mode capture (or inspect what is queued)",
		Long: "Deliver everything the local server-mode cache owes the re_gent server.\n\n" +
			"Server mode spools work when the server is unreachable so a live agent turn is\n" +
			"never blocked. 'rgt sync' drains that queue; 'rgt sync --status' reports it\n" +
			"without touching the network; 'rgt sync --pull <ref>' safely rebuilds a lost cache\n" +
			"from the server, which is the source of truth.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				opts.ref = args[0]
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			if opts.workspace {
				return runSyncWorkspace(cmd.OutOrStdout(), cwd)
			}
			cfg, err := remote.LoadConfigForCWD(remote.OSEnv, cwd)
			if err != nil {
				return err
			}
			return runSync(cmd.OutOrStdout(), cfg, opts)
		},
	}

	cmd.Flags().BoolVar(&opts.status, "status", false, "report queued work without contacting the server")
	cmd.Flags().BoolVar(&opts.pull, "pull", false, "rebuild the local cache from the server")
	cmd.Flags().BoolVar(&opts.repair, "repair", false, "re-verify the whole history on the server and re-upload anything missing")
	cmd.Flags().BoolVar(&opts.workspace, "workspace", false, "refresh the workspace baseline (refs/sync/workspace) from the git working tree")
	return cmd
}

// runSyncWorkspace is `rgt sync --workspace`. Unlike the rest of this command
// it works in local mode too: the baseline this writes is what makes the
// Files view non-empty before the first captured agent step, and that is
// just as true for a project that has never configured a server. In server
// mode, once the baseline is written, this delivers it immediately by
// pushing refs/sync/workspace explicitly — the same targeted push an
// explicit `rgt sync <ref>` makes — mirroring `rgt sync`'s own contract for a
// command a person is waiting on, while runWorkspaceSync itself never
// delivers eagerly (see its doc comment): only one place gets to report
// "delivered".
func runSyncWorkspace(out io.Writer, cwd string) error {
	res, err := runWorkspaceSync(cwd)
	if err != nil {
		return fmt.Errorf("workspace sync: %w", err)
	}
	if res.Wrote {
		fmt.Fprintf(out, "Workspace baseline updated: %d file(s), now at %s\n", res.FileCount, shortHash(store.Hash(res.StepHash)))
	} else {
		fmt.Fprintf(out, "Workspace baseline unchanged: %d file(s), already at %s\n", res.FileCount, shortHash(store.Hash(res.StepHash)))
	}

	cfg, cfgErr := remote.LoadConfigForCWD(remote.OSEnv, cwd)
	if cfgErr != nil || !cfg.Enabled() || cfg.Validate() != nil {
		return nil // local mode: nothing further to deliver
	}
	// Named explicitly: runSync with no ref drains Status's queue, which
	// deliberately excludes refs/sync/* (see the comment on Spool.Status), so
	// an empty ref here would silently deliver nothing and still claim
	// "up to date".
	return runSync(out, cfg, syncOptions{ref: capture.WorkspaceSyncRef})
}

func runSync(out io.Writer, cfg remote.Config, opts syncOptions) error {
	if !cfg.Enabled() {
		return fmt.Errorf("server mode is not configured\n\n" +
			"Set REGENT_SERVER_URL and REGENT_REPO_ID, or add a [server] section to ~/.regent/config.toml")
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

	if opts.status {
		return printSyncStatus(out, cfg, cache, spool)
	}

	client, err := remote.NewHTTPClient(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), manualSyncTimeout)
	defer cancel()

	if opts.pull && opts.repair {
		return fmt.Errorf("--pull and --repair do opposite things; choose one")
	}
	if opts.pull {
		return runPull(ctx, out, cache, client, spool, opts.ref)
	}
	if opts.repair {
		return runRepair(ctx, out, cache, client, spool, opts.ref)
	}
	return runPush(ctx, out, cache, client, spool, opts.ref)
}

// runRepair re-uploads anything the server is missing anywhere in the history,
// not just the delta since the last confirmed push.
func runRepair(ctx context.Context, out io.Writer, cache *store.Store, client remote.Client, spool *remote.Spool, ref string) error {
	refs, err := repairTargets(cache, ref)
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		fmt.Fprintln(out, "Nothing to repair: this cache has no session refs.")
		return nil
	}

	for _, refName := range refs {
		res, err := remote.Repair(ctx, cache, client, spool, refName)
		if err != nil {
			return syncFailure(err)
		}
		fmt.Fprintf(out, "%s: verified %s (%d missing object(s) restored)\n",
			refName, shortHash(res.Tip), res.Objects)
	}
	return nil
}

// repairTargets defaults to session refs only, deliberately excluding
// refs/sync/*: repair ends in the same CAS ref update a push does, and the
// workspace-sync ref routinely "diverges" between ordinary machines that have
// never shared a baseline (see the comment on Spool.Status). Name it
// explicitly — `rgt sync --repair sync/workspace` — to repair it anyway.
func repairTargets(cache *store.Store, ref string) ([]string, error) {
	if ref != "" {
		return []string{qualifyRef(ref)}, nil
	}
	return remote.SessionRefs(cache)
}

func printSyncStatus(out io.Writer, cfg remote.Config, cache *store.Store, spool *remote.Spool) error {
	status, err := spool.Status(cache)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "server:  %s\n", cfg.ServerURL)
	fmt.Fprintf(out, "repo:    %s\n", cfg.Key())
	fmt.Fprintf(out, "cache:   %s\n", cache.Root)

	if status.Clean() {
		fmt.Fprintln(out, "\nUp to date: the server has everything captured locally.")
		return nil
	}

	fmt.Fprintf(out, "\nQueued for delivery: %d ref(s), %d step(s), %d loose object(s)\n",
		status.PendingRefs, status.PendingSteps, len(status.LooseObjects))
	for _, lag := range status.Refs {
		if !lag.Pending() {
			continue
		}
		pushed := "(nothing)"
		if lag.Pushed != "" {
			pushed = shortHash(lag.Pushed)
		}
		steps := "?"
		if lag.Steps >= 0 {
			steps = fmt.Sprintf("%d", lag.Steps)
		}
		fmt.Fprintf(out, "  %s: server at %s, local at %s (%s step(s) behind)\n",
			lag.Ref, pushed, shortHash(lag.Local), steps)
	}
	if status.UnknownDeltas > 0 {
		fmt.Fprintf(out, "\n%d ref(s) could not be measured against the server's last known tip.\n", status.UnknownDeltas)
	}
	fmt.Fprintln(out, "\nRun 'rgt sync' to deliver it.")
	return nil
}

func runPush(ctx context.Context, out io.Writer, cache *store.Store, client remote.Client, spool *remote.Spool, ref string) error {
	if ref != "" {
		refName := qualifyRef(ref)
		res, err := remote.Push(ctx, cache, client, spool, refName)
		if err != nil {
			return syncFailure(err)
		}
		fmt.Fprintf(out, "%s: %s (%d object(s) uploaded)\n", refName, describeTip(res), res.Objects)
		return nil
	}

	res := remote.Flush(ctx, cache, client, spool)
	for _, pushed := range res.Refs {
		fmt.Fprintf(out, "%s: %s (%d object(s) uploaded)\n", pushed.Ref, describeTip(pushed), pushed.Objects)
	}
	if res.Objects > 0 {
		fmt.Fprintf(out, "loose objects: %d uploaded\n", res.Objects)
	}
	if res.Failed() {
		return syncFailure(res.Err())
	}
	if len(res.Refs) == 0 && res.Objects == 0 {
		fmt.Fprintln(out, "Up to date — all captured steps are already synced.")
	}
	return nil
}

// syncFailure turns a delivery failure into an actionable message. Nothing is
// lost when this happens: the work stays queued in the cache.
func syncFailure(err error) error {
	switch {
	case errors.Is(err, remote.ErrDiverged):
		return fmt.Errorf("%w\n\nThe server's history is not an ancestor of yours; nothing was overwritten", err)
	case errors.Is(err, remote.ErrUnauthorized):
		return fmt.Errorf("%w\n\nCheck the token in ~/.regent/config.toml or $REGENT_TOKEN", err)
	default:
		return fmt.Errorf("%w\n\nCaptured work is still queued locally; retry with 'rgt sync'", err)
	}
}

func runPull(ctx context.Context, out io.Writer, cache *store.Store, client remote.Client, spool *remote.Spool, ref string) error {
	var refs []string
	if ref != "" {
		refs = append(refs, qualifyRef(ref))
	} else {
		known, err := spool.KnownRefs()
		if err != nil {
			return err
		}
		refs = append(refs, known...)
	}
	if len(refs) == 0 {
		return fmt.Errorf("no ref to pull\n\n" +
			"This cache has no record of a previous push. Name the session ref explicitly:\n" +
			"    rgt sync --pull sessions/claude_code--<session-id>")
	}

	idx, err := index.Open(cache)
	if err != nil {
		return fmt.Errorf("open cache index: %w", err)
	}
	defer func() { _ = idx.Close() }()

	for _, refName := range refs {
		// Pull fetches the server's objects first, then only advances the ref when
		// doing so cannot orphan local history. Hydrate is deliberately more
		// forceful for a genuinely lost cache, but sync --pull also runs against
		// live caches and must have the same safety contract as rgt pull.
		res, err := remote.Pull(ctx, cache, client, refName)
		if err != nil {
			return fmt.Errorf("pull %s: %w", refName, err)
		}
		if res.Status == remote.PullLocalAhead {
			fmt.Fprintf(out, "%s: this cache is ahead of the server; nothing pulled (server at %s, local at %s; deliver yours with 'rgt sync')\n",
				refName, res.ServerTip, res.Tip)
			continue
		}
		rebuilt, err := rebuildDerived(cache, idx, res.Tip)
		if err != nil {
			return fmt.Errorf("rebuild index for %s: %w", refName, err)
		}
		if err := spool.RecordPushed(refName, res.Tip); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: %s (%d object(s) fetched, %d step(s) indexed)\n",
			refName, shortHash(res.Tip), res.Objects, rebuilt)
	}
	return nil
}

// rebuildDerived reconstructs the query index and blame sidecars for a chain of
// steps. Both are derived from the objects the server holds, which is why they
// are never transferred over the wire.
func rebuildDerived(cache *store.Store, idx *index.DB, tip store.Hash) (int, error) {
	var chain []store.Hash
	for current := tip; current != ""; {
		step, err := cache.ReadStep(current)
		if err != nil {
			return 0, err
		}
		chain = append(chain, current)
		current = step.Parent
	}

	// Oldest first so blame can inherit from a parent that is already rebuilt.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}

	for _, stepHash := range chain {
		step, err := cache.ReadStep(stepHash)
		if err != nil {
			return 0, err
		}
		tree, err := cache.ReadTree(step.Tree)
		if err != nil {
			return 0, err
		}
		if err := idx.IndexStep(stepHash, step, tree); err != nil {
			return 0, err
		}
		if err := rebuildConversation(cache, idx, stepHash, step); err != nil {
			return 0, err
		}
		if err := capture.ComputeAndWriteBlame(cache, step.Parent, stepHash, step.Tree); err != nil {
			return 0, err
		}
	}
	return len(chain), nil
}

func describeTip(res remote.PushResult) string {
	if res.AlreadyCurrent {
		return "already current at " + shortHash(res.Tip)
	}
	return "delivered " + shortHash(res.Tip)
}

// qualifyRef accepts either a full ref name or a bare session id.
func qualifyRef(ref string) string {
	if strings.Contains(ref, "/") {
		return ref
	}
	return "sessions/" + ref
}

func shortHash(h store.Hash) string {
	if len(h) <= 12 {
		return string(h)
	}
	return string(h[:12])
}
