package capture

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/regent-vcs/regent/internal/ignore"
	"github.com/regent-vcs/regent/internal/index"
	"github.com/regent-vcs/regent/internal/remote"
	"github.com/regent-vcs/regent/internal/snapshot"
	"github.com/regent-vcs/regent/internal/store"
	"github.com/regent-vcs/regent/internal/usage"
)

const (
	OriginClaudeCode = "claude_code"
	OriginCodexCLI   = "codex_cli"
	OriginOpenCode   = "opencode"
	OriginPi         = "pi"

	maxRefUpdateAttempts = 8

	// maxUsageBaselineWalk bounds how far back we look for the last recorded
	// usage total. Every step normally carries one, so the walk stops at the
	// parent; the bound only caps the cost when a run of steps was captured
	// without a readable transcript.
	maxUsageBaselineWalk = 64
)

var messageCounter atomic.Uint64

type Recorder struct {
	Store *store.Store
	Index *index.DB
	CWD   string
	// Sink receives blobs and ref updates for optional remote replication.
	// nil is treated as a NoopSink: local writes still succeed.
	Sink CaptureSink
	// Server is non-nil in server mode, where the source of truth is a re_gent
	// server and Store is a disposable machine-local cache. See servermode.go.
	Server *ServerLink

	// scannedTranscripts remembers which (session, turn, transcript) triples this
	// process already scanned for assistant text, so a multi-tool batch costs one
	// transcript read instead of one per tool.
	scannedTranscripts map[string]struct{}
}

type turnScope struct {
	id       string
	allTurns bool
}

type SessionMetadata struct {
	SessionID      string
	Origin         string
	Model          string
	PermissionMode string
	TranscriptPath string
	AgentID        string
	externalID     string
}

type UserPrompt struct {
	SessionMetadata
	TurnID string
	Prompt string
}

type AssistantResponse struct {
	SessionMetadata
	TurnID               string
	LastAssistantMessage string
}

type ToolUse struct {
	SessionMetadata
	TurnID       string
	ToolName     string
	ToolUseID    string
	ToolInput    json.RawMessage
	ToolResponse json.RawMessage
}

// Open returns the recorder for this working directory.
//
// Server mode wins when it is configured: the source of truth is then the
// server and the repository needs no .regent/ directory at all. If server mode
// is configured but cannot be initialised, capture falls back to local mode
// (and to a clean no-op when there is no local store) rather than failing the
// agent's turn.
func Open(cwd string) (*Recorder, bool, error) {
	if cwd == "" {
		return nil, false, fmt.Errorf("cwd is required")
	}

	cfg, enabled, cfgErr := serverConfigFor(remote.OSEnv, cwd)
	if cfgErr != nil {
		logServerModeFallback(cfg, cfgErr)
	}
	if enabled {
		rec, err := OpenServerMode(cwd, cfg)
		if err == nil {
			// Deliver anything a previous invocation could not.
			rec.SyncToServer("hook start")
			return rec, true, nil
		}
		logServerModeFallback(cfg, err)
	}

	s, err := store.Open(filepath.Join(cwd, ".regent"))
	if err != nil {
		if errors.Is(err, store.ErrNotRegentRepository) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("open store: %w", err)
	}

	idx, err := index.Open(s)
	if err != nil {
		return nil, false, fmt.Errorf("open index: %w", err)
	}

	return &Recorder{Store: s, Index: idx, CWD: cwd}, true, nil
}

