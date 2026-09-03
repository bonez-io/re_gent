package server

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bonez-io/re_gent/internal/store"
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
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	TS         int64  `json:"ts,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	ToolUseID  string `json:"tool_use_id,omitempty"`
	ToolInput  string `json:"tool_input,omitempty"`
	ToolOutput string `json:"tool_output,omitempty"`
}

// authorJSON is the human who initiated a session, surfaced so the viewer can
// attribute each session in a shared team timeline. Omitted entirely when the
// session's steps carry no author (older data, or a host with no git identity).
type authorJSON struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

// sessionSummary is one entry in the GET /api/sessions response.
type sessionSummary struct {
	SessionID    string      `json:"session_id"`
	AgentID      string      `json:"agent_id"`
	Origin       string      `json:"origin"`
	HeadStep     string      `json:"head_step"`
	StepCount    int         `json:"step_count"`
	StartedAt    string      `json:"started_at"`
	LastActivity string      `json:"last_activity"`
	Title        string      `json:"title"`
	Author       *authorJSON `json:"author,omitempty"`

	lastActivityNanos int64
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
	Hash            string         `json:"hash"`
	Timestamp       string         `json:"timestamp"`
	Origin          string         `json:"origin"`
	SessionID       string         `json:"session_id"`
	TurnID          string         `json:"turn_id,omitempty"`
	AgentID         string         `json:"agent_id,omitempty"`
	Parent          string         `json:"parent"`
	SecondaryParent string         `json:"secondary_parent,omitempty"`
	Tree            string         `json:"tree"`
	Tool            string         `json:"tool"`
	ToolUseID       string         `json:"tool_use_id"`
	Causes          []logCauseJSON `json:"causes"`
	Files           []string       `json:"files"`
	Args            interface{}    `json:"args"`
	Result          interface{}    `json:"result"`
	Messages        []MessageJSON  `json:"messages"`
	Events          []eventJSON    `json:"events"`
	Author          *authorJSON    `json:"author,omitempty"`
	Usage           *store.Usage   `json:"usage,omitempty"`
	Effects         []store.Effect `json:"effects"`
}

// eventJSON preserves the chronological, normalized transcript stored with a
// step. Tool payload hashes are resolved for the viewer while the underlying
// content-addressed blobs remain canonical.
type eventJSON struct {
	Type      string      `json:"type"`
	Timestamp string      `json:"timestamp,omitempty"`
	Text      string      `json:"text,omitempty"`
	ToolName  string      `json:"tool_name,omitempty"`
	ToolUseID string      `json:"tool_use_id,omitempty"`
	Input     interface{} `json:"input,omitempty"`
	Output    interface{} `json:"output,omitempty"`
}

// logResponse is the GET /api/log envelope.
type logResponse struct {
	SessionID string        `json:"session_id"`
	Steps     []logStepJSON `json:"steps"`
}

type transcriptResponse struct {
	Session sessionSummary `json:"session"`
	Steps   []logStepJSON  `json:"steps"`
}

type repositoryStatus struct {
	Status  string `json:"status"`
	Service struct {
		Name       string `json:"name"`
		APIVersion string `json:"api_version"`
	} `json:"service"`
	Repository struct {
		ID           string `json:"id"`
		SessionCount int    `json:"session_count"`
		RefCount     int    `json:"ref_count"`
		ObjectCount  int    `json:"object_count"`
		LatestStep   string `json:"latest_step,omitempty"`
		LastActivity string `json:"last_activity,omitempty"`
	} `json:"repository"`
}

type fileJSON struct {
	Path     string `json:"path"`
	BlobHash string `json:"blob_hash"`
	Mode     uint32 `json:"mode"`
	Size     int    `json:"size"`
}

type filesResponse struct {
	StepHash   string     `json:"step_hash"`
	TreeHash   string     `json:"tree_hash"`
	TotalFiles int        `json:"total_files"`
	Files      []fileJSON `json:"files"`
}

type blameLineJSON struct {
	Number    int    `json:"number"`
	Content   string `json:"content"`
	StepHash  string `json:"step_hash,omitempty"`
	Origin    string `json:"origin,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

