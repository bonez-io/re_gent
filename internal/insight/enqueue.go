package insight

import (
	"fmt"
	"os"

	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/store"
)

// HasProcessor reports whether this build carries a read pipeline. Until one
// is registered a hook still queues jobs (so nothing recorded is missed when
// it lands) but does not fork a worker that would only exit.
func HasProcessor() bool { return newProcessor != nil }

// newProcessor is set by the read pipeline's package at init. It is nil in a
// build without one.
var newProcessor func(s *store.Store, idx *index.DB, settings Settings) (Processor, error)

// RegisterProcessor installs the read pipeline. It is called from an init
// function so the CLI and the hook see the same processor.
func RegisterProcessor(fn func(s *store.Store, idx *index.DB, settings Settings) (Processor, error)) {
	newProcessor = fn
}

// NewProcessor builds the registered processor, or returns ErrNoProcessor.
func NewProcessor(s *store.Store, idx *index.DB, settings Settings) (Processor, error) {
	if newProcessor == nil {
		return nil, ErrNoProcessor
	}
	return newProcessor(s, idx, settings)
}

// Turn is what a Stop hook hands to Enqueue: the session and turn just
// finalized and the step it wrote, if it wrote one. A turn with no step
// (the agent only talked) still carries messages a work item needs, so it
// is queued too.
type Turn struct {
	SessionID string
	TurnID    string
	StepID    store.Hash
}

// Enqueuer is what the hook edge installs on a capture Recorder. It reads
// the settings once per call, so a change to either config file takes
// effect on the next turn without a restart.
type Enqueuer struct {
	Store *store.Store
	Index *index.DB
	// CWD is the workspace the worker is spawned in.
	CWD string
	// Executable is the rgt binary to spawn. Empty means os.Executable().
	Executable string
	// Spawn starts the worker. Nil means Spawn; tests replace it.
	Spawn func(exe, cwd, root string) error
}

// Enqueue queues the turn and starts a worker if none is running. Every
// failure is returned for the caller to log; none of them should reach the
// agent. It does nothing, and returns nil, when insight is not active for
// this repository and user.
func (e *Enqueuer) Enqueue(turn Turn) error {
	if e == nil || e.Store == nil || e.Index == nil {
		return nil
	}
	settings, err := Load(e.Store)
	if err != nil {
		return fmt.Errorf("insight settings: %w", err)
	}
	if !settings.Active() {
		return nil
	}

	_, inserted, err := e.Index.EnqueueInsightJob(index.InsightJob{
		Kind:      index.InsightJobKindTurn,
		SessionID: turn.SessionID,
		StepID:    string(turn.StepID),
		TurnID:    turn.TurnID,
	})
	if err != nil {
		return fmt.Errorf("enqueue insight job: %w", err)
	}
	if !inserted || !HasProcessor() {
		return nil
	}
	if _, alive := Holder(e.Store.Root); alive {
		return nil
	}

	exe := e.Executable
	if exe == "" {
		exe, err = os.Executable()
		if err != nil {
			return fmt.Errorf("locate rgt: %w", err)
		}
	}
	spawn := e.Spawn
	if spawn == nil {
		spawn = Spawn
	}
	if err := spawn(exe, e.CWD, e.Store.Root); err != nil {
		return fmt.Errorf("start insight worker: %w", err)
	}
	return nil
}
