package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/store"
)

// perFileHunkLimit bounds one file's rendered diff, so a single generated
// file cannot eat the whole budget.
const perFileHunkLimit = 6000

// turn is one turn of a session as gathered from the index, before it is
// shaped for the model.
type turn struct {
	view    TurnView
	ts      time.Time
	lastSeq int
	// deterministic entities found in this turn's text and tool I/O
	entities []EntityView
	// pendingGit maps a shell tool call id to its command, so the matching
	// result can be scanned for what git reported.
	pendingGit map[string]string
}

// batch is what one read call sees.
type batch struct {
	turns   []turn
	lastSeq int
	// more is true when messages remain after this batch (budget cut).
	more bool
}

// gatherTurns reads the session's messages after cursor, groups them by
// turn, attaches step diffs, and stops when the budget is spent.
func (p *Processor) gatherTurns(ctx context.Context, sessionID string, cursor int, budgetBytes int) (batch, error) {
	messages, err := p.Index.ListSessionMessages(sessionID, cursor, 0)
	if err != nil {
		return batch{}, fmt.Errorf("list messages: %w", err)
	}
	if len(messages) == 0 {
		return batch{}, nil
	}

	// Group by turn, preserving first-seen order.
	var order []string
	byTurn := map[string]*turn{}
	for _, m := range messages {
		key := m.TurnID
		if key == "" {
			key = "seq-" + fmt.Sprint(m.SeqNum)
		}
		t, ok := byTurn[key]
		if !ok {
			t = &turn{view: TurnView{Turn: key}, ts: time.Unix(0, m.Timestamp)}
			byTurn[key] = t
			order = append(order, key)
		}
		if m.SeqNum > t.lastSeq {
			t.lastSeq = m.SeqNum
		}
		if t.view.Step == "" && m.StepID != "" {
			t.view.Step = m.StepID
		}
		evidence := t.view.Step
		if evidence == "" {
			evidence = key
		}
		switch m.MessageType {
		case "user":
			t.view.User = joinText(t.view.User, m.ContentText)
			t.entities = append(t.entities, extractURLs(m.ContentText, evidence)...)
		case "assistant":
			t.view.Assistant = joinText(t.view.Assistant, m.ContentText)
			t.entities = append(t.entities, extractURLs(m.ContentText, evidence)...)
		case "reasoning":
			t.view.Reasoning = joinText(t.view.Reasoning, m.ContentText)
		case "tool_call":
			if m.ToolName != "" && !contains(t.view.Tools, m.ToolName) {
				t.view.Tools = append(t.view.Tools, m.ToolName)
			}
			if isShellTool(m.ToolName) && m.ToolInput != "" {
				cmd := p.shellCommand(m.ToolInput)
				t.entities = append(t.entities, extractURLs(cmd, evidence)...)
				byTurn[key].pendingGit = appendGitCommand(byTurn[key].pendingGit, m.ToolUseID, cmd)
			}
		case "tool_result":
			if isShellTool(m.ToolName) && m.ToolOutput != "" {
				if cmd, ok := byTurn[key].pendingGit[m.ToolUseID]; ok {
					out := p.blobText(m.ToolOutput, 20000)
					t.entities = append(t.entities, extractGit(cmd, out, evidence)...)
					t.entities = append(t.entities, extractURLs(out, evidence)...)
				}
			}
		}
	}

	// Step-derived files and hunks, and step evidence for turns whose
	// messages were not linked to their step.
	for _, key := range order {
		t := byTurn[key]
		if t.view.Step == "" {
			if h, ok, err := p.Index.StepForTurn(sessionID, key); err == nil && ok {
				t.view.Step = string(h)
			}
		}
		if t.view.Step == "" {
			continue
		}
		step, ok, err := p.Index.GetStepSummary(t.view.Step)
		if err != nil || !ok {
			continue
		}
		t.ts = step.Timestamp
		files, hunks, err := stepChanges(p.Store, step.ParentID, step.ID, perFileHunkLimit)
		if err != nil {
			p.logf("step %s: %v", step.ID, err)
			continue
		}
		sort.Strings(files)
		t.view.Files = files
		t.view.Hunks = hunks
	}

	// Budget: shape the batch to fit. Turns are added oldest first; when the
	// next turn would overflow, hunks go first, then reasoning, then the
	// batch closes and the rest waits for the next call.
	var out batch
	used := 0
	for i, key := range order {
		t := byTurn[key]
		t.view.At = t.ts.UTC().Format(time.RFC3339)
		size := turnSize(t.view)
		if used+size > budgetBytes && len(out.turns) > 0 {
			out.more = true
			break
		}
		if used+size > budgetBytes {
			// A single turn over budget: trim it rather than skip it.
			t.view.Hunks = nil
			if used+turnSize(t.view) > budgetBytes {
				t.view.Reasoning = ""
			}
			if used+turnSize(t.view) > budgetBytes {
				t.view.Assistant = clip(t.view.Assistant, budgetBytes/2)
				t.view.User = clip(t.view.User, budgetBytes/4)
			}
			size = turnSize(t.view)
		}
		used += size
		out.turns = append(out.turns, *t)
		out.lastSeq = t.lastSeq
		if i == len(order)-1 {
			out.more = false
		}
	}
	return out, nil
}

