package server

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/bonez-io/re_gent/internal/store"
)

// GET /{repoId}/api/feed powers the interactive first-run tutorial (issue
// #107): the tutorial page long-polls "which steps landed in this project
// since my cursor" so it can light up each stage as an agent works, without
// the UI reimplementing the walk-every-session-tip logic itself. The pattern
// — long-poll with an opaque cursor, bounded wait, cheap repeated polling —
// copies selfhosted's connections feed (handleOrgConnections in
// selfhosted/handlers_onboarding.go).
const (
	// feedMaxSteps bounds how many steps a single response can carry, so a
	// burst of concurrent sessions cannot make one poll unbounded.
	feedMaxSteps = 100
	// feedMaxFiles bounds how many changed paths one step reports.
	feedMaxFiles = 50
	// feedPromptChars bounds how much of the user prompt is echoed back.
	feedPromptChars = 200
	// feedDefaultTimeout and feedMaxTimeout bound the long-poll wait per the
	// contract: default 20s, capped at 25s regardless of what the caller asks
	// for.
	feedDefaultTimeout = 20 * time.Second
	feedMaxTimeout     = 25 * time.Second
	// feedPollInterval is how often a long-poll re-checks for new steps.
	feedPollInterval = 500 * time.Millisecond
)

// feedStepJSON is one entry in the GET /api/feed response.
type feedStepJSON struct {
	Hash      string   `json:"hash"`
	SessionID string   `json:"session_id"`
	Origin    string   `json:"origin"`
	TurnID    string   `json:"turn_id"`
	Timestamp string   `json:"timestamp"`
	Files     []string `json:"files"`
	Prompt    string   `json:"prompt"`
}

// feedResponse is the GET /api/feed envelope.
type feedResponse struct {
	Cursor string         `json:"cursor"`
	Steps  []feedStepJSON `json:"steps"`
}

// handleAPIFeed serves the tutorial's long-poll. Cursor design: the cursor is
// the decimal string form of a step's TimestampNanos — specifically the
// maximum TimestampNanos among every step a response has ever returned to
// this cursor's lineage. A bare timestamp cannot alone distinguish "nothing
// happened after this instant" from "something happened in the same instant
// and was already returned," so every response is also deduped by step hash
// within itself (feedStepsSince never emits the same hash twice in one
// answer) before the cursor advances. Two different sessions can in
// principle record a step in the same nanosecond; that only risks one wasted
// re-walk past the boundary on the next poll (a step at exactly the cursor's
// timestamp is excluded and would be re-examined then discarded), never a
// missed or duplicated step in what the client sees, because each poll walks
// every session tip fresh rather than trusting a resumable position.
func (s *Server) handleAPIFeed(w http.ResponseWriter, r *http.Request, repoID string, st *store.Store) {
	since, hasSince := parseFeedCursor(r.URL.Query().Get("since"))

	if !hasSince {
		// No since: report the current cursor immediately with no steps, so
		// the client starts watching from now.
		cursor, err := s.feedCurrentCursor(st)
		if err != nil {
			s.logf("feed cursor in %s: %v", repoID, err)
			httpError(w, http.StatusInternalServerError, "read feed failed")
			return
		}
		writeJSON(w, http.StatusOK, feedResponse{Cursor: strconv.FormatInt(cursor, 10), Steps: []feedStepJSON{}})
		return
	}

	deadline := time.Now().Add(feedTimeout(r.URL.Query().Get("timeout")))
	for {
		steps, cursor, err := s.feedStepsSince(st, repoID, since)
		if err != nil {
			s.logf("feed walk in %s: %v", repoID, err)
			httpError(w, http.StatusInternalServerError, "read feed failed")
			return
		}
		if len(steps) > 0 || time.Now().After(deadline) {
			writeJSON(w, http.StatusOK, feedResponse{Cursor: strconv.FormatInt(cursor, 10), Steps: steps})
			return
		}
		select {
		case <-r.Context().Done():
			// The client hung up mid-wait; report the unchanged cursor rather
			// than racing to write a response nobody reads.
			writeJSON(w, http.StatusOK, feedResponse{Cursor: strconv.FormatInt(since, 10), Steps: []feedStepJSON{}})
			return
		case <-time.After(feedPollInterval):
		}
	}
}

// parseFeedCursor decodes the opaque "since" query parameter. Anything that
// fails to parse — missing, empty, malformed, or from some future cursor
// format — is treated as "no since" rather than a 400: a stale or corrupted
// client cursor should fall back to "start watching from now," matching the
// no-since behavior, not hard-fail the tutorial's poll.
func parseFeedCursor(raw string) (int64, bool) {
	if raw == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// feedTimeout clamps the caller's requested wait to [default, max]: an
// absent or invalid value falls back to feedDefaultTimeout, and anything
// above feedMaxTimeout is capped there.
func feedTimeout(raw string) time.Duration {
	timeout := feedDefaultTimeout
	if raw != "" {
		if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
			timeout = time.Duration(secs) * time.Second
		}
	}
	if timeout > feedMaxTimeout {
		timeout = feedMaxTimeout
	}
	return timeout
}

