package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/insight"
	"github.com/bonez-io/re_gent/internal/insight/provider"
	"github.com/bonez-io/re_gent/internal/store"
)

func init() {
	insight.RegisterProcessor(func(s *store.Store, idx *index.DB, settings insight.Settings) (insight.Processor, error) {
		return New(s, idx, settings)
	})
}

// Reader is the one model call the pipeline makes; provider.Reader satisfies it.
type Reader interface {
	Read(ctx context.Context, instructions string, request []byte) ([]byte, error)
}

// Embedder embeds texts; provider.Embedder satisfies it. Nil means no
// embeddings: search is full-text only.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Processor reads queued jobs into work items.
type Processor struct {
	Store     *store.Store
	Index     *index.DB
	Settings  insight.Settings
	Reader    Reader
	ReadInfo  provider.Info
	Embedder  Embedder
	EmbedInfo provider.Info
	Scrubber  *Scrubber
	// Log receives one line per event; nil means the insight log file.
	Log func(string)

	remotesOnce sync.Once
	remotes     []string
}

// New wires the configured providers. It does no network I/O.
func New(s *store.Store, idx *index.DB, settings insight.Settings) (*Processor, error) {
	reader, readInfo, err := provider.NewReader(settings.Model)
	if err != nil {
		return nil, err
	}
	scrubber, err := NewScrubber(settings.Scrub.Patterns)
	if err != nil {
		return nil, err
	}
	p := &Processor{Store: s, Index: idx, Settings: settings, Reader: reader, ReadInfo: readInfo, Scrubber: scrubber}
	if settings.HasEmbedding() {
		embedder, embedInfo, err := provider.NewEmbedder(settings.Embedding)
		if err != nil {
			return nil, err
		}
		p.Embedder, p.EmbedInfo = embedder, embedInfo
	}
	return p, nil
}

// Process reads everything a session has recorded past its cursor, in
// batches that fit the token budget. A session job starts over from the
// first message and replaces the session's work items.
func (p *Processor) Process(ctx context.Context, job index.InsightJob) error {
	session, ok, err := p.Index.GetSession(job.SessionID)
	if err != nil {
		return err
	}
	if !ok {
		return insight.Permanent(fmt.Errorf("session %s is not recorded", job.SessionID))
	}

	cursor := -1
	replace := job.Kind == index.InsightJobKindSession
	if !replace {
		cursor, err = p.Index.InsightCursor(job.SessionID)
		if err != nil {
			return err
		}
	}

	budget := p.Settings.Model.MaxInputTokens * 4 // bytes; four bytes per token is close enough for a bound
	if budget <= 0 {
		budget = insight.DefaultMaxInputTokens * 4
	}
	first := true
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		b, err := p.gatherTurns(ctx, job.SessionID, cursor, budget)
		if err != nil {
			return err
		}
		if len(b.turns) == 0 {
			return nil
		}
		if err := p.readBatch(ctx, session, b, replace && first); err != nil {
			return err
		}
		first = false
		cursor = b.lastSeq
		if !b.more {
			return nil
		}
	}
}

