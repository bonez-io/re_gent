package server

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/regent-vcs/regent/internal/store"
)

// defaultLogLimit bounds a /api/log walk when the caller does not specify one.
const defaultLogLimit = 50

// maxSessionWalk bounds how far the sessions list walks a single session's
// parent chain. A session's whole history is walked to count steps and find the
// earliest step's first prompt; the bound is a guard against a pathologically
// long or cyclic chain rather than an expected limit.
const maxSessionWalk = 100000

// MessageJSON is one conversation turn in the shape the viewer already parses:
// a flat {type, message:{role, content}} record.
type MessageJSON struct {
	Type    string      `json:"type"`
	Message MessageBody `json:"message"`
}

// MessageBody is the inner {role, content} of a MessageJSON.
type MessageBody struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// conversationEntry mirrors the on-disk conversation blob shape written by the
// capture engine: a JSON array of {type, text, ts} records.
type conversationEntry struct {
	Type string `json:"type"`
	Text string `json:"text"`
	TS   int64  `json:"ts,omitempty"`
}

// sessionSummary is one entry in the GET /api/sessions response.
type sessionSummary struct {
	SessionID    string `json:"session_id"`
	AgentID      string `json:"agent_id"`
	StepCount    int    `json:"step_count"`
	LastActivity string `json:"last_activity"`
	Title        string `json:"title"`
}

// sessionsResponse is the GET /api/sessions envelope.
type sessionsResponse struct {
	TotalSessions int              `json:"total_sessions"`
	Sessions      []sessionSummary `json:"sessions"`
}

// logCauseJSON is one tool call inside a log step.
type logCauseJSON struct {
	Tool      string      `json:"tool"`
	ToolUseID string      `json:"tool_use_id"`
	Args      interface{} `json:"args"`
	Result    interface{} `json:"result"`
}

// logStepJSON is one step in the GET /api/log response. It matches the viewer's
// existing LogStep shape.
type logStepJSON struct {
	Hash      string         `json:"hash"`
	Timestamp string         `json:"timestamp"`
	Origin    string         `json:"origin"`
	Parent    string         `json:"parent"`
	Tool      string         `json:"tool"`
	ToolUseID string         `json:"tool_use_id"`
	Causes    []logCauseJSON `json:"causes"`
	Files     []string       `json:"files"`
	Args      interface{}    `json:"args"`
	Result    interface{}    `json:"result"`
	Messages  []MessageJSON  `json:"messages"`
}

// logResponse is the GET /api/log envelope.
type logResponse struct {
	SessionID string        `json:"session_id"`
	Steps     []logStepJSON `json:"steps"`
}

// handleAPI routes the repo-scoped reconstruction endpoints. It runs after the
// auth gate in ServeHTTP, so every path here is token-protected exactly like
// /objects and /refs. segs is the full path split; segs[0] is the repo id and
// segs[1] == "api".
func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request, repoID string, segs []string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}

	// Reads never bring a repo into existence: openRepo(create=false) returns
	// errRepoNotFound, which we surface as a 404 like the object/ref handlers.
	st, err := s.openRepo(repoID, false)
	if err != nil {
		if errors.Is(err, errRepoNotFound) {
			httpError(w, http.StatusNotFound, "unknown repo "+repoID)
			return
		}
		s.logf("open repo %q: %v", repoID, err)
		httpError(w, http.StatusInternalServerError, "open repo failed")
		return
	}

	switch {
	case len(segs) == 3 && segs[2] == "sessions":
		s.handleAPISessions(w, r, repoID, st)
	case len(segs) == 3 && segs[2] == "log":
		s.handleAPILog(w, r, repoID, st)
	default:
		httpError(w, http.StatusNotFound, "not found")
	}
}