func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	var errs []error
	if r.Sink != nil {
		if err := r.Sink.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if r.Index != nil {
		if err := r.Index.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// enqueueBlobToSink forwards a locally-committed blob to the sink for optional
// remote replication. No-ops when Sink is nil.
func (r *Recorder) enqueueBlobToSink(hash store.Hash, data []byte) {
	if r.Sink != nil {
		r.Sink.EnqueueBlob(hash, data)
	}
}

// enqueueRefToSink forwards a locally-committed ref update to the sink.
// No-ops when Sink is nil.
func (r *Recorder) enqueueRefToSink(name string, old, new store.Hash) {
	if r.Sink != nil {
		r.Sink.EnqueueRef(name, old, new)
	}
}

func (r *Recorder) UpsertSession(meta SessionMetadata) error {
	session, err := normalizeSession(meta)
	if err != nil {
		return err
	}
	if err := r.adoptLegacySession(session); err != nil {
		return err
	}

	return r.upsertNormalizedSession(session)
}

func (r *Recorder) upsertNormalizedSession(session SessionMetadata) error {
	return r.Index.UpsertSession(index.SessionUpdate{
		ID:             session.SessionID,
		Origin:         session.Origin,
		Model:          session.Model,
		PermissionMode: session.PermissionMode,
		TranscriptPath: session.TranscriptPath,
	})
}

func (r *Recorder) RecordUserPrompt(event UserPrompt) error {
	session, scope, err := normalizeTurnEvent(event.SessionMetadata, event.TurnID)
	if err != nil {
		return err
	}
	if err := r.adoptLegacySession(session); err != nil {
		return err
	}
	if err := r.upsertNormalizedSession(session); err != nil {
		return err
	}

	return r.Index.AppendMessage(index.Message{
		ID:          newMessageID("user"),
		SessionID:   session.SessionID,
		TurnID:      scope.id,
		Timestamp:   time.Now().UnixNano(),
		MessageType: "user",
		ContentText: event.Prompt,
	})
}

func (r *Recorder) RecordToolUse(event ToolUse) error {
	if isRegentCommand(event.ToolName, event.ToolInput) {
		return nil
	}
	session, scope, err := normalizeTurnEvent(event.SessionMetadata, event.TurnID)
	if err != nil {
		return err
	}
	if event.ToolName == "" {
		return fmt.Errorf("tool name is required")
	}
	if event.ToolUseID == "" {
		return fmt.Errorf("tool use id is required")
	}
	if len(event.ToolInput) == 0 || !json.Valid(event.ToolInput) {
		return fmt.Errorf("tool input must be valid JSON")
	}
	if len(event.ToolResponse) > 0 && !json.Valid(event.ToolResponse) {
		return fmt.Errorf("tool response must be valid JSON")
	}
	if err := r.adoptLegacySession(session); err != nil {
		return err
	}
	if err := r.upsertNormalizedSession(session); err != nil {
		return err
	}

	// Capture the narration and reasoning that preceded this tool batch before
	// the tool messages land, so the conversation keeps its chronological order.
	r.logAssistantTranscript(r.captureAssistantTranscriptOnce(session, scope))

	inputHash, err := r.Store.WriteBlob(event.ToolInput)
	if err != nil {
		return fmt.Errorf("store tool input: %w", err)
	}
	r.enqueueBlobToSink(inputHash, event.ToolInput)

	var outputHash store.Hash
	if len(event.ToolResponse) > 0 {
		outputHash, err = r.Store.WriteBlob(event.ToolResponse)
		if err != nil {
			return fmt.Errorf("store tool output: %w", err)
		}
		r.enqueueBlobToSink(outputHash, event.ToolResponse)
	}

	now := time.Now().UnixNano()
	call := index.Message{
		ID:          newMessageID("call"),
		SessionID:   session.SessionID,
		TurnID:      scope.id,
		Timestamp:   now,
		MessageType: "tool_call",
		ToolName:    event.ToolName,
		ToolUseID:   event.ToolUseID,
		ToolInput:   string(inputHash),
	}

	result := index.Message{
		ID:          newMessageID("result"),
		SessionID:   session.SessionID,
		TurnID:      scope.id,
		Timestamp:   now + 1,
		MessageType: "tool_result",
		ToolName:    event.ToolName,
		ToolUseID:   event.ToolUseID,
		ToolOutput:  string(outputHash),
	}
	if _, err := r.Index.AppendToolUseMessages(call, result); err != nil {
		return fmt.Errorf("insert tool use messages: %w", err)
	}

	return nil
}

func (r *Recorder) RecordAssistantAndFinalize(event AssistantResponse) error {
	session, scope, err := normalizeTurnEvent(event.SessionMetadata, event.TurnID)
	if err != nil {
		return err
	}
	if err := r.adoptLegacySession(session); err != nil {
		return err
	}
	if err := r.upsertNormalizedSession(session); err != nil {
		return err
	}

	// The end of a turn is the delivery point: by now the step, its tree and
	// its tool payloads exist locally. Deferred so a partially recorded turn is
	// still delivered rather than stranded in the cache.
	defer r.SyncToServer("turn end")

	// Pull in the assistant text and reasoning the turn produced. This runs before
	// the step is written so the blocks are pending and get linked to it, and
	// before the early return below so a recovered turn is enriched too.
	r.logAssistantTranscript(r.captureAssistantTranscript(session, scope))

	if !scope.allTurns {
		existingStep, ok, err := r.existingStepForTurn(session.SessionID, scope.id)
		if err != nil {
			return err
		}
		if ok {
			if err := r.indexAndLinkStep(existingStep, session.SessionID, scope); err != nil {
				LogHookError(r.Store.Root, fmt.Sprintf("recover existing turn %s: %v", scope.id, err))
			}
			if session.TranscriptPath != "" {
				if err := r.ArchiveTranscript(session.SessionID, session.TranscriptPath); err != nil {
					LogHookError(r.Store.Root, fmt.Sprintf("archive transcript: %v", err))
				}
			}
			return nil
		}
	}

	now := time.Now().UnixNano()
	// The host's final assistant message is usually the tail of the transcript we
	// just scanned. Only append it when this turn has no identical pending block,
	// otherwise the same text would be recorded twice.
	captured, err := r.Index.PendingMessageExists(session.SessionID, scope.id, "assistant", event.LastAssistantMessage, scope.allTurns)
	if err != nil {
		return fmt.Errorf("look up pending assistant message: %w", err)
	}
	if !captured {
		if err := r.Index.AppendMessage(index.Message{
			ID:          newMessageID("assistant"),
			SessionID:   session.SessionID,
			TurnID:      scope.id,
			Timestamp:   now,
			MessageType: "assistant",
			ContentText: event.LastAssistantMessage,
		}); err != nil {
			return fmt.Errorf("insert assistant message: %w", err)
		}
	}

	stepHash, err := r.createStepForTurn(session, scope)
	if err != nil {
		return err
	}
	if stepHash == "" {
		rows, err := r.markPendingMessagesProcessed(session.SessionID, scope, now)
		if err != nil {
			return fmt.Errorf("mark messages processed: %w", err)
		}
		if rows == 0 {
			logDebug(r.Store, fmt.Sprintf("no pending messages marked for session %s turn %q", session.SessionID, scope.id))
		}
	}

	if session.TranscriptPath != "" {
		if err := r.ArchiveTranscript(session.SessionID, session.TranscriptPath); err != nil {
			LogHookError(r.Store.Root, fmt.Sprintf("archive transcript: %v", err))
		}
	}

	return nil
}

func (r *Recorder) ArchiveTranscript(sessionID, transcriptPath string) error {
	data, err := os.ReadFile(transcriptPath)
	if err != nil {
		return fmt.Errorf("read transcript: %w", err)
	}

	blobHash, err := r.Store.WriteBlob(data)
	if err != nil {
		return fmt.Errorf("write transcript blob: %w", err)
	}

	// A transcript archive is not reachable from any step, so the DAG walk that
	// pushes a session cannot find it. Queue it explicitly.
	r.markLooseObject(blobHash)

	return r.Index.InsertJSONLSnapshot(sessionID, time.Now().UnixNano(), blobHash)
}

func (r *Recorder) adoptLegacySession(session SessionMetadata) error {
	if session.externalID == "" || session.externalID == session.SessionID {
		return nil
	}

	// Migrate old-format canonical ref (origin:externalID) to new format (origin--externalID).
	if err := r.adoptLegacyCanonicalRef(session); err != nil {
		return err
	}

	if !isSafeLegacyRefName(session.externalID) {
		return r.adoptLegacySessionIndex(session)
	}

	legacyHead, err := r.Store.ReadRef("sessions/" + session.externalID)
	legacyRefExists := err == nil
	mergeLegacyIndex := !legacyRefExists
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("read legacy session ref: %w", err)
		}
	}

	if legacyRefExists {
		canonicalHead, err := r.Store.ReadRef("sessions/" + session.SessionID)
		switch {
		case err == nil:
			if canonicalHead == legacyHead {
				mergeLegacyIndex = true
			} else {
				isAncestor, err := r.stepIsAncestor(legacyHead, canonicalHead)
				if err != nil {
					return fmt.Errorf("check legacy session ancestry: %w", err)
				}
				if isAncestor {
					mergeLegacyIndex = true
				} else {
					mergeLegacyIndex = false
					LogHookError(r.Store.Root, fmt.Sprintf("archiving divergent legacy session ref %s -> %s", session.externalID, session.SessionID))
					if archiveErr := r.archiveLegacySessionRef(session.externalID, legacyHead); archiveErr != nil {
						return archiveErr
					}
				}
			}
		case errors.Is(err, fs.ErrNotExist):
			if err := r.Store.UpdateRef("sessions/"+session.SessionID, "", legacyHead); err != nil && !errors.Is(err, store.ErrRefConflict) {
				return fmt.Errorf("write canonical session ref: %w", err)
			}
			mergeLegacyIndex = true
			logDebug(r.Store, fmt.Sprintf("adopted legacy session ref %s -> %s", session.externalID, session.SessionID))
		default:
			return fmt.Errorf("read canonical session ref: %w", err)
		}

		if err := r.Store.DeleteRef("sessions/"+session.externalID, legacyHead); err != nil && !errors.Is(err, store.ErrRefConflict) {
			return fmt.Errorf("delete legacy session ref: %w", err)
		}
	}

	if !mergeLegacyIndex {
		logDebug(r.Store, fmt.Sprintf("preserved divergent legacy session index %s separate from %s", session.externalID, session.SessionID))
		return nil
	}
	return r.adoptLegacySessionIndex(session)
}

