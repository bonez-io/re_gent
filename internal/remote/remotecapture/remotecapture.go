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
	"github.com/bonez-io/re_gent/internal/config"
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
	// A device-issued access token (RFC 0004) expires; every hook invocation
	// is a fresh process, so the refreshed pair has to be written back to
	// disk here or the next invocation starts from the same stale token and
	// pays the refresh round trip again. A personal access token has no
	// RefreshToken, so this is a no-op for the flow every self-hosted server
	// still uses.
	if cfg.RefreshToken != "" {
		client.SetRefresh(cfg.RefreshToken, persistRefreshedToken(cfg.ServerURL))
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

// persistRefreshedToken returns an HTTPClient.SetRefresh callback that writes
// a refreshed access/refresh pair into the machine-local config so the next
// process (the next hook invocation, a `rgt pull`, a `rgt sync`) starts
// already-authenticated instead of repeating the same refresh.
//
// This runs inside a live agent turn, so a failure here is deliberately
// swallowed: losing the refreshed pair costs one extra refresh round trip
// next time, which is recoverable, whereas surfacing an error here would not
// be — nothing calling this treats a delivery helper's persistence step as
// something the agent turn should fail over.
func persistRefreshedToken(serverURL string) func(accessToken, refreshToken string, expiresIn int) {
	return func(accessToken, refreshToken string, expiresIn int) {
		cfg, err := config.Load()
		if err != nil {
			return
		}
		config.SetDeviceCredential(cfg, serverURL, accessToken, refreshToken, expiresIn)
		_ = config.Save(cfg)
	}
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