type blameResponse struct {
	StepHash string          `json:"step_hash"`
	Path     string          `json:"path"`
	BlobHash string          `json:"blob_hash"`
	Lines    []blameLineJSON `json:"lines"`
}

// handleAPI routes the repo-scoped reconstruction endpoints. The current
// self-hosted server is intentionally open, exactly like /objects and /refs.
// segs is the full path split; segs[0] is the repo id and segs[1] == "api".
func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request, repoID string, segs []string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	// Reads never bring a repo into existence: openRepoTenant(create=false)
	// returns errRepoNotFound, which we surface as a 404 like the object/ref
	// handlers.
	st, err := s.openRepoTenant(resourceTenantFromContext(r.Context()), repoID, false)
	if err != nil {
		if errors.Is(err, errRepoNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo " + repoID})
			return
		}
		s.logf("open repo %q: %v", repoID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "open repo failed"})
		return
	}

	switch {
	case len(segs) == 3 && segs[2] == "status":
		s.handleAPIStatus(w, r, repoID, st)
	case len(segs) == 3 && segs[2] == "sessions":
		s.handleAPISessions(w, r, repoID, st)
	case len(segs) == 3 && segs[2] == "log":
		s.handleAPILog(w, r, repoID, st)
	case len(segs) == 3 && segs[2] == "transcript":
		s.handleAPITranscript(w, r, repoID, st)
	case len(segs) == 4 && segs[2] == "steps":
		s.handleAPIStep(w, r, repoID, st, store.Hash(segs[3]))
	case len(segs) == 3 && segs[2] == "files":
		s.handleAPIFiles(w, r, repoID, st)
	case len(segs) == 3 && segs[2] == "blame":
		s.handleAPIBlame(w, r, repoID, st)
	case len(segs) == 3 && segs[2] == "diff":
		s.handleAPIDiff(w, r, repoID, st)
	case len(segs) == 3 && segs[2] == "feed":
		s.handleAPIFeed(w, r, repoID, st)
	case len(segs) == 5 && segs[2] == "commits" && segs[4] == "steps":
		s.handleAPICommitSteps(w, r, repoID, st, segs[3])
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// handleAPICommitSteps serves GET /{project}/api/commits/{sha}/steps: every
// step in the project whose recorded git_commit effect names sha, per RFC
// 0004's pull-request-provenance route. No hook records a git_commit effect on
// a step yet (see internal/store.Effect and internal/capture), so this always
// returns an empty list today — that is the honest answer, not a stub: the
// moment a producer starts recording Effect{Kind:"git_commit", Descriptor:sha}
// this route surfaces it with no further change here.
func (s *Server) handleAPICommitSteps(w http.ResponseWriter, r *http.Request, repoID string, st *store.Store, sha string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if sha == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing commit sha"})
		return
	}
	refs, err := st.ListRefs("sessions")
	if err != nil {
		s.logf("list session refs for commit lookup in %s: %v", repoID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list sessions failed"})
		return
	}
	matches := []logStepJSON{}
	seen := make(map[store.Hash]bool)
	for _, tip := range refs {
		steps, hashes, walkErr := walkSession(st, tip, maxSessionWalk)
		if walkErr != nil {
			s.logf("walk session for commit lookup in %s: %v", repoID, walkErr)
		}
		for i, step := range steps {
			if step == nil || seen[hashes[i]] {
				continue
			}
			seen[hashes[i]] = true
			for _, effect := range step.Effects {
				if effect.Kind == "git_commit" && effect.Descriptor == sha {
					matches = append(matches, s.stepToJSON(st, repoID, hashes[i], step))
					break
				}
			}
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Timestamp < matches[j].Timestamp })
	writeJSON(w, http.StatusOK, map[string]any{"steps": matches})
}

// handleAPISessions reconstructs the session list from the object store: every
// ref under sessions/, its tip step's metadata, a full step count, and a title
// taken from the earliest step's first user prompt.
func (s *Server) handleAPISessions(w http.ResponseWriter, r *http.Request, repoID string, st *store.Store) {
	out, err := s.listSessionSummaries(st, repoID)
	if err != nil {
		s.logf("list session refs in %s: %v", repoID, err)
		httpError(w, http.StatusInternalServerError, "list sessions failed")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listSessionSummaries(st *store.Store, repoID string) (sessionsResponse, error) {
	refs, err := st.ListRefs("sessions")
	if err != nil {
		return sessionsResponse{}, err
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
	sort.Slice(out.Sessions, func(i, j int) bool {
		if out.Sessions[i].lastActivityNanos == out.Sessions[j].lastActivityNanos {
			return out.Sessions[i].SessionID < out.Sessions[j].SessionID
		}
		return out.Sessions[i].lastActivityNanos > out.Sessions[j].lastActivityNanos
	})
	return out, nil
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
	agentID := tipStep.AgentID
	if agentID == "" {
		agentID = tipStep.Origin
	}
	summary := sessionSummary{
		SessionID:         sessionID,
		AgentID:           agentID,
		Origin:            tipStep.Origin,
		HeadStep:          string(tip),
		StepCount:         len(steps),
		StartedAt:         rfc3339FromNanos(steps[len(steps)-1].TimestampNanos),
		LastActivity:      rfc3339FromNanos(tipStep.TimestampNanos),
		Title:             s.sessionTitle(st, repoID, steps),
		Author:            sessionAuthor(steps),
		lastActivityNanos: tipStep.TimestampNanos,
	}
	return summary, true
}

func (s *Server) sessionTitle(st *store.Store, repoID string, steps []*store.Step) string {
	for i := len(steps) - 1; i >= 0; i-- {
		if title := s.firstUserPrompt(st, repoID, steps[i]); title != "" {
			return title
		}
	}
	return ""
}

// sessionAuthor returns the human who ran a session: the earliest step that
// names anyone. steps arrives newest-first from walkSession, so this walks it
// in reverse.
//
// The direction is the whole point. Reading newest-first would hand a session
// to whoever touched it last, so the name a teammate sees would change as the
// session grew. A session belongs to whoever started it.
//
// Walking forward past author-less steps rather than stopping at the very first
// one covers a session that began before a git identity was set: it reports the
// earliest person who can be named instead of reporting nobody.
//
// Returns nil when no step carries an author, which the summary then omits.
func sessionAuthor(steps []*store.Step) *authorJSON {
	for i := len(steps) - 1; i >= 0; i-- {
		step := steps[i]
		if step == nil {
			continue
		}
		if step.Author.Name != "" || step.Author.Email != "" {
			return &authorJSON{Name: step.Author.Name, Email: step.Author.Email}
		}
	}
	return nil
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

func (s *Server) handleAPIStatus(w http.ResponseWriter, _ *http.Request, repoID string, st *store.Store) {
	sessions, err := s.listSessionSummaries(st, repoID)
	if err != nil {
		s.logf("status sessions in %s: %v", repoID, err)
		httpError(w, http.StatusInternalServerError, "read repository status failed")
		return
	}
	refs, err := st.ListRefs("")
	if err != nil {
		s.logf("status refs in %s: %v", repoID, err)
		httpError(w, http.StatusInternalServerError, "read repository status failed")
		return
	}
	objectCount, err := countObjectFiles(filepath.Join(st.Root, "objects"))
	if err != nil {
		s.logf("status objects in %s: %v", repoID, err)
		httpError(w, http.StatusInternalServerError, "read repository status failed")
		return
	}

	var out repositoryStatus
	out.Status = "ok"
	out.Service.Name = "re_gent"
	out.Service.APIVersion = "1"
	out.Repository.ID = repoID
	out.Repository.SessionCount = sessions.TotalSessions
	out.Repository.RefCount = len(refs)
	out.Repository.ObjectCount = objectCount
	if len(sessions.Sessions) > 0 {
		out.Repository.LatestStep = sessions.Sessions[0].HeadStep
		out.Repository.LastActivity = sessions.Sessions[0].LastActivity
	}
	writeJSON(w, http.StatusOK, out)
}

func countObjectFiles(root string) (int, error) {
	count := 0
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			count++
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	return count, err
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

// handleAPITranscript emits a complete session oldest-first. /api/log remains
// newest-first for compatibility with existing CLI/viewer consumers.
func (s *Server) handleAPITranscript(w http.ResponseWriter, r *http.Request, repoID string, st *store.Store) {
	sessionID := r.URL.Query().Get("session")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing session parameter"})
		return
	}
	tip, err := readSessionTip(st, sessionID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such session"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	steps, hashes, walkErr := walkSession(st, tip, maxSessionWalk)
	if walkErr != nil {
		s.logf("walk transcript %s in %s: %v", sessionID, repoID, walkErr)
	}
	if len(steps) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session has no readable steps"})
		return
	}

	resp := transcriptResponse{
		Steps: make([]logStepJSON, 0, len(steps)),
	}
	resp.Session, _ = s.summarizeSession(st, repoID, sessionID, tip)
	for i := len(steps) - 1; i >= 0; i-- {
		resp.Steps = append(resp.Steps, s.stepToJSON(st, repoID, hashes[i], steps[i]))
	}
	writeJSON(w, http.StatusOK, resp)
}

func readSessionTip(st *store.Store, sessionID string) (store.Hash, error) {
	refName := "sessions/" + sessionID
	if err := validateRefName(refName); err != nil {
		return "", err
	}
	return st.ReadRef(refName)
}

func (s *Server) handleAPIStep(w http.ResponseWriter, _ *http.Request, repoID string, st *store.Store, hash store.Hash) {
	if !hashRE.MatchString(string(hash)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid step hash"})
		return
	}
	step, err := st.ReadStep(hash)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "step not found"})
			return
		}
		s.logf("read step %s in %s: %v", hash, repoID, err)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "object is not a readable step"})
		return
	}
	if step.Tree == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "object is not a readable step"})
		return
	}
	writeJSON(w, http.StatusOK, s.stepToJSON(st, repoID, hash, step))
}