func (r *Recorder) adoptLegacySessionIndex(session SessionMetadata) error {
	changed, err := r.Index.RenameSession(session.externalID, session.SessionID, session.Origin)
	if err != nil {
		return fmt.Errorf("adopt legacy session index: %w", err)
	}
	if changed {
		logDebug(r.Store, fmt.Sprintf("adopted legacy session index %s -> %s", session.externalID, session.SessionID))
	}
	return nil
}

// adoptLegacyCanonicalRef migrates a session ref from the old ":" separator format
// (e.g., claude_code:c8f65e87) to the new "--" separator format
// (e.g., claude_code--c8f65e87). The old ":" format is invalid on Windows filesystems.
func (r *Recorder) adoptLegacyCanonicalRef(session SessionMetadata) error {
	oldCanonicalID := session.Origin + ":" + url.QueryEscape(session.externalID)
	if oldCanonicalID == session.SessionID {
		return nil
	}

	legacyHead, err := r.Store.ReadRef("sessions/" + oldCanonicalID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read legacy canonical ref: %w", err)
	}

	canonicalHead, canonErr := r.Store.ReadRef("sessions/" + session.SessionID)
	if canonErr == nil {
		if canonicalHead == legacyHead {
			_ = r.Store.DeleteRef("sessions/"+oldCanonicalID, legacyHead)
			return r.adoptLegacyCanonicalIndex(oldCanonicalID, session)
		}
		isAncestor, ancErr := r.stepIsAncestor(legacyHead, canonicalHead)
		if ancErr != nil {
			return fmt.Errorf("check legacy canonical ancestry: %w", ancErr)
		}
		if isAncestor {
			_ = r.Store.DeleteRef("sessions/"+oldCanonicalID, legacyHead)
			return r.adoptLegacyCanonicalIndex(oldCanonicalID, session)
		}
		LogHookError(r.Store.Root, fmt.Sprintf("archiving divergent legacy canonical ref %s -> %s", oldCanonicalID, session.SessionID))
		return r.archiveLegacySessionRef(oldCanonicalID, legacyHead)
	}

	if !errors.Is(canonErr, fs.ErrNotExist) {
		return fmt.Errorf("read canonical ref: %w", canonErr)
	}

	if err := r.Store.UpdateRef("sessions/"+session.SessionID, "", legacyHead); err != nil && !errors.Is(err, store.ErrRefConflict) {
		return fmt.Errorf("adopt legacy canonical ref: %w", err)
	}
	_ = r.Store.DeleteRef("sessions/"+oldCanonicalID, legacyHead)
	logDebug(r.Store, fmt.Sprintf("adopted legacy canonical ref %s -> %s", oldCanonicalID, session.SessionID))
	return r.adoptLegacyCanonicalIndex(oldCanonicalID, session)
}