// handleAPISessions reconstructs the session list from the object store: every
// ref under sessions/, its tip step's metadata, a full step count, and a title
// taken from the earliest step's first user prompt.
func (s *Server) handleAPISessions(w http.ResponseWriter, r *http.Request, repoID string, st *store.Store) {
	refs, err := st.ListRefs("sessions")
	if err != nil {
		s.logf("list session refs in %s: %v", repoID, err)
		httpError(w, http.StatusInternalServerError, "list sessions failed")
		return
	}

	// A ref name may contain OS path separators when it came off disk; the
	// session id the viewer round-trips is always slash-joined.
	sessionIDs := make([]string, 0, len(refs))
	tips := make(map[string]store.Hash, len(refs))
	for name, hash := range refs {
		id := toSlash(name)
		sessionIDs = append(sessionIDs, id)
		tips[id] = hash
	}
	sort.Strings(sessionIDs)

	out := sessionsResponse{Sessions: make([]sessionSummary, 0, len(sessionIDs))}
	for _, id := range sessionIDs {
		summary, ok := s.summarizeSession(st, repoID, id, tips[id])
		if !ok {
			// A ref that points at an unreadable or missing tip is skipped
			// rather than failing the whole list (old data, partial push).
			continue
		}
		out.Sessions = append(out.Sessions, summary)
	}
	out.TotalSessions = len(out.Sessions)

	writeJSON(w, http.StatusOK, out)
}

// summarizeSession walks one session's chain to build its list entry. It
// returns ok=false only when the tip step itself cannot be read, since without
// it there is no metadata to report.
func (s *Server) summarizeSession(st *store.Store, repoID, sessionID string, tip store.Hash) (sessionSummary, bool) {
	steps, _, err := walkSession(st, tip, maxSessionWalk)
	if err != nil {
		s.logf("walk session %s in %s: %v", sessionID, repoID, err)
	}
	if len(steps) == 0 {
		return sessionSummary{}, false
	}

	tipStep := steps[0]
	summary := sessionSummary{
		SessionID:    sessionID,
		AgentID:      tipStep.Origin,
		StepCount:    len(steps),
		LastActivity: rfc3339FromNanos(tipStep.TimestampNanos),
		Title:        s.firstUserPrompt(st, repoID, steps[len(steps)-1]),
	}
	return summary, true
}

// firstUserPrompt reads a step's conversation blob and returns the text of its
// first user entry, or "" when there is none. Missing or malformed blobs yield
// "" rather than an error so one bad session never breaks the list.
func (s *Server) firstUserPrompt(st *store.Store, repoID string, step *store.Step) string {
	if step == nil || step.Conversation == "" {
		return ""
	}
	for _, m := range s.conversationEntries(st, repoID, step.Conversation) {
		if m.Type == "user" {
			return m.Text
		}
	}
	return ""
}

// handleAPILog reconstructs a session's step history newest-first from the
// object store and emits it in the viewer's LogStep shape.
func (s *Server) handleAPILog(w http.ResponseWriter, r *http.Request, repoID string, st *store.Store) {
	session := r.URL.Query().Get("session")
	if session == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing session parameter"})
		return
	}

	limit := defaultLogLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	// Bound the walk so a single request cannot materialize an unbounded
	// history (and read every step's blobs) into one response.
	if limit > maxSessionWalk {
		limit = maxSessionWalk
	}

	refName := "sessions/" + session
	if err := validateRefName(refName); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	tip, err := st.ReadRef(refName)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// A JSON error the viewer can render, rather than a bare 404.
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such session"})
			return
		}
		s.logf("read session ref %s in %s: %v", refName, repoID, err)
		httpError(w, http.StatusInternalServerError, "read session failed")
		return
	}

	steps, hashes, err := walkSession(st, tip, limit)
	if err != nil {
		s.logf("walk session %s in %s: %v", session, repoID, err)
	}

	resp := logResponse{SessionID: session, Steps: make([]logStepJSON, 0, len(steps))}
	for i, step := range steps {
		resp.Steps = append(resp.Steps, s.stepToJSON(st, repoID, hashes[i], step))
	}

	writeJSON(w, http.StatusOK, resp)
}

// walkSession follows Parent pointers from tip, newest-first, stopping at an
// empty parent, at limit steps, or at a step that cannot be read. It returns the
// steps and their hashes in parallel; hashes[i] is the address of steps[i],
// which the Step struct itself does not carry.
func walkSession(s *store.Store, tip store.Hash, limit int) ([]*store.Step, []store.Hash, error) {
	var steps []*store.Step
	var hashes []store.Hash
	seen := make(map[store.Hash]bool)

	h := tip
	for h != "" && len(steps) < limit {
		if seen[h] {
			// Defensive: a malformed parent cycle must terminate the walk.
			break
		}
		seen[h] = true

		step, err := s.ReadStep(h)
		if err != nil {
			// A missing or corrupt step ends the walk with what we have so far
			// instead of failing the whole request.
			return steps, hashes, err
		}
		steps = append(steps, step)
		hashes = append(hashes, h)
		h = step.Parent
	}
	return steps, hashes, nil
}