func turnSize(v TurnView) int {
	b, _ := json.Marshal(v)
	return len(b)
}

func clip(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func joinText(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "\n\n" + b
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func isShellTool(name string) bool {
	switch strings.ToLower(name) {
	case "bash", "shell", "execute", "run_command", "terminal", "exec", "local_shell":
		return true
	}
	return false
}

// shellCommand pulls the command string out of a stored tool input blob.
// Hosts differ in the field name; the common ones are tried in order.
func (p *Processor) shellCommand(blobHash string) string {
	raw := p.blobText(blobHash, 20000)
	var in map[string]any
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return raw
	}
	for _, k := range []string{"command", "cmd", "script", "input"} {
		switch v := in[k].(type) {
		case string:
			return v
		case []any:
			var parts []string
			for _, x := range v {
				if s, ok := x.(string); ok {
					parts = append(parts, s)
				}
			}
			return strings.Join(parts, " ")
		}
	}
	return raw
}

// blobText reads a content-addressed blob as text, cut at limit bytes.
func (p *Processor) blobText(hash string, limit int) string {
	data, err := p.Store.ReadBlob(store.Hash(hash))
	if err != nil {
		return ""
	}
	if limit > 0 && len(data) > limit {
		data = data[:limit]
	}
	// Tool results are often JSON-wrapped strings; unwrap one level so git
	// output is scanned as text.
	var s string
	if json.Unmarshal(data, &s) == nil {
		return s
	}
	var obj map[string]any
	if json.Unmarshal(data, &obj) == nil {
		for _, k := range []string{"stdout", "output", "content", "result", "text"} {
			if v, ok := obj[k].(string); ok {
				return v
			}
		}
	}
	return string(data)
}

func appendGitCommand(m map[string]string, toolUseID, cmd string) map[string]string {
	if m == nil {
		m = map[string]string{}
	}
	m[toolUseID] = cmd
	return m
}

// gitRemotes returns the workspace's git remote URLs, cached per processor.
func (p *Processor) gitRemotes(ctx context.Context) []string {
	p.remotesOnce.Do(func() {
		workspace := filepath.Dir(p.Store.Root)
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "git", "-C", workspace, "remote", "-v").Output()
		if err != nil {
			return
		}
		seen := map[string]bool{}
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 || seen[fields[1]] {
				continue
			}
			seen[fields[1]] = true
			p.remotes = append(p.remotes, fields[1])
		}
	})
	return p.remotes
}

// viewOf shapes a stored work item for the request.
func (p *Processor) viewOf(w index.WorkItem) (WorkItemView, error) {
	links, err := p.Index.WorkItemLinks(w.ID)
	if err != nil {
		return WorkItemView{}, err
	}
	v := WorkItemView{ID: w.ID, Goal: w.Goal, Approach: w.Approach, Outcome: w.Outcome, Status: w.Status}
	for _, l := range links {
		v.Entities = append(v.Entities, EntityView{Type: l.Type, Name: l.Name, Ref: l.Ref, Role: l.Role, Confidence: l.Confidence, EvidenceStepID: l.EvidenceStepID})
	}
	return v, nil
}