// adoptLegacyCanonicalIndex migrates SQLite index rows (steps, messages, tool
// uses, transcript pointers, snapshots) from the old colon-separated canonical
// session id to the new "--" id. Without this the ref is repointed but log/show/
// sessions queries, which key on the new id, would no longer find the recorded
// history. Mirrors adoptLegacySessionIndex; only called when the colon ref was
// actually migrated, so it stays off the per-turn hot path.
func (r *Recorder) adoptLegacyCanonicalIndex(oldCanonicalID string, session SessionMetadata) error {
	changed, err := r.Index.RenameSession(oldCanonicalID, session.SessionID, session.Origin)
	if err != nil {
		return fmt.Errorf("adopt legacy canonical index: %w", err)
	}
	if changed {
		logDebug(r.Store, fmt.Sprintf("adopted legacy canonical index %s -> %s", oldCanonicalID, session.SessionID))
	}
	return nil
}

func isSafeLegacyRefName(name string) bool {
	return name != "" && !strings.Contains(name, "/") && !strings.Contains(name, "\\") && name != "." && name != ".."
}

func (r *Recorder) stepIsAncestor(ancestor, descendant store.Hash) (bool, error) {
	for descendant != "" {
		if descendant == ancestor {
			return true, nil
		}
		step, err := r.Store.ReadStep(descendant)
		if err != nil {
			return false, err
		}
		descendant = step.Parent
	}
	return false, nil
}

