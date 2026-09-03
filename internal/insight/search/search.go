// Package search ranks work items for a query over an index: full-text over
// work items, entities, and every recorded message, plus cosine similarity
// over stored vectors when an embedder is given, fused by reciprocal rank.
// It never calls a model except to embed the query, and the same function
// serves the CLI in local mode and the server's /search route, so both
// answer identically.
package search

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/bonez-io/re_gent/internal/index"
)

// Query is what the caller asked for. An empty Text with a File or Entity
// filter lists the items that match the filter.
type Query struct {
	Text    string `json:"text,omitempty"`
	File    string `json:"file,omitempty"`
	Entity  string `json:"entity,omitempty"`
	Status  string `json:"status,omitempty"`
	Session string `json:"session,omitempty"`
	// Since keeps items started at or after this time; zero means no bound.
	Since time.Time `json:"since,omitempty"`
	Limit int       `json:"limit,omitempty"`
}

// Validate rejects a query with nothing to search for or a bad status.
func (q Query) Validate() error {
	if strings.TrimSpace(q.Text) == "" && q.File == "" && q.Entity == "" {
		return fmt.Errorf("give a query, --file, or --entity")
	}
	if q.Status != "" && !index.ValidWorkItemStatus(q.Status) {
		return fmt.Errorf("status %q: want wip, done, failed, abandoned, or superseded", q.Status)
	}
	return nil
}

// Embedder embeds the query text. Nil means full-text only.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Result is the answer, in the shape both the CLI and the API emit.
type Result struct {
	// Notes are things the reader should know: embeddings unavailable, etc.
	Notes []string `json:"notes"`
	Hits  []Hit    `json:"hits"`
	// NotYetRead lists messages that matched in sessions the worker has not
	// read past, one per session, so nothing is omitted for lack of a
	// summary.
	NotYetRead []Unread `json:"not_yet_read"`
}

// Hit is one ranked work item and why it matched.
type Hit struct {
	Score   float64  `json:"score"`
	Matched []string `json:"matched"`
	Item    WorkItem `json:"work_item"`
}

// Unread is a text match the worker has not read into a work item.
type Unread struct {
	SessionID string `json:"session_id"`
	StepID    string `json:"step_id,omitempty"`
	TurnID    string `json:"turn_id,omitempty"`
	Snippet   string `json:"snippet"`
}

// WorkItem is a work item with everything that hangs off it, as the API and
// `--json` print it.
type WorkItem struct {
	ID                  string     `json:"id"`
	SessionID           string     `json:"session_id"`
	Origin              string     `json:"origin"`
	Status              string     `json:"status"`
	Open                bool       `json:"open"`
	Goal                string     `json:"goal"`
	Approach            string     `json:"approach"`
	Outcome             string     `json:"outcome"`
	ContinuesWorkItemID string     `json:"continues_work_item_id,omitempty"`
	StartStepID         string     `json:"start_step_id,omitempty"`
	EndStepID           string     `json:"end_step_id,omitempty"`
	StartTS             time.Time  `json:"start_ts"`
	EndTS               *time.Time `json:"end_ts,omitempty"`
	Provider            string     `json:"provider"`
	Model               string     `json:"model"`
	PromptVersion       string     `json:"prompt_version"`
	UpdatedAt           time.Time  `json:"updated_at"`
	Entities            []Entity   `json:"entities"`
	Files               []string   `json:"files"`
	Steps               []string   `json:"steps"`
}

// Entity is one linked entity with its role and evidence.
type Entity struct {
	Type       string  `json:"type"`
	Name       string  `json:"name"`
	Ref        string  `json:"ref,omitempty"`
	Role       string  `json:"role"`
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source"`
	Evidence   string  `json:"evidence_step_id"`
}