func (s *Server) handleAPIFiles(w http.ResponseWriter, r *http.Request, repoID string, st *store.Store) {
	hash, step, ok := s.requestedStep(w, r, repoID, st)
	if !ok {
		return
	}
	tree, err := st.ReadTree(step.Tree)
	if err != nil {
		s.logf("read tree %s for step %s in %s: %v", step.Tree, hash, repoID, err)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "step tree is unavailable"})
		return
	}
	resp := filesResponse{
		StepHash: string(hash), TreeHash: string(step.Tree),
		Files: make([]fileJSON, 0, len(tree.Entries)),
	}
	for _, entry := range tree.Entries {
		size, err := objectSize(st, entry.Blob)
		if err != nil {
			s.logf("read file blob %s in %s: %v", entry.Blob, repoID, err)
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "tree references an unavailable file blob"})
			return
		}
		resp.Files = append(resp.Files, fileJSON{
			Path: entry.Path, BlobHash: string(entry.Blob), Mode: entry.Mode, Size: size,
		})
	}
	resp.TotalFiles = len(resp.Files)
	writeJSON(w, http.StatusOK, resp)
}

func objectSize(st *store.Store, hash store.Hash) (int, error) {
	if !hashRE.MatchString(string(hash)) {
		return 0, errors.New("invalid object hash")
	}
	info, err := os.Stat(filepath.Join(st.Root, "objects", string(hash[:2]), string(hash)))
	if err != nil {
		return 0, err
	}
	return int(info.Size()), nil
}