func (r *Recorder) archiveLegacySessionRef(externalID string, head store.Hash) error {
	archiveRef := "legacy-sessions/" + externalID
	if err := r.Store.UpdateRef(archiveRef, "", head); err != nil {
		if !errors.Is(err, store.ErrRefConflict) {
			return fmt.Errorf("archive legacy session ref: %w", err)
		}
		archivedHead, readErr := r.Store.ReadRef(archiveRef)
		if readErr != nil {
			return fmt.Errorf("read archived legacy session ref: %w", readErr)
		}
		if archivedHead != head {
			return fmt.Errorf("archive legacy session ref: %w", store.ErrRefConflict)
		}
	}
	return nil
}

func (r *Recorder) createStepForTurn(session SessionMetadata, scope turnScope) (store.Hash, error) {
	sessionID := session.SessionID

	// Read the transcript once, before the ref-conflict retry loop: it is the
	// same file on every attempt, and re-parsing it would only slow the turn.
	cumulativeUsage, haveUsage := r.collectUsage(session)

	for attempt := 0; attempt < maxRefUpdateAttempts; attempt++ {
		parentHash, err := r.Store.ReadRef("sessions/" + sessionID)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("read session ref: %w", err)
		}

		messages, err := r.pendingMessages(sessionID, scope)
		if err != nil {
			return "", fmt.Errorf("get pending messages: %w", err)
		}

		causes := causesFromMessages(messages)
		if len(causes) == 0 {
			return "", nil
		}

		treeHash, err := snapshotWorkspace(r.Store, r.CWD)
		if err != nil {
			return "", fmt.Errorf("snapshot workspace: %w", err)
		}

		step := &store.Step{
			Parent:         parentHash,
			Tree:           treeHash,
			Cause:          causes[0],
			Causes:         causes,
			SessionID:      sessionID,
			Origin:         session.Origin,
			TurnID:         scope.id,
			AgentID:        session.AgentID,
			Author:         ResolveAuthor(),
			TimestampNanos: time.Now().UnixNano(),
		}
		if haveUsage {
			r.attachUsage(step, parentHash, cumulativeUsage)
		}
		// Persist the turn's conversation as a content-addressed blob so it
		// travels with the step to a server that has no SQLite index. A failure
		// here is logged, never fatal: capture must not break the agent's turn.
		if convHash, cerr := conversationBlob(r.Store, messages); cerr != nil {
			LogHookError(r.Store.Root, fmt.Sprintf("conversation blob for turn %q: %v", scope.id, cerr))
		} else {
			step.Conversation = convHash
		}

		stepHash, err := r.Store.WriteStep(step)
		if err != nil {
			return "", fmt.Errorf("write step: %w", err)
		}

		// Enqueue the step blob for remote replication. We re-marshal here so we
		// have the raw bytes without an extra store read-back.
		if r.Sink != nil {
			if stepData, merr := marshalStep(step); merr == nil {
				r.enqueueBlobToSink(stepHash, stepData)
			}
		}

		if err := computeAndWriteBlame(r.Store, parentHash, stepHash, treeHash); err != nil {
			LogHookError(r.Store.Root, fmt.Sprintf("blame step %s: %v", stepHash, err))
		}

		if err := r.Store.UpdateRef("sessions/"+sessionID, parentHash, stepHash); err != nil {
			if errors.Is(err, store.ErrRefConflict) {
				time.Sleep(refUpdateBackoff(attempt))
				continue
			}
			return "", fmt.Errorf("update ref: %w", err)
		}

		// Replicate the ref update after local CAS succeeds.
		r.enqueueRefToSink("sessions/"+sessionID, parentHash, stepHash)

		if err := r.indexAndLinkStep(stepHash, sessionID, scope); err != nil {
			LogHookError(r.Store.Root, fmt.Sprintf("index/link step %s: %v", stepHash, err))
		}

		return stepHash, nil
	}

	return "", fmt.Errorf("update ref: %w", store.ErrRefConflict)
}

// collectUsage reads the API usage recorded in the session's transcript,
// including every subagent transcript it spawned. It reports false when there
// is nothing to attribute, so a step written without a transcript stays exactly
// as it was before usage capture existed.
//
// The transcript is the host's, not ours: it can be absent, mid-write, or in a
// format we do not recognise. Every one of those degrades to "no usage" and a
// line in the hook error log, never to a failed turn.
func (r *Recorder) collectUsage(session SessionMetadata) (store.Usage, bool) {
	if session.TranscriptPath == "" {
		return store.Usage{}, false
	}

	report, err := usage.Collect(session.TranscriptPath)
	if err != nil {
		// The error names files and syscalls, never transcript content.
		LogHookError(r.Store.Root, fmt.Sprintf("collect transcript usage: %v", err))
	}
	if report.Total.IsZero() {
		return store.Usage{}, false
	}
	return report.Total, true
}