// Describe loads a work item's entities, files, and steps.
func Describe(idx *index.DB, w index.WorkItem) WorkItem {
	j := WorkItem{
		ID: w.ID, SessionID: w.SessionID, Origin: w.Origin, Status: w.Status, Open: w.Open(),
		Goal: w.Goal, Approach: w.Approach, Outcome: w.Outcome, ContinuesWorkItemID: w.ContinuesWorkItemID,
		StartStepID: w.StartStepID, EndStepID: w.EndStepID, StartTS: w.StartTS,
		Provider: w.ModelProvider, Model: w.ModelName, PromptVersion: w.PromptVersion, UpdatedAt: w.UpdatedAt,
		Entities: []Entity{}, Files: []string{}, Steps: []string{},
	}
	if !w.EndTS.IsZero() {
		end := w.EndTS
		j.EndTS = &end
	}
	links, _ := idx.WorkItemLinks(w.ID)
	for _, l := range links {
		j.Entities = append(j.Entities, Entity{Type: l.Type, Name: l.Name, Ref: l.Ref, Role: l.Role, Confidence: l.Confidence, Source: l.Source, Evidence: l.EvidenceStepID})
	}
	if files, _ := idx.WorkItemFiles(w.ID); files != nil {
		j.Files = files
	}
	if steps, _ := idx.WorkItemSteps(w.ID); steps != nil {
		j.Steps = steps
	}
	return j
}

// DescribeAll is Describe over a list.
func DescribeAll(idx *index.DB, items []index.WorkItem) []WorkItem {
	out := make([]WorkItem, 0, len(items))
	for _, w := range items {
		out = append(out, Describe(idx, w))
	}
	return out
}

const rrfK = 60.0

