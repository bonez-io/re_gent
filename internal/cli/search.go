package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/insight"
	"github.com/bonez-io/re_gent/internal/insight/provider"
	"github.com/bonez-io/re_gent/internal/store"
	"github.com/bonez-io/re_gent/internal/style"
	"github.com/spf13/cobra"
)

// SearchCmd creates the search command.
func SearchCmd() *cobra.Command {
	var file, entity, status, session, since string
	var limit int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Find work items and sessions by meaning, text, entity, or file",
		Long: `Find work items and sessions by meaning, text, entity, or file.

Search is hybrid: full-text over work items, entities, and every recorded
message, plus semantic similarity when an embedding provider is configured,
fused by reciprocal rank. It never calls a model except to embed the query,
and works with no embedding provider at all.

A message that matches but belongs to a session no work item covers yet is
still returned, marked "not yet read", with the step to rgt show. Nothing is
omitted for lack of a summary.`,
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			query := ""
			if len(args) == 1 {
				query = strings.TrimSpace(args[0])
			}
			if query == "" && file == "" && entity == "" {
				return fmt.Errorf("give a query, --file, or --entity")
			}
			if status != "" && !index.ValidWorkItemStatus(status) {
				return fmt.Errorf("status %q: want wip, done, failed, abandoned, or superseded", status)
			}
			var sinceTime time.Time
			if since != "" {
				d, err := time.ParseDuration(since)
				if err != nil {
					return fmt.Errorf("--since %q: want a duration such as 72h", since)
				}
				sinceTime = time.Now().Add(-d)
			}

			s, err := openStoreFromCWD()
			if err != nil {
				return err
			}
			idx, err := index.Open(s)
			if err != nil {
				return err
			}
			defer func() { _ = idx.Close() }()

			res, err := runSearch(cmd.Context(), s, idx, searchQuery{
				Text: query, File: file, Entity: entity, Status: status, Session: session, Since: sinceTime, Limit: limit,
			}, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(res.json(idx))
			}
			printSearchResult(cmd.OutOrStdout(), res)
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "only work items that changed this path")
	cmd.Flags().StringVar(&entity, "entity", "", "only work items linked to this entity: a name, type:name, or ref")
	cmd.Flags().StringVar(&status, "status", "", "only this status")
	cmd.Flags().StringVar(&session, "session", "", "only this session")
	cmd.Flags().StringVar(&since, "since", "", "only items started within this duration, e.g. 72h")
	cmd.Flags().IntVar(&limit, "limit", 10, "maximum results")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

type searchQuery struct {
	Text    string
	File    string
	Entity  string
	Status  string
	Session string
	Since   time.Time
	Limit   int
}

// searchHit is one ranked work item with why it matched.
type searchHit struct {
	Item    index.WorkItem
	Score   float64
	Matched []string
}

// unreadHit is a message match in a session no work item covers.
type unreadHit struct {
	index.MessageHit
}

type searchResult struct {
	Hits   []searchHit
	Unread []unreadHit
	// Notes are things the reader should know: embeddings unavailable, etc.
	Notes []string
}

const rrfK = 60.0

func runSearch(ctx context.Context, s *store.Store, idx *index.DB, q searchQuery, stderr io.Writer) (searchResult, error) {
	var res searchResult
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

	// Restrictions from --file and --entity: a set of allowed ids, or nil.
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
		if err := semanticCredit(ctx, s, idx, q.Text, fetch, credit, &res); err != nil {
			return res, err
		}

		// Messages: the guarantee that literal text is always findable.
		mhits, err := idx.SearchMessagesFTS(q.Text, fetch)
		if err != nil {
			return res, err
		}
		cursors := map[string]int{}
		for _, m := range mhits {
			// A message is "read" once the worker's cursor for its session
			// has passed it; the item it belongs to is found through its
			// step, or through the span of the session's items when the
			// turn used no tools and so has no step.
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
				res.Unread = append(res.Unread, unreadHit{m})
			}
		}
	}

	// Load, filter, rank.
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
		res.Hits = append(res.Hits, searchHit{Item: w, Score: score, Matched: matched})
	}
	sort.SliceStable(res.Hits, func(i, j int) bool {
		a, b := res.Hits[i], res.Hits[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		// An unfinished attempt at the same thing is the most useful thing to know.
		if (a.Item.Status == index.WorkItemWIP) != (b.Item.Status == index.WorkItemWIP) {
			return a.Item.Status == index.WorkItemWIP
		}
		return a.Item.StartTS.After(b.Item.StartTS)
	})
	if q.Limit > 0 && len(res.Hits) > q.Limit {
		res.Hits = res.Hits[:q.Limit]
	}
	// Unread hits: one per session, filtered like items, capped.
	seenSession := map[string]bool{}
	var unread []unreadHit
	for _, u := range res.Unread {
		if seenSession[u.SessionID] || (q.Session != "" && u.SessionID != q.Session) {
			continue
		}
		seenSession[u.SessionID] = true
		unread = append(unread, u)
		if q.Limit > 0 && len(unread) >= q.Limit {
			break
		}
	}
	res.Unread = unread
	return res, nil
}