// readBatch makes one model call for a batch and writes the result.
func (p *Processor) readBatch(ctx context.Context, session index.SessionRow, b batch, replace bool) error {
	var open *index.WorkItem
	var resumable []index.WorkItem
	if !replace {
		if w, ok, err := p.Index.OpenWorkItem(session.ID); err != nil {
			return err
		} else if ok {
			// Idle rule: a long silence closes the open item as wip without a
			// model call; it then appears as resumable like any other.
			if b.turns[0].ts.Sub(w.UpdatedAt) > p.Settings.WorkItemIdle {
				if err := p.Index.CloseWorkItem(w.ID, w.EndStepID, w.UpdatedAt); err != nil {
					return err
				}
				p.logf("session %s: closed %s as wip after %s idle", session.ID, w.ID, b.turns[0].ts.Sub(w.UpdatedAt).Round(time.Minute))
				resumable = append(resumable, w)
			} else {
				open = &w
			}
		}
	}
	others, err := p.Index.ResumableWorkItems(session.ID, 5)
	if err != nil {
		return err
	}
	resumable = append(resumable, others...)

	types, err := p.Index.EntityTypesInUse(30)
	if err != nil {
		return err
	}

	req := Request{
		PromptVersion: 1,
		Repository:    Repository{Remotes: p.gitRemotes(ctx), EntityTypesInUse: types},
		Resumable:     []ResumableView{},
		Turns:         make([]TurnView, 0, len(b.turns)),
	}
	if open != nil {
		v, err := p.viewOf(*open)
		if err != nil {
			return err
		}
		req.OpenWorkItem = &v
	}
	resumableIDs := map[string]bool{}
	for _, w := range resumable {
		v, err := p.viewOf(w)
		if err != nil {
			return err
		}
		resumableIDs[w.ID] = true
		req.Resumable = append(req.Resumable, ResumableView{
			ID: w.ID, SessionID: w.SessionID, Goal: w.Goal, Outcome: w.Outcome, Status: w.Status,
			Entities: v.Entities, EndedAt: rfc3339(w.EndTS),
		})
	}
	var deterministic []EntityView
	for _, t := range b.turns {
		req.Turns = append(req.Turns, t.view)
		deterministic = append(deterministic, t.entities...)
	}
	deterministic = dedupeEntities(deterministic)
	req.DeterministicEntities = deterministic
	if req.DeterministicEntities == nil {
		req.DeterministicEntities = []EntityView{}
	}

	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	scrubbed := p.Scrubber.Scrub(raw)
	// The deterministic entities the model must keep are matched after the
	// scrub, so a URL the scrub rewrote is compared in its rewritten form.
	var shown Request
	if err := json.Unmarshal(scrubbed, &shown); err != nil {
		return insight.Permanent(fmt.Errorf("scrubbed request is no longer valid JSON: %w", err))
	}

	resp, err := p.read(ctx, scrubbed)
	if err != nil {
		return err
	}

	result, err := apply(resp, applyInput{
		sessionID: session.ID, origin: session.Origin, open: open, resumable: resumableIDs,
		turns: b.turns, deterministic: shown.DeterministicEntities,
		provider: p.ReadInfo.Provider, model: p.ReadInfo.Model,
	})
	for _, d := range result.dropped {
		p.logf("session %s: %s", session.ID, d)
	}
	if err != nil {
		return insight.Permanent(err)
	}

	if p.Embedder != nil {
		texts := make([]string, len(result.items))
		for i, w := range result.items {
			texts[i] = embeddingText(w)
		}
		vectors, err := p.Embedder.Embed(ctx, texts)
		if err != nil {
			// Embeddings are a search improvement, not the record. Keep the
			// work items and let `rgt insight rebuild --embeddings` catch up.
			p.logf("session %s: embedding failed, work items stored without vectors: %v", session.ID, err)
		} else if len(vectors) == len(result.items) {
			for i := range result.items {
				result.items[i].Vector = vectors[i]
				result.items[i].EmbedProvider = p.EmbedInfo.Provider
				result.items[i].EmbedModel = p.EmbedInfo.Model
			}
		}
	}

	write := index.InsightWrite{Items: result.items, CursorSession: session.ID, CursorSeq: b.lastSeq}
	if replace {
		write.ReplaceSession = session.ID
	}
	if err := p.Index.WriteInsight(write); err != nil {
		return err
	}
	for _, w := range result.items {
		p.logf("session %s: %s %s %q (%d steps, %d files, %d entities)",
			session.ID, w.Item.ID, w.Item.Status, clip(w.Item.Goal, 60), len(w.StepIDs), len(w.Files), len(w.Links))
	}
	return nil
}

// read calls the model, retrying once when the reply does not parse.
func (p *Processor) read(ctx context.Context, request []byte) (Response, error) {
	var lastErr error
	for attempt := range 2 {
		raw, err := p.Reader.Read(ctx, Instructions, request)
		if err != nil {
			return Response{}, fmt.Errorf("%s: %w", p.readerName(), err)
		}
		resp, err := parseResponse(raw)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		p.logf("read attempt %d: %v", attempt+1, err)
	}
	return Response{}, insight.Permanent(fmt.Errorf("%s: %w", p.readerName(), lastErr))
}

func (p *Processor) readerName() string {
	if p.ReadInfo.Model == "" {
		return p.ReadInfo.Provider
	}
	return p.ReadInfo.Provider + " " + p.ReadInfo.Model
}

func (p *Processor) logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	if p.Log != nil {
		p.Log(line)
		return
	}
	insight.Logf(p.Store.Root, "%s", strings.TrimSpace(line))
}

var _ insight.Processor = (*Processor)(nil)