func (s *Server) requestedStep(w http.ResponseWriter, r *http.Request, repoID string, st *store.Store) (store.Hash, *store.Step, bool) {
	rawHash := r.URL.Query().Get("step")
	sessionID := r.URL.Query().Get("session")
	if rawHash == "" && sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provide step or session parameter"})
		return "", nil, false
	}
	var hash store.Hash
	if rawHash != "" {
		if !hashRE.MatchString(rawHash) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid step hash"})
			return "", nil, false
		}
		hash = store.Hash(rawHash)
	} else {
		var err error
		hash, err = readSessionTip(st, sessionID)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such session"})
			} else {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			}
			return "", nil, false
		}
	}
	step, err := st.ReadStep(hash)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "step not found"})
		} else {
			s.logf("read requested step %s in %s: %v", hash, repoID, err)
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "object is not a readable step"})
		}
		return "", nil, false
	}
	if step.Tree == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "object is not a readable step"})
		return "", nil, false
	}
	return hash, step, true
}

func (s *Server) handleAPIBlame(w http.ResponseWriter, r *http.Request, repoID string, st *store.Store) {
	hash, _, ok := s.requestedStep(w, r, repoID, st)
	if !ok {
		return
	}
	filePath, err := normalizedRepoPath(r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	content, blobHash, blame, err := deriveBlame(st, hash, filePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "file not found in step tree"})
			return
		}
		s.logf("derive blame for %s at %s in %s: %v", filePath, hash, repoID, err)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "file history is incomplete"})
		return
	}
	lines := splitStoredLines(content)
	resp := blameResponse{
		StepHash: string(hash), Path: filePath, BlobHash: string(blobHash),
		Lines: make([]blameLineJSON, 0, len(lines)),
	}
	stepCache := make(map[store.Hash]*store.Step)
	for i, line := range lines {
		row := blameLineJSON{Number: i + 1, Content: line}
		if i < len(blame.Lines) {
			provenance := blame.Lines[i]
			row.StepHash = string(provenance)
			originStep, found := stepCache[provenance]
			if !found {
				originStep, _ = st.ReadStep(provenance)
				stepCache[provenance] = originStep
			}
			if originStep != nil {
				row.Origin = originStep.Origin
				row.Timestamp = rfc3339FromNanos(originStep.TimestampNanos)
			}
		}
		resp.Lines = append(resp.Lines, row)
	}
	writeJSON(w, http.StatusOK, resp)
}