// attachUsage records the cumulative transcript reading on the step and the
// share of it this step is responsible for.
//
// The transcript reports session totals, not per-turn ones, so a step's own
// usage is the reading now minus the reading stored on its parent. Usage the
// host has not yet flushed is therefore not lost: it lands on the next step,
// because the baseline only ever moves forward by what we actually recorded.
func (r *Recorder) attachUsage(step *store.Step, parentHash store.Hash, cumulative store.Usage) {
	total := cumulative
	step.UsageTotal = &total

	delta := cumulative.Sub(r.parentUsageTotal(parentHash))
	if delta.IsZero() {
		return
	}
	step.Usage = &delta
}

// parentUsageTotal returns the most recent cumulative usage recorded in the
// step's ancestry, or the zero value when there is none.
//
// It walks past ancestors that carry no reading rather than stopping at the
// parent. A turn whose transcript was briefly unreadable records no total, and
// treating that gap as a zero baseline would charge the next step for
// everything the session had already spent.
func (r *Recorder) parentUsageTotal(parentHash store.Hash) store.Usage {
	for current, walked := parentHash, 0; current != "" && walked < maxUsageBaselineWalk; walked++ {
		step, err := r.Store.ReadStep(current)
		if err != nil {
			LogHookError(r.Store.Root, fmt.Sprintf("read step %s for usage baseline: %v", current, err))
			return store.Usage{}
		}
		if step.UsageTotal != nil {
			return *step.UsageTotal
		}
		current = step.Parent
	}
	return store.Usage{}
}

func refUpdateBackoff(attempt int) time.Duration {
	backoff := 5 * time.Millisecond
	for i := 0; i < attempt; i++ {
		backoff *= 2
		if backoff >= 100*time.Millisecond {
			return 100 * time.Millisecond
		}
	}
	return backoff
}

func (r *Recorder) existingStepForTurn(sessionID, turnID string) (store.Hash, bool, error) {
	stepHash, ok, err := r.Index.StepForTurn(sessionID, turnID)
	if err != nil {
		return "", false, fmt.Errorf("lookup indexed turn: %w", err)
	}
	if ok {
		return stepHash, true, nil
	}

	headHash, err := r.Store.ReadRef("sessions/" + sessionID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read session ref: %w", err)
	}

	for current := headHash; current != ""; {
		step, err := r.Store.ReadStep(current)
		if err != nil {
			return "", false, fmt.Errorf("read ancestor step %s: %w", current, err)
		}
		if step.SessionID == sessionID && step.TurnID == turnID {
			return current, true, nil
		}
		current = step.Parent
	}
	return "", false, nil
}

func (r *Recorder) indexAndLinkStep(stepHash store.Hash, sessionID string, scope turnScope) error {
	step, err := r.Store.ReadStep(stepHash)
	if err != nil {
		return fmt.Errorf("read step: %w", err)
	}
	tree, err := r.Store.ReadTree(step.Tree)
	if err != nil {
		return fmt.Errorf("read tree: %w", err)
	}
	if err := r.Index.IndexStep(stepHash, step, tree); err != nil {
		return fmt.Errorf("index step: %w", err)
	}

	rows, err := r.linkPendingMessages(sessionID, scope, stepHash, time.Now().UnixNano())
	if err != nil {
		return fmt.Errorf("link messages: %w", err)
	}
	if rows == 0 {
		logDebug(r.Store, fmt.Sprintf("no pending messages linked for step %s session %s turn %q", stepHash, sessionID, scope.id))
	}
	return nil
}

func (r *Recorder) pendingMessages(sessionID string, scope turnScope) ([]index.Message, error) {
	if scope.allTurns {
		return r.Index.GetAllPendingMessages(sessionID)
	}
	return r.Index.GetPendingMessages(sessionID, scope.id)
}

func (r *Recorder) linkPendingMessages(sessionID string, scope turnScope, stepHash store.Hash, processedAt int64) (int64, error) {
	if scope.allTurns {
		return r.Index.LinkAllPendingMessagesToStep(sessionID, stepHash, processedAt)
	}
	return r.Index.LinkPendingMessagesToStep(sessionID, scope.id, stepHash, processedAt)
}

