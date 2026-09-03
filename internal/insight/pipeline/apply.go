package pipeline

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/store"
)

// applyInput is everything apply needs beyond the model's reply.
type applyInput struct {
	sessionID     string
	origin        string
	open          *index.WorkItem
	resumable     map[string]bool
	turns         []turn
	deterministic []EntityView
	provider      string
	model         string
}

// applyResult is what the reply became, ready to write.
type applyResult struct {
	items   []index.WorkItemWrite
	dropped []string
}

// parseResponse validates the reply's shape. It is strict about what
// re_gent relies on and lenient about the rest: a bad status or a missing
// evidence id is a dropped link or a rejected item, never a crash.
func parseResponse(raw []byte) (Response, error) {
	var resp Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return Response{}, fmt.Errorf("reply is not the expected JSON: %w", err)
	}
	if len(resp.WorkItems) == 0 {
		return Response{}, fmt.Errorf("reply has no work_items")
	}
	return resp, nil
}

// apply turns a response into writes. Rules (RFC 0007 "Link"):
//   - an item with the open item's id extends it; any other id is ignored
//     and the item is treated as new
//   - a new item must start at a step or turn that was shown, else it is
//     folded into the previous item
//   - every link must name evidence that was shown, else it is dropped
//   - deterministic entities are merged in and never removed
//   - continues_work_item_id must name a resumable item shown
func apply(resp Response, in applyInput) (applyResult, error) {
	var res applyResult
	if len(in.turns) == 0 {
		return res, fmt.Errorf("nothing to apply")
	}

	// Evidence ids the model was shown, and the turn index each maps to.
	position := map[string]int{}
	for i, t := range in.turns {
		position[t.view.Turn] = i
		if t.view.Step != "" {
			position[t.view.Step] = i
		}
	}
	if in.open != nil {
		position[in.open.ID] = -1
	}

	// Decide each item's starting turn index.
	type planned struct {
		resp   WorkItemResponse
		start  int // index into in.turns; -1 for the open item
		isOpen bool
	}
	var plan []planned
	for _, wi := range resp.WorkItems {
		isOpen := in.open != nil && wi.ID != "" && wi.ID == in.open.ID
		start, ok := position[wi.StartsAtStep]
		switch {
		case isOpen:
			start = 0
			if len(plan) > 0 {
				// The open item must come first; a later "extend" is a new item.
				isOpen = false
				if !ok || start < 0 {
					start = plan[len(plan)-1].start
				}
			}
		case !ok || start < 0:
			if len(plan) == 0 {
				start = 0
			} else {
				res.dropped = append(res.dropped, fmt.Sprintf("work item %q starts at unknown %q; merged into previous", clip(wi.Goal, 40), wi.StartsAtStep))
				start = plan[len(plan)-1].start
			}
		}
		plan = append(plan, planned{resp: wi, start: start, isOpen: isOpen})
	}
	// Items are contiguous runs; sort by start and make starts strictly
	// increasing, merging items that would start on the same turn.
	for i := 1; i < len(plan); i++ {
		if plan[i].start <= plan[i-1].start {
			if !plan[i-1].isOpen || plan[i].start > 0 {
				res.dropped = append(res.dropped, fmt.Sprintf("work item %q starts where the previous one does; merged", clip(plan[i].resp.Goal, 40)))
				plan[i-1].resp = mergeResponses(plan[i-1].resp, plan[i].resp)
				plan = append(plan[:i], plan[i+1:]...)
				i--
				continue
			}
		}
	}
	// If there is an open item and the first planned item is not it, the
	// turns before the first new item's start belong to the open item, which
	// closes as wip at that point.
	if in.open != nil && (len(plan) == 0 || !plan[0].isOpen) && len(plan) > 0 && plan[0].start > 0 {
		plan = append([]planned{{resp: WorkItemResponse{
			ID: in.open.ID, Goal: in.open.Goal, Approach: in.open.Approach, Outcome: in.open.Outcome, Status: in.open.Status,
		}, start: 0, isOpen: true}}, plan...)
	}

	deterministicByTurn := map[int][]EntityView{}
	for _, e := range in.deterministic {
		if i, ok := position[e.EvidenceStepID]; ok && i >= 0 {
			deterministicByTurn[i] = append(deterministicByTurn[i], e)
		}
	}

	for k, pl := range plan {
		end := len(in.turns)
		if k+1 < len(plan) {
			end = plan[k+1].start
		}
		if pl.start >= end {
			continue
		}
		turns := in.turns[pl.start:end]

		item := index.WorkItem{
			SessionID:     in.sessionID,
			Origin:        in.origin,
			Goal:          strings.TrimSpace(pl.resp.Goal),
			Approach:      strings.TrimSpace(pl.resp.Approach),
			Outcome:       strings.TrimSpace(pl.resp.Outcome),
			Status:        strings.ToLower(strings.TrimSpace(pl.resp.Status)),
			ModelProvider: in.provider,
			ModelName:     in.model,
			PromptVersion: PromptVersion,
		}
		if !index.ValidWorkItemStatus(item.Status) {
			res.dropped = append(res.dropped, fmt.Sprintf("work item %q: status %q read as wip", clip(item.Goal, 40), pl.resp.Status))
			item.Status = index.WorkItemWIP
		}
		if pl.isOpen {
			item.ID = in.open.ID
			item.StartStepID = in.open.StartStepID
			item.StartTS = in.open.StartTS
			item.ContinuesWorkItemID = in.open.ContinuesWorkItemID
		} else {
			first := turns[0]
			item.ID = workItemID(in.sessionID, first.view.Turn)
			item.StartStepID = first.view.Step
			item.StartTS = first.ts
		}
		if c := strings.TrimSpace(pl.resp.ContinuesWorkItemID); c != "" {
			if in.resumable[c] {
				item.ContinuesWorkItemID = c
			} else {
				res.dropped = append(res.dropped, fmt.Sprintf("work item %q continues unknown %q; ignored", clip(item.Goal, 40), c))
			}
		}
		last := turns[len(turns)-1]
		isLast := k == len(plan)-1
		if item.Status != index.WorkItemWIP || !isLast {
			item.EndTS = last.ts
			item.EndStepID = last.view.Step
		}

		write := index.WorkItemWrite{Item: item}
		fileSet := map[string]bool{}
		for i := pl.start; i < end; i++ {
			t := in.turns[i]
			if t.view.Step != "" {
				write.StepIDs = append(write.StepIDs, t.view.Step)
			}
			for _, f := range t.view.Files {
				if !fileSet[f] {
					fileSet[f] = true
					write.Files = append(write.Files, f)
				}
			}
			for _, e := range deterministicByTurn[i] {
				write.Links = append(write.Links, linkOf(e, index.LinkSourceDeterministic))
			}
		}

		for _, e := range pl.resp.Entities {
			e.Type = index.NormalizeEntityType(e.Type)
			e.Name = strings.TrimSpace(e.Name)
			if e.Type == "" || e.Name == "" {
				res.dropped = append(res.dropped, "entity without type or name")
				continue
			}
			if e.EvidenceStepID == "" {
				res.dropped = append(res.dropped, fmt.Sprintf("entity %s:%s has no evidence", e.Type, e.Name))
				continue
			}
			if _, ok := position[e.EvidenceStepID]; !ok {
				res.dropped = append(res.dropped, fmt.Sprintf("entity %s:%s cites unknown evidence %q", e.Type, e.Name, e.EvidenceStepID))
				continue
			}
			write.Links = append(write.Links, linkOf(e, index.LinkSourceModel))
		}
		res.items = append(res.items, write)
	}
	if len(res.items) == 0 {
		return res, fmt.Errorf("reply produced no usable work item")
	}
	return res, nil
}