func normalizedRepoPath(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("missing path parameter")
	}
	raw = strings.ReplaceAll(raw, "\\", "/")
	for _, segment := range strings.Split(raw, "/") {
		if segment == ".." {
			return "", errors.New("path must be repository-relative")
		}
	}
	cleaned := path.Clean(raw)
	if cleaned == "." || path.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("path must be repository-relative")
	}
	return cleaned, nil
}

func deriveBlame(st *store.Store, target store.Hash, filePath string) ([]byte, store.Hash, *store.BlameMap, error) {
	steps, hashes, err := walkSession(st, target, maxSessionWalk)
	if err != nil {
		return nil, "", nil, err
	}
	if len(steps) == maxSessionWalk && steps[len(steps)-1].Parent != "" {
		return nil, "", nil, errors.New("step history exceeds blame walk limit")
	}
	var oldContent []byte
	var content []byte
	var blobHash store.Hash
	var blame *store.BlameMap
	for i := len(steps) - 1; i >= 0; i-- {
		step := steps[i]
		tree, err := st.ReadTree(step.Tree)
		if err != nil {
			return nil, "", nil, err
		}
		entry := tree.FindEntry(filePath)
		if entry == nil {
			oldContent = nil
			content = nil
			blobHash = ""
			blame = nil
			continue
		}
		content, err = st.ReadBlob(entry.Blob)
		if err != nil {
			return nil, "", nil, err
		}
		blame = store.ComputeBlame(oldContent, content, blame, hashes[i])
		oldContent = content
		blobHash = entry.Blob
	}
	if blobHash == "" {
		return nil, "", nil, fs.ErrNotExist
	}
	return content, blobHash, blame, nil
}

