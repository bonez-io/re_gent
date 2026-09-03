package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/bonez-io/re_gent/internal/remote"
	"github.com/bonez-io/re_gent/internal/store"
	"github.com/spf13/cobra"
)

// gitHookCooldown is how long a failed push-time delivery suppresses the next
// attempt. Same value capture uses after a failed agent-turn delivery, for the
// same reason: a long outage must stay cheap, and two `git push` in a row
// during one should not each pay the full network timeout.
const gitHookCooldown = 30 * time.Second

// GitHookCmd is the hidden entry point the pre-push script invokes.
//
// It is a separate command rather than a flag on `rgt sync` because the two
// have opposite contracts. `rgt sync` is typed by a person who is waiting for
// an answer: it uses a generous timeout, prints a report, and exits non-zero on
// failure. This runs inside `git push`, where the person is waiting for Git:
// one bounded attempt, at most one line, and exit 0 always — no outcome here
// may become Git's outcome. Sharing a command would mean one of those contracts
// quietly winning over the other.
func GitHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    gitHookVerb + " " + gitHookName,
		Short:  "Deliver queued history during git push (internal)",
		Hidden: true,
		Args:   cobra.ArbitraryArgs, // Git passes remote name and URL; both ignored
		// SilenceErrors and a RunE that never returns one: even a bug that
		// panics inside is caught below, so the exit code Git sees is 0.
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil // unknown hook name: nothing to do, still exit 0
			}
			switch args[0] {
			case gitHookName:
				runGitPrePushHook(cmd.ErrOrStderr(), os.LookupEnv, time.Now)
			case gitPostCommitHookName:
				runGitPostCommitHook(cmd.ErrOrStderr(), os.LookupEnv)
			}
			return nil
		},
	}
	return cmd
}

// runGitPostCommitHook is the post-commit behaviour: refresh the workspace
// baseline, bounded, so the Files view reflects what a commit just changed
// without a live agent turn having to touch those files first. It is
// deliberately simpler than runGitPrePushHook: there is no server-mode queue
// to drain here — the workspace sync's own delivery already handles that in
// server mode (see runWorkspaceSyncServerMode) — so a commit's only job is to
// keep the baseline current.
//
// The installed script backgrounds this whole process (see
// postCommitHookScript), so the bounded wait here is belt and braces: even if
// something someday invokes this synchronously, `git commit` still cannot be
// made to wait past workspaceSyncHookBudget.
//
// It writes at most one line, only when there is something worth saying
// (a change was recorded); silence on every other outcome — no-op, timeout,
// error — matches the pre-push hook's "silence never means failure" contract.
func runGitPostCommitHook(out io.Writer, env func(string) (string, bool)) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(out, "Regent: workspace sync skipped (%v)\n", r)
		}
	}()

	if optedOutOfGitHook(env) {
		return
	}

	cwd, err := os.Getwd()
	if err != nil {
		return
	}

	res, ok, err := runWorkspaceSyncBounded(cwd, remote.Env(env), workspaceSyncHookBudget)
	if !ok {
		// Still running in the background; nothing to report yet, and nothing
		// was lost — see runWorkspaceSyncBounded.
		return
	}
	if err != nil || !res.Wrote {
		return
	}
	fmt.Fprintf(out, "Regent: workspace baseline updated (%d file(s))\n", res.FileCount)
}

// runGitPrePushHook is the pre-push behaviour, mirroring the agent-turn
// delivery in capture/servermode.go step for step: read the outbox from
// durable state, stop if clean, honour the cooldown, one bounded Flush, then
// clear or start the cooldown from the result.
//
// It writes at most one line to out. Nothing when nothing was owed; otherwise
// one line that says what happened and — whenever work remains — which command
// finishes it. Silence never means failure. See RFC 0002 D7.
//
// It never returns and never panics out: every early exit is a plain return,
// and a deferred recover turns a bug into a logged line rather than a failed
// push. That recover is the last line of defence, not the design; the script
// also discards the exit code and always exits 0.
func runGitPrePushHook(out io.Writer, env func(string) (string, bool), now func() time.Time) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(out, "Regent: sync skipped (%v)\n", r)
		}
	}()

	if optedOutOfGitHook(env) {
		return
	}

	cwd, err := os.Getwd()
	if err != nil {
		return
	}

	// Refresh the workspace baseline before draining the queue: a push is as
	// good a moment as a commit to notice the working tree moved, and unlike
	// the delivery below this runs in local mode too — it resolves its own
	// store regardless of server configuration, so it is not behind the
	// cfg.Enabled() gate that follows. Bounded and best-effort: a failure or a
	// timeout here must never turn into an output line, let alone abort the
	// rest of this hook.
	_, _, _ = runWorkspaceSyncBounded(cwd, remote.Env(env), workspaceSyncHookBudget)

	cfg, err := remote.LoadConfigForCWD(remote.Env(env), cwd)
	if err != nil || !cfg.Enabled() {
		return // local mode, or an unreadable binding: nothing to deliver to
	}
	if err := cfg.Validate(); err != nil {
		return
	}

	cacheDir, err := remote.CacheDirFor(cfg)
	if err != nil {
		return
	}
	cache, err := store.Open(cacheDir)
	if err != nil {
		return // no cache yet means nothing has been captured on this machine
	}
	spool, err := remote.OpenSpool(filepath.Join(cacheDir, "spool"))
	if err != nil {
		return
	}

	status, err := spool.Status(cache)
	if err != nil {
		fmt.Fprintf(out, "Regent: could not read the sync queue (%v) — run 'rgt sync --status'\n", err)
		return
	}
	if status.Clean() {
		return
	}

	// Cooldown: a recent attempt already failed. Do not spend the budget again;
	// say how much is queued and how to force it.
	if cooling, _, err := spool.InCooldown(now()); err == nil && cooling {
		fmt.Fprintf(out, "Regent: %s queued, retry cooling down (rgt sync to force)\n", pluralSteps(status))
		return
	}

	client, err := remote.NewHTTPClient(cfg)
	if err != nil {
		fmt.Fprintf(out, "Regent: %s queued, server config invalid (%v)\n", pluralSteps(status), err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	res := remote.Flush(ctx, cache, client, spool)
	delivered := 0
	for _, r := range res.Refs {
		delivered += r.Steps
	}

	if !res.Failed() {
		_ = spool.ClearCooldown()
		fmt.Fprintf(out, "Regent: delivered %d step(s)\n", delivered)
		return
	}

	// Something stayed queued. Objects-first-ref-last means whatever was
	// confirmed is confirmed; the rest is exactly as pending as before.
	_ = spool.StartCooldown(now().Add(gitHookCooldown))
	if delivered > 0 {
		fmt.Fprintf(out, "Regent: delivered %d of %d step(s), rest queued (rgt sync)\n",
			delivered, status.PendingSteps)
		return
	}
	if ctx.Err() != nil {
		fmt.Fprintf(out, "Regent: server slow — %s queued (rgt sync)\n", pluralSteps(status))
		return
	}
	fmt.Fprintf(out, "Regent: server unreachable — %s queued (rgt sync)\n", pluralSteps(status))
}

// pluralSteps renders the queue depth for the one-line report. When the count
// is unknown for some refs (UnknownDeltas), it says so rather than printing a
// number smaller than the truth.
func pluralSteps(st remote.Status) string {
	if st.UnknownDeltas > 0 {
		return fmt.Sprintf("%d ref(s)", st.PendingRefs)
	}
	return fmt.Sprintf("%d step(s)", st.PendingSteps)
}
