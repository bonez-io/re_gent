package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bonez-io/re_gent/internal/config"
	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/insight"
	"github.com/bonez-io/re_gent/internal/insight/mirror"
	// The read pipeline registers itself as the insight processor. Every
	// server composition carries it, so a project enabled on any server is
	// read the same way.
	_ "github.com/bonez-io/re_gent/internal/insight/pipeline"
	"github.com/bonez-io/re_gent/internal/insight/provider"
	"github.com/bonez-io/re_gent/internal/insight/search"
	"github.com/bonez-io/re_gent/internal/store"
	"github.com/pelletier/go-toml/v2"
)

// InsightConfigFile is the server's provider configuration, under the data
// directory: the same [model] and [embedding] tables a person puts under
// [insight] in ~/.regent/config.toml. REGENT_INSIGHT_CONFIG overrides the
// path. Keys are named by environment variable here too; the server process
// reads them at call time.
const InsightConfigFile = "insight.toml"

// insightEnabledKey is the per-project switch, kept in the project's own
// index so it travels with the project's data.
const insightEnabledKey = "enabled"

// insightService runs RFC 0007 for a server: a per-project index mirrored
// from pushed refs, an in-process worker, and the routes the CLI calls in
// server mode.
type insightService struct {
	srv        *Server
	configPath string
	providers  config.InsightUserConfig
	configErr  error

	mu      sync.Mutex
	indexes map[string]*index.DB
	running map[string]bool
}

func newInsightService(srv *Server) *insightService {
	svc := &insightService{srv: srv, indexes: map[string]*index.DB{}, running: map[string]bool{}}
	svc.configPath = strings.TrimSpace(os.Getenv("REGENT_INSIGHT_CONFIG"))
	if svc.configPath == "" {
		svc.configPath = filepath.Join(srv.dataDir, InsightConfigFile)
	}
	data, err := os.ReadFile(svc.configPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// No providers: projects can be enabled, nothing is read. Status says so.
	case err != nil:
		svc.configErr = fmt.Errorf("read %s: %w", svc.configPath, err)
	default:
		if err := toml.Unmarshal(data, &svc.providers); err != nil {
			svc.configErr = fmt.Errorf("parse %s: %w", svc.configPath, err)
		}
	}
	return svc
}

// index returns the project's index, opened once per process. It lives in
// the project's storage root beside objects/ and refs/.
func (svc *insightService) index(st *store.Store) (*index.DB, error) {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if idx, ok := svc.indexes[st.Root]; ok {
		return idx, nil
	}
	idx, err := index.Open(st)
	if err != nil {
		return nil, err
	}
	svc.indexes[st.Root] = idx
	return idx, nil
}

// settings resolves a project's settings: the per-project switch plus the
// server's providers.
func (svc *insightService) settings(idx *index.DB) (insight.Settings, error) {
	if svc.configErr != nil {
		return insight.Settings{}, svc.configErr
	}
	enabled, err := idx.InsightMeta(insightEnabledKey)
	if err != nil {
		return insight.Settings{}, err
	}
	return insight.Resolve(store.InsightConfig{Enabled: enabled == "true"}, svc.providers)
}

// afterRefUpdate is the ingest hook: a session ref moved, so mirror the new
// steps into the index, queue the turn, and make sure a worker is running.
// Everything here is best-effort and logged; the push already succeeded.
func (svc *insightService) afterRefUpdate(repoID string, st *store.Store, name string, tip store.Hash) {
	if !strings.HasPrefix(name, "sessions/") {
		return
	}
	// A project that never enabled insight has no index; a push must not
	// create one (and an ingest goroutine must not write into a project
	// directory nobody asked it to touch).
	if _, err := os.Stat(filepath.Join(st.Root, "index.db")); err != nil {
		return
	}
	idx, err := svc.index(st)
	if err != nil {
		svc.srv.logf("insight %s: open index: %v", repoID, err)
		return
	}
	settings, err := svc.settings(idx)
	if err != nil || !settings.Active() {
		return
	}
	sessionID, n, err := mirror.SyncSession(st, idx, strings.TrimPrefix(name, "sessions/"), tip)
	if err != nil {
		svc.srv.logf("insight %s: mirror %s: %v", repoID, sessionID, err)
	}
	if n == 0 {
		return
	}
	if _, _, err := idx.EnqueueInsightJob(index.InsightJob{Kind: index.InsightJobKindTurn, SessionID: sessionID, StepID: string(tip)}); err != nil {
		svc.srv.logf("insight %s: enqueue: %v", repoID, err)
		return
	}
	svc.kick(repoID, st, idx, settings)
}

