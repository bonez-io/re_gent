// Package remotecapture is the server-mode adapter for capture. It lives below
// remote so the capture package itself stays transport- and policy-free.
package remotecapture

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bonez-io/re_gent/internal/capture"
	"github.com/bonez-io/re_gent/internal/remote"
	"github.com/bonez-io/re_gent/internal/store"
)

const cooldownAfterFailure = 30 * time.Second

// Link implements capture.Delivery for a remote server and its local outbox.
type Link struct {
	Client  remote.Client
	Spool   *remote.Spool
	Timeout time.Duration
	Now     func() time.Time
}

func (l *Link) now() time.Time {
	if l.Now == nil {
		return time.Now()
	}
	return l.Now()
}

// Open opens a recorder backed by the configured machine-local server cache.
// The caller has already made the explicit storage choice at the command edge.
func Open(cwd string, cfg remote.Config) (*capture.Recorder, *Link, error) {
	if cwd == "" {
		return nil, nil, fmt.Errorf("cwd is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}
	client, err := remote.NewHTTPClient(cfg)
	if err != nil {
		return nil, nil, err
	}
	cacheDir, err := remote.CacheDirFor(cfg)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("create cache dir: %w", err)
	}
	rec, ok, err := capture.OpenStore(cwd, cacheDir)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, fmt.Errorf("open cache store: unavailable")
	}
	spool, err := remote.OpenSpool(filepath.Join(cacheDir, "spool"))
	if err != nil {
		_ = rec.Close()
		return nil, nil, err
	}
	link := &Link{Client: client, Spool: spool, Timeout: cfg.Timeout}
	rec.Delivery = link
	return rec, link, nil
}

// Start delivers work an earlier hook invocation queued.
func (l *Link) Start(rec *capture.Recorder) { l.sync(rec, "hook start") }

// Finalize delivers a completed captured turn.
func (l *Link) Finalize(rec *capture.Recorder) { l.sync(rec, "turn end") }

func (l *Link) QueueObject(rec *capture.Recorder, h store.Hash) {
	if err := l.Spool.MarkObject(h); err != nil {
		capture.LogHookError(rec.Store.Root, fmt.Sprintf("queue object %s for server: %v", h, err))
	}
}

// Sync drains the outbox without ever returning an error to the agent hook.
func (l *Link) Sync(rec *capture.Recorder, reason string) { l.sync(rec, reason) }

func (l *Link) sync(rec *capture.Recorder, reason string) {
	status, err := l.Spool.Status(rec.Store)
	if err != nil {
		capture.LogHookError(rec.Store.Root, fmt.Sprintf("server sync (%s): read outbox: %v", reason, err))
		return
	}
	if status.Clean() {
		return
	}
	if cooling, until, err := l.Spool.InCooldown(l.now()); err != nil {
		capture.LogHookError(rec.Store.Root, fmt.Sprintf("server sync (%s): read cooldown: %v", reason, err))
	} else if cooling {
		return
	} else {
		_ = until
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.Timeout)
	defer cancel()
	res := remote.Flush(ctx, rec.Store, l.Client, l.Spool)
	if !res.Failed() {
		_ = l.Spool.ClearCooldown()
		return
	}
	_ = l.Spool.StartCooldown(l.now().Add(cooldownAfterFailure))
	capture.LogHookError(rec.Store.Root, fmt.Sprintf("server sync (%s) failed; %d step(s) queued in %s, run 'rgt sync' to retry: %v", reason, status.PendingSteps, l.Spool.Dir(), res.Err()))
}
