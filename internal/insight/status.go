package insight

import (
	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/store"
)

// Status is what `rgt insight status` shows, in one shape for local mode and
// for a server answering on a project's behalf.
type Status struct {
	Enabled bool `json:"enabled"`
	// Active means enabled and a model provider is configured.
	Active      bool   `json:"active"`
	ConfigError string `json:"config_error,omitempty"`
	// Where the providers are configured: "~/.regent/config.toml" locally,
	// or the server's config path when the server answers.
	ProvidersFrom string         `json:"providers_from"`
	ScrubCapture  string         `json:"scrub_capture"`
	WorkItemIdle  string         `json:"work_item_idle"`
	Model         ProviderStatus `json:"model"`
	Embedding     ProviderStatus `json:"embedding"`
	// HasProcessor is false in a build without the read pipeline.
	HasProcessor bool `json:"has_processor"`
	// WorkerPID is set while a worker holds the lock.
	WorkerPID   int                   `json:"worker_pid,omitempty"`
	Queue       map[string]int        `json:"queue"`
	LastFailure *JobFailure           `json:"last_failure,omitempty"`
	Coverage    index.InsightCoverage `json:"coverage"`
}

// ProviderStatus names a configured provider without its key.
type ProviderStatus struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	// Key is the key variable's state in words: "no key needed",
	// "X is set", "X is not set".
	Key string `json:"key,omitempty"`
}

// JobFailure is the most recent failed job.
type JobFailure struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	SessionID string `json:"session_id"`
	Error     string `json:"error"`
}

// Collect assembles the status of one store and index under settings.
// settingsErr, when non-nil, is reported and the provider fields are left
// empty.
func Collect(s *store.Store, idx *index.DB, settings Settings, settingsErr error, providersFrom string) (Status, error) {
	st := Status{ProvidersFrom: providersFrom, HasProcessor: HasProcessor(), Queue: map[string]int{}}
	if settingsErr != nil {
		st.ConfigError = settingsErr.Error()
	} else {
		st.Enabled = settings.Enabled
		st.Active = settings.Active()
		st.ScrubCapture = settings.Scrub.Capture
		st.WorkItemIdle = settings.WorkItemIdle.String()
		if settings.Model.Provider != "" {
			st.Model = ProviderStatus{Provider: settings.Model.Provider, Model: settings.Model.Model, Key: settings.ModelKey().String()}
		}
		if settings.Embedding.Provider != "" {
			st.Embedding = ProviderStatus{Provider: settings.Embedding.Provider, Model: settings.Embedding.Model, Key: settings.EmbeddingKey().String()}
		}
	}
	if pid, alive := Holder(s.Root); alive {
		st.WorkerPID = pid
	}
	counts, err := idx.InsightJobCounts()
	if err != nil {
		return st, err
	}
	st.Queue = counts
	if failed, ok, err := idx.LastFailedInsightJob(); err == nil && ok {
		st.LastFailure = &JobFailure{ID: failed.ID, Kind: failed.Kind, SessionID: failed.SessionID, Error: failed.LastError}
	}
	cov, err := idx.InsightCoverage()
	if err != nil {
		return st, err
	}
	st.Coverage = cov
	return st, nil
}