func (r *Recorder) markPendingMessagesProcessed(sessionID string, scope turnScope, processedAt int64) (int64, error) {
	if scope.allTurns {
		return r.Index.MarkAllPendingMessagesProcessed(sessionID, processedAt)
	}
	return r.Index.MarkPendingMessagesProcessed(sessionID, scope.id, processedAt)
}

func causesFromMessages(messages []index.Message) []store.Cause {
	var causes []store.Cause
	for _, msg := range messages {
		if msg.MessageType != "tool_call" || msg.ToolInput == "" {
			continue
		}

		cause := store.Cause{
			ToolUseID: msg.ToolUseID,
			ToolName:  msg.ToolName,
			ArgsBlob:  store.Hash(msg.ToolInput),
		}
		for _, resultMsg := range messages {
			if resultMsg.MessageType == "tool_result" && resultMsg.ToolUseID == msg.ToolUseID {
				cause.ResultBlob = store.Hash(resultMsg.ToolOutput)
				break
			}
		}
		causes = append(causes, cause)
	}
	return causes
}

// conversationEntry is one user/assistant/reasoning message preserved in a
// step's conversation blob.
type conversationEntry struct {
	Type string `json:"type"`
	Text string `json:"text"`
	TS   int64  `json:"ts,omitempty"`
}

// conversationBlob content-addresses the turn's conversation — the user prompt
// and the assistant/reasoning narration — and returns its hash. Tool calls and
// results are skipped because they already travel with the step as Causes. It
// returns an empty hash and writes nothing when the turn has no such messages,
// which keeps steps without conversation text byte-identical.
func conversationBlob(s *store.Store, messages []index.Message) (store.Hash, error) {
	var entries []conversationEntry
	for _, msg := range messages {
		switch msg.MessageType {
		case "user", "assistant", "reasoning":
			entries = append(entries, conversationEntry{
				Type: msg.MessageType,
				Text: msg.ContentText,
				TS:   msg.Timestamp,
			})
		}
	}
	if len(entries) == 0 {
		return "", nil
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("marshal conversation: %w", err)
	}
	return s.WriteBlob(data)
}

// marshalStep serializes a step to the same JSON format that WriteStep uses,
// producing bytes that hash to the same value already stored in the object store.
func marshalStep(step *store.Step) ([]byte, error) {
	step.NormalizeCauses()
	return json.Marshal(step)
}

func snapshotWorkspace(s *store.Store, cwd string) (store.Hash, error) {
	return snapshot.Snapshot(s, cwd, ignore.Default(cwd))
}

// ComputeAndWriteBlame derives the per-line provenance sidecars for one step by
// diffing its tree against its parent's. Blame maps are derived data, not
// canonical state, so they are recomputed rather than transferred when a cache
// is rebuilt from the server.
func ComputeAndWriteBlame(s *store.Store, parentHash, currentStepHash, treeHash store.Hash) error {
	return computeAndWriteBlame(s, parentHash, currentStepHash, treeHash)
}

func computeAndWriteBlame(s *store.Store, parentHash, currentStepHash, treeHash store.Hash) error {
	tree, err := s.ReadTree(treeHash)
	if err != nil {
		return fmt.Errorf("read current tree %s: %w", treeHash, err)
	}

	parentEntries := map[string]store.TreeEntry{}
	if parentHash != "" {
		parentStep, err := s.ReadStep(parentHash)
		if err != nil {
			return fmt.Errorf("read parent step %s: %w", parentHash, err)
		}
		parentTree, err := s.ReadTree(parentStep.Tree)
		if err != nil {
			return fmt.Errorf("read parent tree %s: %w", parentStep.Tree, err)
		}
		for _, entry := range parentTree.Entries {
			parentEntries[entry.Path] = entry
		}
	}

	var problems []error
	for _, entry := range tree.Entries {
		parentEntry, hasParentEntry := parentEntries[entry.Path]

		if hasParentEntry && parentEntry.Blob == entry.Blob {
			oldBlame, err := s.ReadBlameForFile(parentHash, entry.Path)
			if err == nil {
				if err := s.WriteBlameForFile(currentStepHash, entry.Path, oldBlame); err != nil {
					problems = append(problems, fmt.Errorf("copy blame for %s: %w", entry.Path, err))
				}
				continue
			}
			problems = append(problems, fmt.Errorf("read parent blame for unchanged %s: %w", entry.Path, err))
			continue
		}

		var oldContent []byte
		var oldBlame *store.BlameMap
		if hasParentEntry {
			oldContent, err = s.ReadBlob(parentEntry.Blob)
			if err != nil {
				problems = append(problems, fmt.Errorf("read parent blob for %s: %w", entry.Path, err))
				continue
			}
			oldBlame, err = s.ReadBlameForFile(parentHash, parentEntry.Path)
			if err != nil {
				problems = append(problems, fmt.Errorf("read parent blame for %s: %w", entry.Path, err))
			}
		}

		newContent, err := s.ReadBlob(entry.Blob)
		if err != nil {
			problems = append(problems, fmt.Errorf("read current blob for %s: %w", entry.Path, err))
			continue
		}

		newBlame := store.ComputeBlame(oldContent, newContent, oldBlame, currentStepHash)
		if err := s.WriteBlameForFile(currentStepHash, entry.Path, newBlame); err != nil {
			problems = append(problems, fmt.Errorf("write blame for %s: %w", entry.Path, err))
		}
	}

	return errors.Join(problems...)
}