// enqueueUnread queues a turn job for every session the worker has not read
// to the end, and returns how many it queued.
func (svc *insightService) enqueueUnread(repoID string, idx *index.DB) int {
	sessions, err := idx.SessionsWithUnread()
	if err != nil {
		svc.srv.logf("insight %s: unread sessions: %v", repoID, err)
		return 0
	}
	queued := 0
	for _, sessionID := range sessions {
		tip, _ := idx.SessionHead(sessionID)
		if _, inserted, err := idx.EnqueueInsightJob(index.InsightJob{Kind: index.InsightJobKindTurn, SessionID: sessionID, StepID: string(tip)}); err != nil {
			svc.srv.logf("insight %s: enqueue %s: %v", repoID, sessionID, err)
		} else if inserted {
			queued++
		}
	}
	return queued
}

// kick starts a worker goroutine for the project unless one is running.
func (svc *insightService) kick(repoID string, st *store.Store, idx *index.DB, settings insight.Settings) {
	svc.mu.Lock()
	if svc.running[st.Root] {
		svc.mu.Unlock()
		return
	}
	svc.running[st.Root] = true
	svc.mu.Unlock()

	go func() {
		defer func() {
			svc.mu.Lock()
			delete(svc.running, st.Root)
			svc.mu.Unlock()
		}()
		processor, err := insight.NewProcessor(st, idx, settings)
		if err != nil {
			svc.srv.logf("insight %s: %v", repoID, err)
			return
		}
		worker := &insight.Worker{Store: st, Index: idx, Processor: processor, Log: func(line string) {
			svc.srv.logf("insight %s: %s", repoID, line)
		}}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		report, held, err := worker.Run(ctx)
		if err != nil {
			svc.srv.logf("insight %s: worker: %v", repoID, err)
		}
		if held && (report.Done > 0 || report.Failed > 0) {
			svc.srv.logf("insight %s: %d done, %d retried, %d failed", repoID, report.Done, report.Retried, report.Failed)
		}
	}()
}