// feedCurrentCursor returns the newest TimestampNanos across every session
// and sync ref tip, without walking any history — used only for the
// immediate no-since response, where the contract asks for "the current
// cursor" and nothing else.
func (s *Server) feedCurrentCursor(st *store.Store) (int64, error) {
	var max int64
	scan := func(dir string) error {
		refs, err := st.ListRefs(dir)
		if err != nil {
			return err
		}
		for _, tip := range refs {
			step, err := st.ReadStep(tip)
			if err != nil {
				// An unreadable tip contributes nothing to the cursor rather
				// than failing the whole request.
				continue
			}
			if step.TimestampNanos > max {
				max = step.TimestampNanos
			}
		}
		return nil
	}
	if err := scan("sessions"); err != nil {
		return 0, err
	}
	// refs/sync/* holds steps another agent's sync worker records (Origin:
	// "sync") outside any session ref. The directory may not exist yet;
	// ListRefs already tolerates that (returns an empty map, nil error), so
	// this is a plain best-effort inclusion, not a dependency on that code.
	if err := scan("sync"); err != nil {
		return 0, err
	}
	return max, nil
}

// feedStepsSince walks every refs/sessions/* and refs/sync/* tip backward
// through Parent pointers, collecting steps newer than since (strictly
// greater TimestampNanos), oldest first, capped at feedMaxSteps. A branch's
// walk stops the moment it reaches a step at or before since, since steps
// only get older walking toward the root and anything at-or-before the
// cursor was already surfaced by an earlier poll. Hashes are deduped across
// branches (a session and a sync tip should never share steps, but nothing
// here depends on that).
func (s *Server) feedStepsSince(st *store.Store, repoID string, since int64) ([]feedStepJSON, int64, error) {
	type walked struct {
		hash store.Hash
		step *store.Step
	}
	var all []walked
	seen := make(map[store.Hash]bool)

	collect := func(dir string) error {
		refs, err := st.ListRefs(dir)
		if err != nil {
			return err
		}
		for _, tip := range refs {
			h := tip
			for h != "" {
				if seen[h] {
					break
				}
				step, err := st.ReadStep(h)
				if err != nil {
					// A missing or corrupt step ends this branch's walk with
					// whatever was already collected, rather than failing
					// the whole feed.
					break
				}
				if step.TimestampNanos <= since {
					break
				}
				seen[h] = true
				all = append(all, walked{hash: h, step: step})
				h = step.Parent
			}
		}
		return nil
	}
	if err := collect("sessions"); err != nil {
		return nil, since, err
	}
	if err := collect("sync"); err != nil {
		return nil, since, err
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].step.TimestampNanos == all[j].step.TimestampNanos {
			return all[i].hash < all[j].hash
		}
		return all[i].step.TimestampNanos < all[j].step.TimestampNanos
	})
	if len(all) > feedMaxSteps {
		all = all[:feedMaxSteps]
	}

	cursor := since
	out := make([]feedStepJSON, 0, len(all))
	for _, w := range all {
		out = append(out, s.feedStepJSON(st, repoID, w.hash, w.step))
		if w.step.TimestampNanos > cursor {
			cursor = w.step.TimestampNanos
		}
	}
	return out, cursor, nil
}

// feedStepJSON builds one feed entry: the tree delta between the step and
// its parent (changedFiles, already used by /api/log) and the user prompt
// that produced the step's turn (firstUserPrompt, already used by
// /api/sessions' title). Both are best-effort — a missing conversation or
// unreadable tree yields an empty result, never an error, so one bad step
// never breaks the feed.
func (s *Server) feedStepJSON(st *store.Store, repoID string, hash store.Hash, step *store.Step) feedStepJSON {
	files := changedFiles(st, step)
	if len(files) > feedMaxFiles {
		files = files[:feedMaxFiles]
	}
	if files == nil {
		files = []string{}
	}
	return feedStepJSON{
		Hash:      string(hash),
		SessionID: step.SessionID,
		Origin:    step.Origin,
		TurnID:    step.TurnID,
		Timestamp: rfc3339FromNanos(step.TimestampNanos),
		Files:     files,
		Prompt:    truncatePrompt(s.firstUserPrompt(st, repoID, step), feedPromptChars),
	}
}

// truncatePrompt returns the first max runes of prompt, so a pasted essay in
// a user turn does not balloon the feed response.
func truncatePrompt(prompt string, max int) string {
	r := []rune(prompt)
	if len(r) <= max {
		return prompt
	}
	return string(r[:max])
}