// stepToJSON assembles one step's causes, args/results, files, and messages
// into the viewer's LogStep shape. Every referenced blob is read defensively:
// a missing one is skipped, never fatal.
func (s *Server) stepToJSON(st *store.Store, repoID string, hash store.Hash, step *store.Step) logStepJSON {
	step.NormalizeCauses()

	js := logStepJSON{
		Hash:      string(hash),
		Timestamp: rfc3339FromNanos(step.TimestampNanos),
		Origin:    step.Origin,
		Parent:    string(step.Parent),
		Causes:    make([]logCauseJSON, 0, len(step.Causes)),
		Files:     []string{},
		Messages:  s.conversationMessages(st, repoID, step.Conversation),
	}

	seenFile := make(map[string]bool)
	for _, cause := range step.Causes {
		args := s.readJSONBlob(st, repoID, cause.ArgsBlob)
		result := s.readJSONBlob(st, repoID, cause.ResultBlob)

		js.Causes = append(js.Causes, logCauseJSON{
			Tool:      cause.ToolName,
			ToolUseID: cause.ToolUseID,
			Args:      args,
			Result:    result,
		})

		if fp := filePathOf(args); fp != "" && !seenFile[fp] {
			seenFile[fp] = true
			js.Files = append(js.Files, fp)
		}
	}

	if len(step.Causes) > 0 {
		js.Tool = step.Causes[0].ToolName
		js.ToolUseID = step.Causes[0].ToolUseID
		js.Args = js.Causes[0].Args
		js.Result = js.Causes[0].Result
	}

	return js
}

// conversationMessages reads a step's conversation blob and maps each entry to
// the viewer's flat {type, message:{role, content}} record. It tolerates an
// empty, missing, or invalid blob by returning an empty (non-nil) slice, so the
// JSON always carries a "messages" array.
func (s *Server) conversationMessages(st *store.Store, repoID string, convHash store.Hash) []MessageJSON {
	out := []MessageJSON{}
	for _, e := range s.conversationEntries(st, repoID, convHash) {
		out = append(out, MessageJSON{
			Type:    e.Type,
			Message: MessageBody{Role: e.Type, Content: e.Text},
		})
	}
	return out
}

// conversationEntries reads and decodes a conversation blob, returning nil for
// an empty hash, an unreadable blob, or malformed JSON.
func (s *Server) conversationEntries(st *store.Store, repoID string, convHash store.Hash) []conversationEntry {
	if convHash == "" {
		return nil
	}
	data, err := st.ReadBlob(convHash)
	if err != nil {
		s.logf("read conversation blob %s in %s: %v", convHash, repoID, err)
		return nil
	}
	var entries []conversationEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		s.logf("decode conversation blob %s in %s: %v", convHash, repoID, err)
		return nil
	}
	return entries
}

// readJSONBlob reads a blob and returns it parsed as arbitrary JSON. When the
// bytes are not valid JSON the raw string is returned so nothing is lost, and a
// missing blob (empty hash or read error) yields nil.
func (s *Server) readJSONBlob(st *store.Store, repoID string, h store.Hash) interface{} {
	if h == "" {
		return nil
	}
	data, err := st.ReadBlob(h)
	if err != nil {
		s.logf("read blob %s in %s: %v", h, repoID, err)
		return nil
	}
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return string(data)
	}
	return v
}

// filePathOf extracts a string "file_path" field from parsed tool args, or ""
// when args is not an object or has no such field.
func filePathOf(args interface{}) string {
	m, ok := args.(map[string]interface{})
	if !ok {
		return ""
	}
	fp, ok := m["file_path"].(string)
	if !ok {
		return ""
	}
	return fp
}

// rfc3339FromNanos formats a Unix-nanosecond timestamp as RFC3339, or "" when
// the timestamp is zero (unset).
func rfc3339FromNanos(nanos int64) string {
	if nanos == 0 {
		return ""
	}
	return time.Unix(0, nanos).UTC().Format(time.RFC3339)
}

// toSlash converts any OS path separators in a ref name to forward slashes.
func toSlash(name string) string {
	return strings.ReplaceAll(name, "\\", "/")
}
