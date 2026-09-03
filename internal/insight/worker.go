package insight

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/store"
)

// LogFileName is where the worker writes under .regent/log/.
const LogFileName = "insight.log"

// MaxAttempts is how many times a job is tried before it is failed for good.
const MaxAttempts = 3

// ErrNoProcessor is returned by Worker.Run when no read pipeline is
// registered. Jobs stay queued; nothing is marked done that was not done.
var ErrNoProcessor = errors.New("no insight processor is built into this rgt; jobs stay queued")

// Processor turns one queued job into derived rows. It is the seam the read
// pipeline (RFC 0007 S4) plugs into. A returned error is recorded on the job;
// the job is retried until MaxAttempts unless the error is Permanent.
type Processor interface {
	Process(ctx context.Context, job index.InsightJob) error
}

// ProcessorFunc adapts a function to Processor.
type ProcessorFunc func(ctx context.Context, job index.InsightJob) error

// Process calls f.
func (f ProcessorFunc) Process(ctx context.Context, job index.InsightJob) error { return f(ctx, job) }

// PermanentError marks a failure that retrying will not fix, such as a reply
// that does not parse twice or a session that no longer exists.
type PermanentError struct{ Err error }

func (e PermanentError) Error() string { return e.Err.Error() }
func (e PermanentError) Unwrap() error { return e.Err }

// Permanent wraps err so the worker fails the job without retry.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return PermanentError{Err: err}
}

// Worker drains the insight queue for one store.
type Worker struct {
	Store     *store.Store
	Index     *index.DB
	Processor Processor
	// Log receives one line per event. Nil means the log file under
	// .regent/log/.
	Log func(string)
}

// Report is what one Run did.
type Report struct {
	Done    int
	Retried int
	Failed  int
	// Reset is how many jobs a previous worker left running and this one
	// returned to the queue before starting.
	Reset int
}

// Run takes the lock, resets jobs a dead worker left running, and drains the
// queue until it is empty or ctx is cancelled. The second value is false,
// with no error, when another worker already holds the lock.
func (w *Worker) Run(ctx context.Context) (Report, bool, error) {
	if w.Store == nil || w.Index == nil {
		return Report{}, false, errors.New("worker needs a store and an index")
	}
	lock, held, err := TryLock(w.Store.Root)
	if err != nil {
		return Report{}, false, err
	}
	if !held {
		return Report{}, false, nil
	}
	defer lock.Release()

	var report Report
	reset, err := w.Index.ResetRunningInsightJobs()
	if err != nil {
		return report, true, fmt.Errorf("reset running jobs: %w", err)
	}
	report.Reset = int(reset)
	if reset > 0 {
		w.logf("returned %d job(s) a previous worker left running to the queue", reset)
	}

	if w.Processor == nil {
		return report, true, ErrNoProcessor
	}

	for {
		if err := ctx.Err(); err != nil {
			return report, true, err
		}
		job, ok, err := w.Index.ClaimInsightJob()
		if err != nil {
			return report, true, fmt.Errorf("claim job: %w", err)
		}
		if !ok {
			return report, true, nil
		}

		started := time.Now()
		perr := w.Processor.Process(ctx, job)
		if perr == nil {
			if err := w.Index.CompleteInsightJob(job.ID); err != nil {
				return report, true, fmt.Errorf("complete job %d: %w", job.ID, err)
			}
			report.Done++
			w.logf("job %d %s session=%s done in %s", job.ID, job.Kind, job.SessionID, time.Since(started).Round(time.Millisecond))
			continue
		}

		var permanent PermanentError
		retry := !errors.As(perr, &permanent) && job.Attempts < MaxAttempts && ctx.Err() == nil
		if err := w.Index.FailInsightJob(job.ID, perr, retry); err != nil {
			return report, true, fmt.Errorf("fail job %d: %w", job.ID, err)
		}
		if retry {
			report.Retried++
			w.logf("job %d %s session=%s attempt %d failed, will retry: %v", job.ID, job.Kind, job.SessionID, job.Attempts, perr)
		} else {
			report.Failed++
			w.logf("job %d %s session=%s failed after %d attempt(s): %v", job.ID, job.Kind, job.SessionID, job.Attempts, perr)
		}
		if ctx.Err() != nil {
			return report, true, ctx.Err()
		}
	}
}

func (w *Worker) logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	if w.Log != nil {
		w.Log(line)
		return
	}
	Logf(w.Store.Root, "%s", line)
}

// Logf appends one timestamped line to .regent/log/insight.log. Errors are
// dropped: the log must never be the thing that fails a hook or a worker.
func Logf(root, format string, args ...any) {
	path := filepath.Join(root, "log", LogFileName)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	fmt.Fprintf(f, "[%s] %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}