func linkOf(e EntityView, source string) index.LinkWrite {
	role := strings.ToLower(strings.TrimSpace(e.Role))
	switch role {
	case "goal", "touched", "produced", "referenced", "blocked_by":
	default:
		role = "referenced"
	}
	conf := e.Confidence
	if conf <= 0 || conf > 1 {
		conf = 1
		if source == index.LinkSourceModel {
			conf = 0.5
		}
	}
	return index.LinkWrite{
		Type: index.NormalizeEntityType(e.Type), Name: e.Name, Ref: strings.TrimSpace(e.Ref),
		Role: role, Confidence: conf, Source: source, EvidenceStepID: e.EvidenceStepID,
	}
}

func mergeResponses(a, b WorkItemResponse) WorkItemResponse {
	if strings.TrimSpace(a.Goal) == "" {
		a.Goal = b.Goal
	}
	a.Approach = strings.TrimSpace(joinText(a.Approach, b.Approach))
	if strings.TrimSpace(b.Outcome) != "" {
		a.Outcome = b.Outcome
	}
	if b.Status != "" {
		a.Status = b.Status
	}
	a.Entities = append(a.Entities, b.Entities...)
	if a.ContinuesWorkItemID == "" {
		a.ContinuesWorkItemID = b.ContinuesWorkItemID
	}
	return a
}

// workItemID is deterministic in (session, first turn) so a job retried
// after a crash rewrites the same row instead of adding a twin.
func workItemID(sessionID, firstTurn string) string {
	return "wi_" + string(store.HashBytes([]byte(sessionID + "\x00" + firstTurn)))[:24]
}

// embeddingText is what gets embedded for a work item.
func embeddingText(w index.WorkItemWrite) string {
	var names []string
	for _, l := range w.Links {
		names = append(names, l.Name)
	}
	parts := []string{w.Item.Goal, w.Item.Approach, w.Item.Outcome}
	if len(names) > 0 {
		parts = append(parts, strings.Join(names, ", "))
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
