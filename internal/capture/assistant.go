package capture

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bonez-io/re_gent/internal/index"
	"github.com/bonez-io/re_gent/internal/jsonl"
	"lukechampine.com/blake3"
)

// maxTranscriptScanBytes bounds the transcript we are willing to re-scan while a
// hook is holding up a live agent turn. Past this size we skip the scan and log
// rather than stall the agent.
const maxTranscriptScanBytes = 64 << 20

// captureAssistantTranscript records every assistant text and reasoning block in
// the host transcript that is not already stored for this session.
//
// The host hooks only hand us the *final* assistant message of a turn, so the
// narration and reasoning the agent produced between tool calls would otherwise
// be lost. Reading the transcript recovers it, and because each entry gets a
// deterministic id (see assistantMessageID) the scan is idempotent: replaying it
// on every tool batch and again at Stop stores each block exactly once.
//
// Robustness to transcript churn comes from doing this eagerly. Once a block is
// in the index it survives a later /compact or /clear rewriting or truncating
// the JSONL, because we never delete rows and never re-derive them from the
// current file.
//
// Errors are returned for logging only — capture must never fail a turn.
func (r *Recorder) captureAssistantTranscript(session SessionMetadata, scope turnScope) (int, error) {
	if session.TranscriptPath == "" {
		return 0, nil
	}

	info, err := os.Stat(session.TranscriptPath)
	if err != nil {
		// The transcript may not exist yet, or may have been rotated away by
		// /clear. Nothing to add; previously captured rows are unaffected.
		return 0, nil
	}
	if info.Size() > maxTranscriptScanBytes {
		return 0, fmt.Errorf("skipped transcript scan: %s is %d bytes (limit %d)",
			session.TranscriptPath, info.Size(), maxTranscriptScanBytes)
	}

	// A partial read still yields usable entries, so keep them and report the
	// error alongside whatever we managed to store.
	entries, scanErr := jsonl.ExtractAssistantEntries(session.TranscriptPath)

	problems := make([]error, 0, 1)
	if scanErr != nil {
		problems = append(problems, scanErr)
	}

	inserted := 0
	now := time.Now().UnixNano()
	for i, entry := range entries {
		added, err := r.Index.AppendMessageIfNew(index.Message{
			ID:          assistantMessageID(session.SessionID, scope.id, entry),
			SessionID:   session.SessionID,
			TurnID:      scope.id,
			Timestamp:   now + int64(i),
			MessageType: entry.Kind,
			ContentText: entry.Text,
		})
		if err != nil {
			problems = append(problems, fmt.Errorf("append %s message: %w", entry.Kind, err))
			continue
		}
		if added {
			inserted++
		}
	}

	return inserted, errors.Join(problems...)
}

// captureAssistantTranscriptOnce runs captureAssistantTranscript at most once per
// (session, turn, transcript) for the lifetime of this Recorder. A tool batch
// arrives as several RecordToolUse calls in one hook process, and they all see
// the same transcript, so one scan per process is enough.
func (r *Recorder) captureAssistantTranscriptOnce(session SessionMetadata, scope turnScope) (int, error) {
	key := strings.Join([]string{session.SessionID, scope.id, session.TranscriptPath}, "\x00")
	if r.scannedTranscripts == nil {
		r.scannedTranscripts = map[string]struct{}{}
	}
	if _, done := r.scannedTranscripts[key]; done {
		return 0, nil
	}
	r.scannedTranscripts[key] = struct{}{}

	return r.captureAssistantTranscript(session, scope)
}

// logAssistantTranscript folds the outcome of a transcript capture into the hook
// logs. Nothing is propagated to the caller: losing reasoning text must never
// cost the user their step.
func (r *Recorder) logAssistantTranscript(inserted int, err error) {
	if err != nil {
		LogHookError(r.Store.Root, fmt.Sprintf("capture assistant transcript: %v", err))
	}
	if inserted > 0 {
		logDebug(r.Store, fmt.Sprintf("captured %d assistant/reasoning message(s) from transcript", inserted))
	}
}

// assistantMessageID derives a stable message id for a transcript block. The
// digest covers the block's identity (session, turn, host uuid, position) and its
// text, so re-scanning an unchanged transcript produces the same id — which the
// messages primary key then dedupes. Hosts that omit a uuid degrade gracefully to
// a content-addressed id within the turn.
func assistantMessageID(sessionID, turnID string, entry jsonl.AssistantEntry) string {
	parts := []string{sessionID, turnID, entry.UUID, strconv.Itoa(entry.Index), entry.Kind, entry.Text}
	digest := blake3.Sum256([]byte(strings.Join(parts, "\x00")))
	return fmt.Sprintf("msg_%s_%s", entry.Kind, hex.EncodeToString(digest[:16]))
}
