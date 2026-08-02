package store

import (
	"encoding/json"
	"fmt"
)

// Cause describes what triggered this step
type Cause struct {
	ToolUseID  string `json:"tool_use_id"`
	ToolName   string `json:"tool_name"`
	ArgsBlob   Hash   `json:"args_blob,omitempty"`
	ResultBlob Hash   `json:"result_blob,omitempty"`
}

// Author identifies the human who initiated this step.
type Author struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

// Effect describes side effects of the step
type Effect struct {
	Kind       string `json:"kind"`       // "http_call", "db_write", "process_exec", ...
	Descriptor string `json:"descriptor"` // human-readable summary
}

// Usage holds the model API accounting for a step: tokens billed, cache
// creation/read tokens, and how many API calls produced them. Counts are read
// from the agent host's transcript; they are never derived from message text,
// so a Usage value carries no conversation content.
//
// Subagents is the number of subagent transcripts folded into the counts.
type Usage struct {
	InputTokens         int64 `json:"input_tokens,omitempty"`
	OutputTokens        int64 `json:"output_tokens,omitempty"`
	CacheCreationTokens int64 `json:"cache_creation_input_tokens,omitempty"`
	CacheReadTokens     int64 `json:"cache_read_input_tokens,omitempty"`
	APICalls            int64 `json:"api_calls,omitempty"`
	Subagents           int64 `json:"subagents,omitempty"`
}

// IsZero reports whether every counter is zero.
func (u Usage) IsZero() bool {
	return u == Usage{}
}

// Add returns the field-wise sum of u and other.
func (u Usage) Add(other Usage) Usage {
	return Usage{
		InputTokens:         u.InputTokens + other.InputTokens,
		OutputTokens:        u.OutputTokens + other.OutputTokens,
		CacheCreationTokens: u.CacheCreationTokens + other.CacheCreationTokens,
		CacheReadTokens:     u.CacheReadTokens + other.CacheReadTokens,
		APICalls:            u.APICalls + other.APICalls,
		Subagents:           u.Subagents + other.Subagents,
	}
}

// Sub returns the field-wise difference u - other, clamped at zero. Clamping
// matters because the baseline can exceed the current reading when the host
// starts a fresh transcript (for example after /compact); such a step reports
// no usage rather than a negative count.
func (u Usage) Sub(other Usage) Usage {
	return Usage{
		InputTokens:         clampedSub(u.InputTokens, other.InputTokens),
		OutputTokens:        clampedSub(u.OutputTokens, other.OutputTokens),
		CacheCreationTokens: clampedSub(u.CacheCreationTokens, other.CacheCreationTokens),
		CacheReadTokens:     clampedSub(u.CacheReadTokens, other.CacheReadTokens),
		APICalls:            clampedSub(u.APICalls, other.APICalls),
		Subagents:           clampedSub(u.Subagents, other.Subagents),
	}
}

// TotalTokens returns every token counter added together.
func (u Usage) TotalTokens() int64 {
	return u.InputTokens + u.OutputTokens + u.CacheCreationTokens + u.CacheReadTokens
}

func clampedSub(a, b int64) int64 {
	if a <= b {
		return 0
	}
	return a - b
}

// Step is the equivalent of a git commit
type Step struct {
	Parent          Hash `json:"parent,omitempty"`
	SecondaryParent Hash `json:"secondary_parent,omitempty"` // merge second parent
	Tree            Hash `json:"tree"`
	Transcript      Hash `json:"transcript,omitempty"`
	// Conversation is a content-addressed blob holding the turn's conversation
	// (the user prompt plus assistant/reasoning text) so it survives being
	// pushed to a server that has no local SQLite index to reconstruct it from.
	// omitempty keeps steps written before this field existed byte-identical.
	Conversation   Hash     `json:"conversation,omitempty"`
	Config         Hash     `json:"config,omitempty"` // system prompt + tools + memory hash
	Cause          Cause    `json:"cause,omitempty"`  // DEPRECATED: use Causes instead (kept for backward compat)
	Causes         []Cause  `json:"causes,omitempty"` // Multiple tools in one conversation turn
	SessionID      string   `json:"session_id"`
	Origin         string   `json:"origin,omitempty"`
	TurnID         string   `json:"turn_id,omitempty"`
	AgentID        string   `json:"agent_id,omitempty"`
	Author         Author   `json:"author,omitempty"` // human who initiated this step
	TimestampNanos int64    `json:"ts"`
	Effects        []Effect `json:"effects,omitempty"`

	// Usage is the API usage attributed to this step: the delta between the
	// transcript's totals now and the totals recorded on the parent step.
	// UsageTotal is the transcript-cumulative reading at this step and is what
	// the next step subtracts from. Both are nil when the host gave us no
	// readable transcript, which keeps older steps byte-identical.
	Usage      *Usage `json:"usage,omitempty"`
	UsageTotal *Usage `json:"usage_total,omitempty"`
}

// PrimaryCause returns the canonical cause used for legacy displays and indexes.
func (step *Step) PrimaryCause() Cause {
	if step == nil {
		return Cause{}
	}
	if len(step.Causes) > 0 {
		return step.Causes[0]
	}
	return step.Cause
}

// NormalizeCauses keeps the legacy Cause field and the Causes slice consistent.
func (step *Step) NormalizeCauses() {
	if step == nil {
		return
	}
	if len(step.Causes) == 0 && step.Cause.ToolName != "" {
		step.Causes = []Cause{step.Cause}
	}
	if len(step.Causes) > 0 {
		step.Cause = step.Causes[0]
	}
}

// WriteStep writes a step to the object store
func (s *Store) WriteStep(step *Step) (Hash, error) {
	step.NormalizeCauses()

	data, err := json.Marshal(step)
	if err != nil {
		return "", fmt.Errorf("marshal step: %w", err)
	}

	return s.WriteBlob(data)
}

// ReadStep reads a step from the object store
func (s *Store) ReadStep(h Hash) (*Step, error) {
	data, err := s.ReadBlob(h)
	if err != nil {
		return nil, err
	}

	var step Step
	if err := json.Unmarshal(data, &step); err != nil {
		return nil, fmt.Errorf("unmarshal step %s: %w", h, err)
	}

	return &step, nil
}

// WalkAncestors walks the step's ancestor chain, calling fn for each step
// Stops when fn returns an error or when a step has no parent
func (s *Store) WalkAncestors(h Hash, fn func(*Step) error) error {
	for h != "" {
		step, err := s.ReadStep(h)
		if err != nil {
			return err
		}

		if err := fn(step); err != nil {
			return err
		}

		h = step.Parent
	}
	return nil
}