func splitStoredLines(content []byte) []string {
	if len(content) == 0 {
		return []string{}
	}
	lines := strings.Split(string(content), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
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
		Hash:            string(hash),
		Timestamp:       rfc3339FromNanos(step.TimestampNanos),
		Origin:          step.Origin,
		SessionID:       step.SessionID,
		TurnID:          step.TurnID,
		AgentID:         step.AgentID,
		Parent:          string(step.Parent),
		SecondaryParent: string(step.SecondaryParent),
		Tree:            string(step.Tree),
		Causes:          make([]logCauseJSON, 0, len(step.Causes)),
		Files:           []string{},
		Messages:        s.conversationMessages(st, repoID, step.Conversation),
		Events:          s.conversationEvents(st, repoID, step),
		Usage:           step.Usage,
		Effects:         step.Effects,
	}
	if js.Effects == nil {
		js.Effects = []store.Effect{}
	}
	if step.Author.Name != "" || step.Author.Email != "" {
		js.Author = &authorJSON{Name: step.Author.Name, Email: step.Author.Email}
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
	if changed := changedFiles(st, step); len(changed) > 0 {
		// The tree delta is canonical and repository-relative. Cause arguments
		// are only a legacy fallback; combining them would expose an absolute
		// host path next to the same repository path and double-count the file.
		js.Files = changed
	}

	return js
}

// changedFiles derives the workspace delta from canonical trees. It returns
// nil for older/partial history so cause-derived paths remain available.
func changedFiles(st *store.Store, step *store.Step) []string {
	if step == nil || step.Tree == "" {
		return nil
	}
	current, err := st.ReadTree(step.Tree)
	if err != nil {
		return nil
	}
	previous := map[string]store.TreeEntry{}
	if step.Parent != "" {
		parent, err := st.ReadStep(step.Parent)
		if err == nil && parent.Tree != "" {
			if tree, err := st.ReadTree(parent.Tree); err == nil {
				for _, entry := range tree.Entries {
					previous[entry.Path] = entry
				}
			}
		}
	}
	var out []string
	for _, entry := range current.Entries {
		old, found := previous[entry.Path]
		if !found || old.Blob != entry.Blob || old.Mode != entry.Mode {
			out = append(out, entry.Path)
		}
		delete(previous, entry.Path)
	}
	for path := range previous {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func (s *Server) conversationEvents(st *store.Store, repoID string, step *store.Step) []eventJSON {
	out := []eventJSON{}
	hasPortableTools := false
	for _, entry := range s.conversationEntries(st, repoID, step.Conversation) {
		event := eventJSON{
			Type:      entry.Type,
			Timestamp: rfc3339FromNanos(entry.TS),
			Text:      entry.Text,
			ToolName:  entry.ToolName,
			ToolUseID: entry.ToolUseID,
		}
		switch entry.Type {
		case "user", "assistant", "reasoning":
			out = append(out, event)
		case "tool_call":
			hasPortableTools = true
			event.Input = s.readJSONBlob(st, repoID, store.Hash(entry.ToolInput))
			out = append(out, event)
		case "tool_result":
			hasPortableTools = true
			event.Output = s.readJSONBlob(st, repoID, store.Hash(entry.ToolOutput))
			out = append(out, event)
		}
	}

	// Early portable conversation objects contained text only. Causes are the
	// canonical legacy fallback; timestamps are offset deterministically so the
	// UI can preserve call/result order without pretending to know more.
	if !hasPortableTools {
		for i, cause := range step.Causes {
			base := step.TimestampNanos + int64(i*2)
			out = append(out,
				eventJSON{
					Type: "tool_call", Timestamp: rfc3339FromNanos(base),
					ToolName: cause.ToolName, ToolUseID: cause.ToolUseID,
					Input: s.readJSONBlob(st, repoID, cause.ArgsBlob),
				},
				eventJSON{
					Type: "tool_result", Timestamp: rfc3339FromNanos(base + 1),
					ToolName: cause.ToolName, ToolUseID: cause.ToolUseID,
					Output: s.readJSONBlob(st, repoID, cause.ResultBlob),
				},
			)
		}
	}
	return out
}

// conversationMessages reads a step's conversation blob and maps each entry to
// the viewer's flat {type, message:{role, content}} record. It tolerates an
// empty, missing, or invalid blob by returning an empty (non-nil) slice, so the
// JSON always carries a "messages" array.
func (s *Server) conversationMessages(st *store.Store, repoID string, convHash store.Hash) []MessageJSON {
	out := []MessageJSON{}
	for _, e := range s.conversationEntries(st, repoID, convHash) {
		// Tool calls/results are exposed through the step's causes. Modern
		// conversation blobs also carry them to preserve local ordering after a
		// pull, but the read API's message shape only represents text roles.
		if e.Type != "user" && e.Type != "assistant" && e.Type != "reasoning" {
			continue
		}
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
