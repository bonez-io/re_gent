// Package pipeline is the read pipeline of RFC 0007: it turns a session's
// recorded turns into work items and entities through one structured model
// call per batch, and registers itself as the insight processor.
//
// The shapes here are Appendix A of the RFC on the wire. Every field the
// model is shown is scrubbed first; every link the model returns must name
// evidence the model was shown, or it is dropped.
package pipeline

// PromptVersion is stamped on every work item. Bump it whenever the
// instructions text or the request shape changes, so a rebuild can tell a
// stale reading from a fresh one.
const PromptVersion = "1"

// Request is what the model receives.
type Request struct {
	PromptVersion         int             `json:"prompt_version"`
	Repository            Repository      `json:"repository"`
	OpenWorkItem          *WorkItemView   `json:"open_work_item"`
	Resumable             []ResumableView `json:"resumable"`
	Turns                 []TurnView      `json:"turns"`
	DeterministicEntities []EntityView    `json:"deterministic_entities"`
}

// Repository is what the model knows about where it is.
type Repository struct {
	Remotes          []string `json:"remotes"`
	EntityTypesInUse []string `json:"entity_types_in_use"`
}

// WorkItemView is the open work item the new turns may extend.
type WorkItemView struct {
	ID       string       `json:"id"`
	Goal     string       `json:"goal"`
	Approach string       `json:"approach"`
	Outcome  string       `json:"outcome"`
	Status   string       `json:"status"`
	Entities []EntityView `json:"entities"`
}

// ResumableView is a wip item from an earlier session the new turns may
// continue.
type ResumableView struct {
	ID        string       `json:"id"`
	SessionID string       `json:"session_id"`
	Goal      string       `json:"goal"`
	Outcome   string       `json:"outcome"`
	Status    string       `json:"status"`
	Entities  []EntityView `json:"entities"`
	EndedAt   string       `json:"ended_at"`
}

// TurnView is one turn as shown to the model. Step is empty for a turn that
// used no tools; Turn is always set and is accepted as evidence too.
type TurnView struct {
	Turn      string     `json:"turn"`
	Step      string     `json:"step,omitempty"`
	At        string     `json:"at"`
	User      string     `json:"user,omitempty"`
	Assistant string     `json:"assistant,omitempty"`
	Reasoning string     `json:"reasoning,omitempty"`
	Tools     []string   `json:"tools,omitempty"`
	Files     []string   `json:"files,omitempty"`
	Hunks     []HunkView `json:"hunks,omitempty"`
}

// HunkView is one file's diff in a turn, unified-diff style.
type HunkView struct {
	Path string `json:"path"`
	Diff string `json:"diff"`
}

// EntityView is an entity as shown to, or returned by, the model.
type EntityView struct {
	Type           string  `json:"type"`
	Name           string  `json:"name"`
	Ref            string  `json:"ref,omitempty"`
	Role           string  `json:"role,omitempty"`
	Confidence     float64 `json:"confidence,omitempty"`
	EvidenceStepID string  `json:"evidence_step_id"`
}

// Response is what the model returns.
type Response struct {
	WorkItems []WorkItemResponse `json:"work_items"`
}

// WorkItemResponse is one work item the model read out of the turns.
type WorkItemResponse struct {
	// ID is the open item's id when the turns extend it, else null.
	ID string `json:"id"`
	// ContinuesWorkItemID names a resumable item this one picks up.
	ContinuesWorkItemID string `json:"continues_work_item_id"`
	// StartsAtStep is the step or turn id where this item begins; required
	// for a new item, ignored for the open one.
	StartsAtStep string       `json:"starts_at_step"`
	Goal         string       `json:"goal"`
	Approach     string       `json:"approach"`
	Outcome      string       `json:"outcome"`
	Status       string       `json:"status"`
	Entities     []EntityView `json:"entities"`
}