func isRegentCommand(toolName string, input json.RawMessage) bool {
	if !strings.EqualFold(toolName, "Bash") && !strings.EqualFold(toolName, "functions.exec_command") {
		return false
	}

	var args map[string]interface{}
	if err := json.Unmarshal(input, &args); err != nil {
		return false
	}

	command, _ := args["command"].(string)
	if command == "" {
		command, _ = args["cmd"].(string)
	}
	return IsRegentCommand(command)
}

// IsRegentCommand reports whether the given command string starts with an
// invocation of the rgt or regent CLI binary, after stripping environment
// variable prefixes and path components.
func IsRegentCommand(command string) bool {
	fields := strings.Fields(strings.TrimSpace(command))
	for len(fields) > 0 && strings.Contains(fields[0], "=") && !strings.HasPrefix(fields[0], "=") {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return false
	}

	first := strings.TrimPrefix(filepath.Base(fields[0]), "./")
	if first == "rgt" || first == "regent" {
		return true
	}

	return len(fields) >= 3 && fields[0] == "go" && fields[1] == "run" && strings.Contains(fields[2], "cmd/rgt")
}

func normalizeTurnEvent(meta SessionMetadata, turnID string) (SessionMetadata, turnScope, error) {
	session, err := normalizeSession(meta)
	if err != nil {
		return SessionMetadata{}, turnScope{}, err
	}

	scope, err := normalizeTurnScope(session.Origin, turnID)
	if err != nil {
		return SessionMetadata{}, turnScope{}, err
	}
	return session, scope, nil
}

func normalizeSession(meta SessionMetadata) (SessionMetadata, error) {
	origin := meta.Origin
	if origin == "" {
		origin = OriginClaudeCode
	}
	if !isSafeOrigin(origin) {
		return SessionMetadata{}, fmt.Errorf("invalid origin %q", origin)
	}
	externalID := strings.TrimSpace(meta.SessionID)
	if externalID == "" {
		return SessionMetadata{}, fmt.Errorf("session id is required")
	}

	meta.Origin = origin
	meta.SessionID = canonicalSessionID(origin, externalID)
	meta.externalID = externalID
	return meta, nil
}

func canonicalSessionID(origin, externalID string) string {
	return origin + "--" + url.QueryEscape(externalID)
}

func normalizeTurnScope(origin, turnID string) (turnScope, error) {
	if turnID != "" {
		return turnScope{id: turnID}, nil
	}
	if origin == OriginClaudeCode || origin == OriginOpenCode {
		return turnScope{allTurns: true}, nil
	}
	return turnScope{}, fmt.Errorf("turn id is required")
}

func isSafeOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	for _, r := range origin {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func newMessageID(kind string) string {
	return fmt.Sprintf("msg_%d_%d_%s", time.Now().UnixNano(), messageCounter.Add(1), kind)
}

// logToFile appends a formatted line to a log file under .regent/. The log
// directory is created automatically; file I/O errors are silently discarded.
func logToFile(root, relPath, line string) {
	logPath := filepath.Join(root, relPath)
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	fmt.Fprintln(f, line)
}

func logDebug(s *store.Store, msg string) {
	logToFile(s.Root, "log/hook-debug.log",
		fmt.Sprintf("[%s] %s", time.Now().Format(time.RFC3339), msg))
}

// LogHookError writes an error message to .regent/log/hook-error.log. It is
// safe to call when the log directory does not yet exist; the directory is
// created automatically. Errors from file I/O are silently discarded so that
// hook error logging never interrupts the agent.
func LogHookError(root string, msg string) {
	logToFile(root, "log/hook-error.log",
		fmt.Sprintf("[%s] %s", time.Now().Format(time.RFC3339), msg))
}
