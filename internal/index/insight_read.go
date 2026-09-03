package index

import (
	"database/sql"
	"errors"
	"time"

	"github.com/bonez-io/re_gent/internal/store"
)

// SessionRow is the sessions table row the worker needs.
type SessionRow struct {
	ID         string
	Origin     string
	StartedAt  time.Time
	LastSeenAt time.Time
}

// GetSession returns one session by id.
func (idx *DB) GetSession(id string) (SessionRow, bool, error) {
	var s SessionRow
	var started, seen int64
	err := idx.db.QueryRow(`SELECT id, origin, started_at, last_seen_at FROM sessions WHERE id = ?`, id).
		Scan(&s.ID, &s.Origin, &started, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionRow{}, false, nil
	}
	if err != nil {
		return SessionRow{}, false, err
	}
	s.StartedAt = time.Unix(0, started)
	s.LastSeenAt = time.Unix(0, seen)
	return s, true, nil
}

// ListSessionMessages returns a session's messages with seq greater than
// afterSeq, in order. Pass -1 for every message.
func (idx *DB) ListSessionMessages(sessionID string, afterSeq int, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 5000
	}
	rows, err := idx.db.Query(`
		SELECT id, session_id, step_id, turn_id, seq_num, timestamp, processed_at, message_type,
		       content_text, tool_name, tool_use_id, tool_input, tool_output
		FROM messages
		WHERE session_id = ? AND seq_num > ?
		ORDER BY seq_num ASC
		LIMIT ?
	`, sessionID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var messages []Message
	for rows.Next() {
		var msg Message
		var stepID, turnID, contentText, toolName, toolUseID, toolInput, toolOutput sql.NullString
		var processedAt sql.NullInt64
		if err := rows.Scan(&msg.ID, &msg.SessionID, &stepID, &turnID, &msg.SeqNum, &msg.Timestamp,
			&processedAt, &msg.MessageType, &contentText, &toolName, &toolUseID, &toolInput, &toolOutput); err != nil {
			return nil, err
		}
		msg.StepID, msg.TurnID, msg.ContentText = stepID.String, turnID.String, contentText.String
		msg.ToolName, msg.ToolUseID, msg.ToolInput, msg.ToolOutput = toolName.String, toolUseID.String, toolInput.String, toolOutput.String
		msg.ProcessedAt = processedAt.Int64
		messages = append(messages, msg)
	}
	return messages, rows.Err()
}

// StepSummary is the part of a step row the worker reads.
type StepSummary struct {
	ID        string
	ParentID  string
	TurnID    string
	Timestamp time.Time
	TreeHash  store.Hash
}

// GetStepSummary returns one step by id.
func (idx *DB) GetStepSummary(id string) (StepSummary, bool, error) {
	var s StepSummary
	var parent, turn sql.NullString
	var ts int64
	err := idx.db.QueryRow(`SELECT id, parent_id, turn_id, ts_nanos, tree_hash FROM steps WHERE id = ?`, id).
		Scan(&s.ID, &parent, &turn, &ts, &s.TreeHash)
	if errors.Is(err, sql.ErrNoRows) {
		return StepSummary{}, false, nil
	}
	if err != nil {
		return StepSummary{}, false, err
	}
	s.ParentID, s.TurnID, s.Timestamp = parent.String, turn.String, time.Unix(0, ts)
	return s, true, nil
}

// StepFiles returns the paths a step's tree holds, per step_files.
func (idx *DB) StepFiles(stepID string) ([]string, error) {
	rows, err := idx.db.Query(`SELECT path FROM step_files WHERE step_id = ? ORDER BY path`, stepID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}