// Run answers q. embedder may be nil; when it fails, the result carries a
// note and is full-text only, never an error.
func Run(ctx context.Context, idx *index.DB, embedder Embedder, embedProvider, embedModel string, q Query) (Result, error) {
	res := Result{Notes: []string{}, Hits: []Hit{}, NotYetRead: []Unread{}}
	if err := q.Validate(); err != nil {
		return res, err
	}
	q.Text = strings.TrimSpace(q.Text)
	scores := map[string]float64{}
	why := map[string]map[string]bool{}
	credit := func(id, source string, rank int) {
		scores[id] += 1 / (rrfK + float64(rank))
		if why[id] == nil {
			why[id] = map[string]bool{}
		}
		why[id][source] = true
	}
	fetch := 50
	if q.Limit > fetch {
		fetch = q.Limit * 2
	}

	// Restrictions from File and Entity: a set of allowed ids, or nil.
	var allowed map[string]bool
	restrict := func(ids []string, source string) {
		set := map[string]bool{}
		for i, id := range ids {
			set[id] = true
			if q.Text == "" {
				credit(id, source, i)
			}
		}
		if allowed == nil {
			allowed = set
			return
		}
		for id := range allowed {
			if !set[id] {
				delete(allowed, id)
			}
		}
	}
	if q.File != "" {
		ids, err := idx.WorkItemsForFile(q.File)
		if err != nil {
			return res, err
		}
		restrict(ids, "file")
	}
	if q.Entity != "" {
		entities, err := idx.FindEntities(q.Entity, 20)
		if err != nil {
			return res, err
		}
		var ids []string
		seen := map[string]bool{}
		for _, e := range entities {
			linked, err := idx.WorkItemsForEntity(e.ID)
			if err != nil {
				return res, err
			}
			for _, id := range linked {
				if !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
		restrict(ids, "entity")
	}

	if q.Text != "" {
		hits, err := idx.SearchWorkItemsFTS(q.Text, fetch)
		if err != nil {
			return res, err
		}
		for i, h := range hits {
			credit(h.ID, "text", i)
		}
		ehits, err := idx.SearchEntitiesFTS(q.Text, fetch)
		if err != nil {
			return res, err
		}
		for i, h := range ehits {
			credit(h.ID, "entity", i)
		}
		if embedder != nil {
			if err := semanticCredit(ctx, idx, embedder, embedProvider, embedModel, q.Text, fetch, credit, &res); err != nil {
				return res, err
			}
		}

		// Messages: the guarantee that literal text is always findable. A
		// message is "read" once the worker's cursor for its session has
		// passed it; its item is found through its step, or through the span
		// of the session's items when the turn used no tools and has no step.
		mhits, err := idx.SearchMessagesFTS(q.Text, fetch)
		if err != nil {
			return res, err
		}
		cursors := map[string]int{}
		for _, m := range mhits {
			cursor, ok := cursors[m.SessionID]
			if !ok {
				cursor, err = idx.InsightCursor(m.SessionID)
				if err != nil {
					return res, err
				}
				cursors[m.SessionID] = cursor
			}
			var owners []string
			if m.StepID != "" {
				byStep, err := idx.WorkItemsForSteps([]string{m.StepID})
				if err != nil {
					return res, err
				}
				owners = byStep[m.StepID]
			}
			if len(owners) == 0 && m.SeqNum <= cursor {
				owners, err = idx.WorkItemsCovering(m.SessionID, m.Timestamp)
				if err != nil {
					return res, err
				}
			}
			for i, id := range owners {
				credit(id, "message", i)
			}
			if len(owners) == 0 && m.SeqNum > cursor {
				res.NotYetRead = append(res.NotYetRead, Unread{SessionID: m.SessionID, StepID: m.StepID, TurnID: m.TurnID, Snippet: m.Snippet})
			}
		}
	}

	// Load, filter, rank.
	type ranked struct {
		item  index.WorkItem
		score float64
		why   []string
	}
	var list []ranked
	for id, score := range scores {
		if allowed != nil && !allowed[id] {
			continue
		}
		w, ok, err := idx.GetWorkItem(id)
		if err != nil || !ok {
			continue
		}
		if q.Status != "" && w.Status != q.Status {
			continue
		}
		if q.Session != "" && w.SessionID != q.Session {
			continue
		}
		if !q.Since.IsZero() && w.StartTS.Before(q.Since) {
			continue
		}
		var matched []string
		for k := range why[id] {
			matched = append(matched, k)
		}
		sort.Strings(matched)
		list = append(list, ranked{item: w, score: score, why: matched})
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.score != b.score {
			return a.score > b.score
		}
		// An unfinished attempt at the same thing is the most useful thing to know.
		if (a.item.Status == index.WorkItemWIP) != (b.item.Status == index.WorkItemWIP) {
			return a.item.Status == index.WorkItemWIP
		}
		return a.item.StartTS.After(b.item.StartTS)
	})
	if q.Limit > 0 && len(list) > q.Limit {
		list = list[:q.Limit]
	}
	for _, r := range list {
		res.Hits = append(res.Hits, Hit{Score: r.score, Matched: r.why, Item: Describe(idx, r.item)})
	}

	// Unread: one per session, session-filtered, capped.
	seenSession := map[string]bool{}
	unread := []Unread{}
	for _, u := range res.NotYetRead {
		if seenSession[u.SessionID] || (q.Session != "" && u.SessionID != q.Session) {
			continue
		}
		seenSession[u.SessionID] = true
		unread = append(unread, u)
		if q.Limit > 0 && len(unread) >= q.Limit {
			break
		}
	}
	res.NotYetRead = unread
	return res, nil
}

func semanticCredit(ctx context.Context, idx *index.DB, embedder Embedder, provider, model, text string, fetch int, credit func(string, string, int), res *Result) error {
	stored, err := idx.Embeddings("work_item", provider, model)
	if err != nil {
		return err
	}
	if len(stored) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	vectors, err := embedder.Embed(ctx, []string{text})
	if err != nil || len(vectors) != 1 {
		res.Notes = append(res.Notes, fmt.Sprintf("embedding the query failed (%v); full-text only", err))
		return nil
	}
	type scored struct {
		id  string
		sim float64
	}
	var ranked []scored
	for _, e := range stored {
		if sim, ok := Cosine(vectors[0], e.Vector); ok {
			ranked = append(ranked, scored{e.OwnerID, sim})
		}
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].sim > ranked[j].sim })
	for i, r := range ranked {
		if i >= fetch || r.sim < 0.2 {
			break
		}
		credit(r.id, "meaning", i)
	}
	return nil
}

// Cosine is the similarity of two vectors; ok is false when they cannot be
// compared (different lengths, or a zero vector).
func Cosine(a, b []float32) (float64, bool) {
	if len(a) != len(b) || len(a) == 0 {
		return 0, false
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0, false
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb)), true
}