// semanticCredit embeds the query and credits the nearest stored vectors.
// Any failure degrades to full-text with a note; it never fails the search.
func semanticCredit(ctx context.Context, s *store.Store, idx *index.DB, text string, fetch int, credit func(string, string, int), res *searchResult) error {
	settings, err := insight.Load(s)
	if err != nil || !settings.HasEmbedding() {
		return nil
	}
	embedder, info, err := provider.NewEmbedder(settings.Embedding)
	if err != nil {
		res.Notes = append(res.Notes, "embeddings unavailable ("+err.Error()+"); full-text only")
		return nil
	}
	stored, err := idx.Embeddings("work_item", info.Provider, info.Model)
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
		if sim, ok := cosine(vectors[0], e.Vector); ok {
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

func cosine(a, b []float32) (float64, bool) {
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

func printSearchResult(w io.Writer, res searchResult) {
	for _, n := range res.Notes {
		fmt.Fprintln(w, style.Warning("note:"), n)
	}
	if len(res.Hits) == 0 && len(res.Unread) == 0 {
		fmt.Fprintln(w, "Nothing matched.")
		return
	}
	for i, h := range res.Hits {
		fmt.Fprintf(w, "%2d. %s  %-10s %s\n", i+1, style.DimText(shortID(h.Item.ID)), styleStatus(h.Item.Status), h.Item.Goal)
		detail := fmt.Sprintf("%s · session %s · matched %s", h.Item.StartTS.Local().Format("2006-01-02"), h.Item.SessionID, strings.Join(h.Matched, ", "))
		fmt.Fprintf(w, "    %s\n", style.DimText(detail))
		if h.Item.Outcome != "" {
			fmt.Fprintf(w, "    %s\n", clipLine(h.Item.Outcome, 110))
		}
	}
	if len(res.Unread) > 0 {
		fmt.Fprintf(w, "\n%s\n", style.Warning("Not yet read into work items (text match only):"))
		for _, u := range res.Unread {
			where := "session " + u.SessionID
			if u.StepID != "" {
				where += "  rgt show " + shortID(u.StepID)
			}
			fmt.Fprintf(w, "  %s\n    %s\n", style.DimText(where), clipLine(u.Snippet, 110))
		}
	}
}

func clipLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

type searchJSON struct {
	Notes  []string         `json:"notes"`
	Hits   []searchHitJSON  `json:"hits"`
	Unread []searchUnreadJS `json:"not_yet_read"`
}

type searchHitJSON struct {
	Score   float64      `json:"score"`
	Matched []string     `json:"matched"`
	Item    workItemJSON `json:"work_item"`
}

type searchUnreadJS struct {
	SessionID string `json:"session_id"`
	StepID    string `json:"step_id,omitempty"`
	TurnID    string `json:"turn_id,omitempty"`
	Snippet   string `json:"snippet"`
}

func (r searchResult) json(idx *index.DB) searchJSON {
	out := searchJSON{Notes: r.Notes, Hits: []searchHitJSON{}, Unread: []searchUnreadJS{}}
	if out.Notes == nil {
		out.Notes = []string{}
	}
	for _, h := range r.Hits {
		out.Hits = append(out.Hits, searchHitJSON{Score: h.Score, Matched: h.Matched, Item: workItemsJSON(idx, []index.WorkItem{h.Item})[0]})
	}
	for _, u := range r.Unread {
		out.Unread = append(out.Unread, searchUnreadJS{SessionID: u.SessionID, StepID: u.StepID, TurnID: u.TurnID, Snippet: u.Snippet})
	}
	return out
}