// handleAPIInsight serves /{repo}/api/insight/{status|settings|run|rebuild}.
func (s *Server) handleAPIInsight(w http.ResponseWriter, r *http.Request, repoID string, st *store.Store, verb string) {
	svc := s.insight
	idx, err := svc.index(st)
	if err != nil {
		s.logf("insight %s: open index: %v", repoID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "open index failed"})
		return
	}
	switch {
	case verb == "status" && r.Method == http.MethodGet:
		s.writeInsightStatus(w, st, idx)
	case verb == "settings" && r.Method == http.MethodPost:
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "decode body: " + err.Error()})
			return
		}
		if err := idx.SetInsightMeta(insightEnabledKey, strconv.FormatBool(body.Enabled)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if body.Enabled {
			if _, err := mirror.Sync(st, idx); err != nil {
				s.logf("insight %s: mirror: %v", repoID, err)
			}
			if err := idx.RebuildInsightFTS(); err != nil {
				s.logf("insight %s: rebuild fts: %v", repoID, err)
			}
			_ = idx.SetInsightMeta("enabled_at", time.Now().UTC().Format(time.RFC3339))
			// Start on what is already here; a person who enables expects
			// to see something without a second command.
			if settings, err := svc.settings(idx); err == nil && settings.Active() {
				if svc.enqueueUnread(repoID, idx) > 0 {
					svc.kick(repoID, st, idx, settings)
				}
			}
		}
		s.writeInsightStatus(w, st, idx)
	case verb == "run" && r.Method == http.MethodPost:
		settings, err := svc.settings(idx)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
			return
		}
		if !settings.Active() {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "insight is not active for this project; see status"})
			return
		}
		if _, err := mirror.Sync(st, idx); err != nil {
			s.logf("insight %s: mirror: %v", repoID, err)
		}
		svc.enqueueUnread(repoID, idx)
		svc.kick(repoID, st, idx, settings)
		s.writeInsightStatus(w, st, idx)
	case verb == "rebuild" && r.Method == http.MethodPost:
		if _, err := mirror.Sync(st, idx); err != nil {
			s.logf("insight %s: mirror: %v", repoID, err)
		}
		if err := idx.RebuildInsightFTS(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		queued, err := idx.EnqueueSessionInsightJobs()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if settings, err := svc.settings(idx); err == nil && settings.Active() && queued > 0 {
			svc.kick(repoID, st, idx, settings)
		}
		s.writeInsightStatus(w, st, idx)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func (s *Server) writeInsightStatus(w http.ResponseWriter, st *store.Store, idx *index.DB) {
	settings, settingsErr := s.insight.settings(idx)
	status, err := insight.Collect(st, idx, settings, settingsErr, s.insight.configPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if s.insight.workerRunning(st) && status.WorkerPID == 0 {
		status.WorkerPID = os.Getpid()
	}
	writeJSON(w, http.StatusOK, status)
}

func (svc *insightService) workerRunning(st *store.Store) bool {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	return svc.running[st.Root]
}

// handleAPISearch serves GET /{repo}/api/search.
func (s *Server) handleAPISearch(w http.ResponseWriter, r *http.Request, repoID string, st *store.Store) {
	idx, err := s.insight.index(st)
	if err != nil {
		s.logf("insight %s: open index: %v", repoID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "open index failed"})
		return
	}
	qv := r.URL.Query()
	q := search.Query{
		Text: qv.Get("q"), File: qv.Get("file"), Entity: qv.Get("entity"),
		Status: qv.Get("status"), Session: qv.Get("session"),
	}
	if raw := qv.Get("since"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "since: want RFC 3339"})
			return
		}
		q.Since = t
	}
	if raw := qv.Get("limit"); raw != "" {
		q.Limit, _ = strconv.Atoi(raw)
	}
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 10
	}
	if err := q.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	var embedder search.Embedder
	var info provider.Info
	var notes []string
	if settings, err := s.insight.settings(idx); err == nil && settings.HasEmbedding() {
		if e, i, err := provider.NewEmbedder(settings.Embedding); err == nil {
			embedder, info = e, i
		} else {
			notes = append(notes, "embeddings unavailable ("+err.Error()+"); full-text only")
		}
	}
	res, err := search.Run(r.Context(), idx, embedder, info.Provider, info.Model, q)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	res.Notes = append(notes, res.Notes...)
	writeJSON(w, http.StatusOK, res)
}

// handleAPIWork serves GET /{repo}/api/work and /{repo}/api/work/{id}.
func (s *Server) handleAPIWork(w http.ResponseWriter, r *http.Request, repoID string, st *store.Store, id string) {
	idx, err := s.insight.index(st)
	if err != nil {
		s.logf("insight %s: open index: %v", repoID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "open index failed"})
		return
	}
	if id != "" {
		item, ok, err := idx.GetWorkItem(id)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no work item " + id})
			return
		}
		writeJSON(w, http.StatusOK, search.Describe(idx, item))
		return
	}
	qv := r.URL.Query()
	f := index.WorkItemFilter{Status: qv.Get("status"), SessionID: qv.Get("session")}
	if f.Status != "" && !index.ValidWorkItemStatus(f.Status) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "status: want wip, done, failed, abandoned, or superseded"})
		return
	}
	f.Limit, _ = strconv.Atoi(qv.Get("limit"))
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 20
	}
	items, err := idx.ListWorkItems(f)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, search.DescribeAll(idx, items))
}
