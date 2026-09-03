package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/bonez-io/re_gent/internal/capture"
	"github.com/bonez-io/re_gent/internal/remote"
	"github.com/bonez-io/re_gent/internal/remote/remotecapture"
	"github.com/bonez-io/re_gent/internal/style"
)

// workspaceSyncHookBudget bounds how long a git hook's workspace sync may run
// before the hook stops waiting on it and moves on. Hooks must never make a
// git command wait indefinitely on hashing a large working tree.
const workspaceSyncHookBudget = 2 * time.Second

// workspaceSyncResult reports what one workspace sync did, for callers that
// print a summary (rgt sync --workspace, rgt init, rgt connect).
type workspaceSyncResult struct {
	StepHash  string
	Wrote     bool
	FileCount int
}

// runWorkspaceSync writes (or extends) the workspace baseline for cwd.
//
// It resolves the same store an agent-turn hook would write to — the local
// .regent/ store in local mode, the machine-local server cache in server
// mode — by following openHookRecorder's exact choice (cmd/rgt/message_hook.go):
// remotecapture.Open when server mode is configured and valid, capture.Open
// otherwise. A baseline written here therefore always lives exactly where the
// next agent turn's steps will.
//
// This only ever writes locally; it never delivers to a server itself, and
// deliberately so. Spool.Status — the queue every git hook and agent turn
// drains automatically — excludes refs/sync/* on purpose (see the comment on
// Status, internal/remote/spool.go): two ordinary machines routinely produce
// incompatible rootless sync chains, so folding delivery in here would have
// meant either racing that automatic drain (an earlier version did exactly
// this, and its own "delivered N step(s)" line sometimes had nothing left to
// report once the workspace sync had already silently pushed everything) or
// inheriting the same false-conflict risk for something that carries none of
// a session's stakes. `rgt sync --workspace` delivers explicitly afterward,
// by name, the same way `rgt sync <ref>` pushes any other named ref.
func runWorkspaceSync(cwd string) (workspaceSyncResult, error) {
	return runWorkspaceSyncWithEnv(cwd, remote.OSEnv)
}

// runWorkspaceSyncWithEnv is runWorkspaceSync with an injectable environment
// reader, so a caller that already threads one for testability (the git
// hooks; see runGitPrePushHook) does not have this reach past it to the real
// process environment.
func runWorkspaceSyncWithEnv(cwd string, env remote.Env) (workspaceSyncResult, error) {
	cfg, cfgErr := remote.LoadConfigForCWD(env, cwd)
	serverMode := cfgErr == nil && cfg.Enabled() && cfg.Validate() == nil

	if serverMode {
		return runWorkspaceSyncServerMode(cwd, cfg)
	}
	return runWorkspaceSyncLocal(cwd)
}

// runWorkspaceSyncBounded runs runWorkspaceSync but never blocks the caller
// for longer than budget. When the budget is exceeded, the sync keeps running
// to completion in its own goroutine — harmless, since object and ref writes
// are atomic and a step chains onto whatever the ref points to when it
// finally lands — and this reports ok=false so the caller can say so without
// waiting further. It exists for the git hooks: a workspace sync costs about
// what an agent turn's snapshot already costs, but a hook firing on every
// commit must never make `git commit` itself pay an unbounded version of that
// cost.
func runWorkspaceSyncBounded(cwd string, env remote.Env, budget time.Duration) (result workspaceSyncResult, ok bool, err error) {
	type outcome struct {
		res workspaceSyncResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := runWorkspaceSyncWithEnv(cwd, env)
		done <- outcome{res, err}
	}()
	select {
	case o := <-done:
		return o.res, true, o.err
	case <-time.After(budget):
		return workspaceSyncResult{}, false, nil
	}
}

func runWorkspaceSyncLocal(cwd string) (workspaceSyncResult, error) {
	rec, ok, err := capture.Open(cwd)
	if err != nil {
		return workspaceSyncResult{}, fmt.Errorf("open store: %w", err)
	}
	if !ok {
		return workspaceSyncResult{}, fmt.Errorf("not a re_gent repository (no .regent/ in %s)", cwd)
	}
	defer func() { _ = rec.Close() }()

	stepHash, wrote, fileCount, err := capture.WorkspaceSync(rec.Store, cwd)
	if err != nil {
		return workspaceSyncResult{}, err
	}
	return workspaceSyncResult{StepHash: string(stepHash), Wrote: wrote, FileCount: fileCount}, nil
}

// runBaselineSync is the first-run baseline: `rgt init` and `rgt connect`
// call this once, after hooks are wired, so the Files view is never empty
// before the first captured agent step. It prints exactly one line — success
// or warning — and never returns an error: a baseline snapshot is a
// convenience over capture, and a failure here (a workspace too large to walk
// in time, a permissions problem) must not fail init or connect, which have
// already done everything else they needed to by this point.
func runBaselineSync(out io.Writer, cwd string) {
	res, err := runWorkspaceSync(cwd)
	if err != nil {
		fmt.Fprintf(out, "  %s Baseline snapshot skipped: %v\n", style.Warning(""), err)
		return
	}
	fmt.Fprintf(out, "  %s Baseline snapshot: %d file(s)\n", style.Success(""), res.FileCount)
}

func runWorkspaceSyncServerMode(cwd string, cfg remote.Config) (workspaceSyncResult, error) {
	// remotecapture.Open wires a *Link (capture.Delivery) onto the recorder
	// for parity with the hook path, but it is deliberately never invoked
	// here — see runWorkspaceSync's doc comment.
	rec, _, err := remotecapture.Open(cwd, cfg)
	if err != nil {
		return workspaceSyncResult{}, fmt.Errorf("open server-mode cache: %w", err)
	}
	defer func() { _ = rec.Close() }()

	stepHash, wrote, fileCount, err := capture.WorkspaceSync(rec.Store, cwd)
	if err != nil {
		return workspaceSyncResult{}, err
	}
	return workspaceSyncResult{StepHash: string(stepHash), Wrote: wrote, FileCount: fileCount}, nil
}
