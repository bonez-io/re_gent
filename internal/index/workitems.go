package index

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/bonez-io/re_gent/internal/store"
)

// Work item statuses: the one closed enum in RFC 0007.
const (
	WorkItemWIP        = "wip"
	WorkItemDone       = "done"
	WorkItemFailed     = "failed"
	WorkItemAbandoned  = "abandoned"
	WorkItemSuperseded = "superseded"
)

// ValidWorkItemStatus reports whether s is one of the five statuses.
func ValidWorkItemStatus(s string) bool {
	switch s {
	case WorkItemWIP, WorkItemDone, WorkItemFailed, WorkItemAbandoned, WorkItemSuperseded:
		return true
	}
	return false
}

// Link roles and sources.
const (
	LinkSourceDeterministic = "deterministic"
	LinkSourceModel         = "model"
)

// WorkItem is one row of work_items. An item is open while Status is wip
// and EndTS is zero: the next turn of its session extends it. Closing sets
// EndTS, whether the model closed it with a final status or the idle rule
// closed it as wip.
type WorkItem struct {
	ID                  string
	SessionID           string
	Origin              string
	StartStepID         string
	EndStepID           string
	StartTS             time.Time
	EndTS               time.Time
	Goal                string
	Approach            string
	Outcome             string
	Status              string
	ContinuesWorkItemID string
	ModelProvider       string
	ModelName           string
	PromptVersion       string
	UpdatedAt           time.Time
}

// Open reports whether the next turn of the session extends this item.
func (w WorkItem) Open() bool { return w.Status == WorkItemWIP && w.EndTS.IsZero() }

// Entity is one row of entities.
type Entity struct {
	ID   string
	Type string
	Name string
	Ref  string
}

// EntityLink is one row of work_item_entities joined with its entity.
type EntityLink struct {
	Entity
	WorkItemID     string
	Role           string
	Confidence     float64
	Source         string
	EvidenceStepID string
}

// LinkWrite is one link to write with a work item. The entity is resolved
// or created by the writer.
type LinkWrite struct {
	Type           string
	Name           string
	Ref            string
	Role           string
	Confidence     float64
	Source         string
	EvidenceStepID string
}

// WorkItemWrite is one work item with everything that hangs off it.
type WorkItemWrite struct {
	Item    WorkItem
	StepIDs []string
	// Files are the paths the item's steps changed against their parents.
	// step_files holds each step's whole tree, so "touched" has to be
	// derived from the diff at write time; it is deterministic and needs no
	// model.
	Files []string
	Links []LinkWrite
	// Vector, when non-nil, is stored under (EmbedProvider, EmbedModel).
	Vector        []float32
	EmbedProvider string
	EmbedModel    string
}

// InsightWrite is everything one job produces, written in one transaction so
// a work item is only ever seen whole.
type InsightWrite struct {
	// ReplaceSession, when set, first deletes every work item of that
	// session (a rebuild reads the session from its first step).
	ReplaceSession string
	Items          []WorkItemWrite
	// Cursor advances the session's read position to this message seq.
	CursorSession string
	CursorSeq     int
}

// WriteInsight applies w atomically.
func (idx *DB) WriteInsight(w InsightWrite) error {
	tx, err := idx.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if w.ReplaceSession != "" {
		if err := deleteWorkItemsForSession(tx, w.ReplaceSession); err != nil {
			return err
		}
	}
	now := time.Now().UnixNano()
	for _, item := range w.Items {
		if err := writeWorkItem(tx, item, now); err != nil {
			return err
		}
	}
	if w.CursorSession != "" {
		if _, err := tx.Exec(`
			INSERT INTO insight_cursors (session_id, last_message_seq, updated_at) VALUES (?, ?, ?)
			ON CONFLICT (session_id) DO UPDATE SET last_message_seq = excluded.last_message_seq, updated_at = excluded.updated_at
		`, w.CursorSession, w.CursorSeq, now); err != nil {
			return fmt.Errorf("advance cursor: %w", err)
		}
	}
	return tx.Commit()
}

func deleteWorkItemsForSession(tx *sql.Tx, sessionID string) error {
	for _, stmt := range []string{
		`DELETE FROM work_item_entities WHERE work_item_id IN (SELECT id FROM work_items WHERE session_id = ?)`,
		`DELETE FROM work_item_steps WHERE work_item_id IN (SELECT id FROM work_items WHERE session_id = ?)`,
		`DELETE FROM work_item_files WHERE work_item_id IN (SELECT id FROM work_items WHERE session_id = ?)`,
		`DELETE FROM embeddings WHERE owner_kind = 'work_item' AND owner_id IN (SELECT id FROM work_items WHERE session_id = ?)`,
		`DELETE FROM work_items WHERE session_id = ?`,
	} {
		if _, err := tx.Exec(stmt, sessionID); err != nil {
			return fmt.Errorf("replace session %s: %w", sessionID, err)
		}
	}
	return nil
}

func writeWorkItem(tx *sql.Tx, w WorkItemWrite, now int64) error {
	item := w.Item
	if item.ID == "" || item.SessionID == "" {
		return errors.New("work item needs an id and a session")
	}
	if !ValidWorkItemStatus(item.Status) {
		return fmt.Errorf("work item %s: status %q is not one of wip, done, failed, abandoned, superseded", item.ID, item.Status)
	}
	var endTS, startTS any
	startTS = item.StartTS.UnixNano()
	if !item.EndTS.IsZero() {
		endTS = item.EndTS.UnixNano()
	}
	if _, err := tx.Exec(`
		INSERT INTO work_items (id, session_id, origin, start_step_id, end_step_id, start_ts, end_ts,
		                        goal, approach, outcome, status, continues_work_item_id,
		                        model_provider, model_name, prompt_version, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			end_step_id = excluded.end_step_id, end_ts = excluded.end_ts,
			goal = excluded.goal, approach = excluded.approach, outcome = excluded.outcome,
			status = excluded.status, continues_work_item_id = excluded.continues_work_item_id,
			model_provider = excluded.model_provider, model_name = excluded.model_name,
			prompt_version = excluded.prompt_version, updated_at = excluded.updated_at
	`, item.ID, item.SessionID, item.Origin, nullString(item.StartStepID), nullString(item.EndStepID),
		startTS, endTS, item.Goal, item.Approach, item.Outcome, item.Status,
		nullString(item.ContinuesWorkItemID), nullString(item.ModelProvider), nullString(item.ModelName),
		nullString(item.PromptVersion), now); err != nil {
		return fmt.Errorf("write work item %s: %w", item.ID, err)
	}

	for _, stepID := range w.StepIDs {
		if stepID == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO work_item_steps (work_item_id, step_id) VALUES (?, ?)`, item.ID, stepID); err != nil {
			return fmt.Errorf("link step: %w", err)
		}
	}

	for _, path := range w.Files {
		if path == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO work_item_files (work_item_id, path) VALUES (?, ?)`, item.ID, path); err != nil {
			return fmt.Errorf("link file: %w", err)
		}
	}

	for _, link := range w.Links {
		entityID, err := upsertEntity(tx, link.Type, link.Name, link.Ref)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO work_item_entities (work_item_id, entity_id, role, confidence, source, evidence_step_id)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (work_item_id, entity_id, role) DO UPDATE SET
				confidence = MAX(confidence, excluded.confidence),
				source = CASE WHEN source = 'deterministic' THEN source ELSE excluded.source END,
				evidence_step_id = excluded.evidence_step_id
		`, item.ID, entityID, link.Role, link.Confidence, link.Source, link.EvidenceStepID); err != nil {
			return fmt.Errorf("link entity %s: %w", link.Name, err)
		}
	}

	if w.Vector != nil {
		if err := writeEmbedding(tx, "work_item", item.ID, w.EmbedProvider, w.EmbedModel, w.Vector, now); err != nil {
			return err
		}
	}
	return nil
}

// NormalizeEntityType lowercases and snake_cases a type the model chose, so
// "Pull Request", "pull-request", and "pull_request" are one type.
func NormalizeEntityType(t string) string {
	t = strings.TrimSpace(strings.ToLower(t))
	var b strings.Builder
	lastUnderscore := false
	for _, r := range t {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.TrimRight(b.String(), "_")
}

// upsertEntity finds an entity by (type, ref) when ref is set, else by
// (type, lower(name)), and creates it when absent.
func upsertEntity(tx *sql.Tx, typ, name, ref string) (string, error) {
	typ = NormalizeEntityType(typ)
	name = strings.TrimSpace(name)
	ref = strings.TrimSpace(ref)
	if typ == "" || name == "" {
		return "", errors.New("entity needs a type and a name")
	}
	var id string
	var err error
	if ref != "" {
		err = tx.QueryRow(`SELECT id FROM entities WHERE type = ? AND ref = ?`, typ, ref).Scan(&id)
	} else {
		err = tx.QueryRow(`SELECT id FROM entities WHERE type = ? AND lower(name) = lower(?) LIMIT 1`, typ, name).Scan(&id)
	}
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	key := typ + "\x00" + ref
	if ref == "" {
		key = typ + "\x00\x00" + strings.ToLower(name)
	}
	id = "ent_" + string(store.HashBytes([]byte(key)))[:24]
	if _, err := tx.Exec(`INSERT OR IGNORE INTO entities (id, type, name, ref) VALUES (?, ?, ?, ?)`, id, typ, name, nullString(ref)); err != nil {
		return "", fmt.Errorf("create entity %s: %w", name, err)
	}
	return id, nil
}

func writeEmbedding(tx *sql.Tx, kind, id, provider, model string, vector []float32, now int64) error {
	if provider == "" || model == "" {
		return errors.New("embedding needs a provider and a model")
	}
	_, err := tx.Exec(`
		INSERT INTO embeddings (owner_kind, owner_id, provider, model, dim, vector, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (owner_kind, owner_id, provider, model) DO UPDATE SET
			dim = excluded.dim, vector = excluded.vector, updated_at = excluded.updated_at
	`, kind, id, provider, model, len(vector), EncodeVector(vector), now)
	if err != nil {
		return fmt.Errorf("write embedding: %w", err)
	}
	return nil
}

// EncodeVector renders a vector as little-endian float32 bytes.
func EncodeVector(v []float32) []byte {
	out := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(out[4*i:], math.Float32bits(f))
	}
	return out
}

// DecodeVector reads what EncodeVector wrote.
func DecodeVector(b []byte) []float32 {
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return out
}

// InsightCursor returns the last message seq the worker read for a session,
// or -1 when nothing has been read.
func (idx *DB) InsightCursor(sessionID string) (int, error) {
	var seq int
	err := idx.db.QueryRow(`SELECT last_message_seq FROM insight_cursors WHERE session_id = ?`, sessionID).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return -1, nil
	}
	return seq, err
}

// ClearInsightCursor forgets a session's read position, so a rebuild reads
// it from the start.
func (idx *DB) ClearInsightCursor(sessionID string) error {
	_, err := idx.db.Exec(`DELETE FROM insight_cursors WHERE session_id = ?`, sessionID)
	return err
}

const workItemColumns = `id, session_id, origin, start_step_id, end_step_id, start_ts, end_ts,
	goal, approach, outcome, status, continues_work_item_id, model_provider, model_name, prompt_version, updated_at`

func scanWorkItem(row rowScanner) (WorkItem, error) {
	var w WorkItem
	var startStep, endStep, continues, provider, model, version sql.NullString
	var startTS, updated int64
	var endTS sql.NullInt64
	if err := row.Scan(&w.ID, &w.SessionID, &w.Origin, &startStep, &endStep, &startTS, &endTS,
		&w.Goal, &w.Approach, &w.Outcome, &w.Status, &continues, &provider, &model, &version, &updated); err != nil {
		return WorkItem{}, err
	}
	w.StartStepID, w.EndStepID, w.ContinuesWorkItemID = startStep.String, endStep.String, continues.String
	w.ModelProvider, w.ModelName, w.PromptVersion = provider.String, model.String, version.String
	w.StartTS = time.Unix(0, startTS)
	if endTS.Valid {
		w.EndTS = time.Unix(0, endTS.Int64)
	}
	w.UpdatedAt = time.Unix(0, updated)
	return w, nil
}

// GetWorkItem returns one work item by id or unique id prefix.
func (idx *DB) GetWorkItem(id string) (WorkItem, bool, error) {
	rows, err := idx.db.Query(`SELECT `+workItemColumns+` FROM work_items WHERE id = ? OR id LIKE ? LIMIT 2`, id, id+"%")
	if err != nil {
		return WorkItem{}, false, err
	}
	defer func() { _ = rows.Close() }()
	var items []WorkItem
	for rows.Next() {
		w, err := scanWorkItem(rows)
		if err != nil {
			return WorkItem{}, false, err
		}
		items = append(items, w)
	}
	if err := rows.Err(); err != nil {
		return WorkItem{}, false, err
	}
	switch len(items) {
	case 0:
		return WorkItem{}, false, nil
	case 1:
		return items[0], true, nil
	}
	for _, w := range items {
		if w.ID == id {
			return w, true, nil
		}
	}
	return WorkItem{}, false, fmt.Errorf("work item id %q is ambiguous", id)
}

// OpenWorkItem returns the session's open item, if any.
func (idx *DB) OpenWorkItem(sessionID string) (WorkItem, bool, error) {
	w, err := scanWorkItem(idx.db.QueryRow(`
		SELECT `+workItemColumns+` FROM work_items
		WHERE session_id = ? AND status = ? AND end_ts IS NULL
		ORDER BY start_ts DESC LIMIT 1
	`, sessionID, WorkItemWIP))
	if errors.Is(err, sql.ErrNoRows) {
		return WorkItem{}, false, nil
	}
	return w, err == nil, err
}

// CloseWorkItem sets an item's end without changing its status: the idle
// rule closing an open item as wip.
func (idx *DB) CloseWorkItem(id, endStepID string, endTS time.Time) error {
	_, err := idx.db.Exec(`
		UPDATE work_items SET end_step_id = COALESCE(?, end_step_id), end_ts = ?, updated_at = ? WHERE id = ?
	`, nullString(endStepID), endTS.UnixNano(), time.Now().UnixNano(), id)
	return err
}

// WorkItemFilter narrows ListWorkItems.
type WorkItemFilter struct {
	Status    string
	SessionID string
	Limit     int
}

// ListWorkItems returns work items newest first.
func (idx *DB) ListWorkItems(f WorkItemFilter) ([]WorkItem, error) {
	query := `SELECT ` + workItemColumns + ` FROM work_items WHERE 1=1`
	var args []any
	if f.Status != "" {
		query += ` AND status = ?`
		args = append(args, f.Status)
	}
	if f.SessionID != "" {
		query += ` AND session_id = ?`
		args = append(args, f.SessionID)
	}
	query += ` ORDER BY start_ts DESC`
	if f.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := idx.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var items []WorkItem
	for rows.Next() {
		w, err := scanWorkItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, w)
	}
	return items, rows.Err()
}

// ResumableWorkItems returns wip items from other sessions, most recently
// touched first, for the model to recognise a continuation.
func (idx *DB) ResumableWorkItems(excludeSession string, limit int) ([]WorkItem, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := idx.db.Query(`
		SELECT `+workItemColumns+` FROM work_items
		WHERE status = ? AND session_id != ?
		ORDER BY updated_at DESC LIMIT ?
	`, WorkItemWIP, excludeSession, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var items []WorkItem
	for rows.Next() {
		w, err := scanWorkItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, w)
	}
	return items, rows.Err()
}

// WorkItemLinks returns the entities linked to a work item.
func (idx *DB) WorkItemLinks(workItemID string) ([]EntityLink, error) {
	rows, err := idx.db.Query(`
		SELECT e.id, e.type, e.name, COALESCE(e.ref, ''), l.work_item_id, l.role, l.confidence, l.source, l.evidence_step_id
		FROM work_item_entities l JOIN entities e ON e.id = l.entity_id
		WHERE l.work_item_id = ?
		ORDER BY l.confidence DESC, e.type, e.name
	`, workItemID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var links []EntityLink
	for rows.Next() {
		var l EntityLink
		if err := rows.Scan(&l.ID, &l.Type, &l.Name, &l.Ref, &l.WorkItemID, &l.Role, &l.Confidence, &l.Source, &l.EvidenceStepID); err != nil {
			return nil, err
		}
		links = append(links, l)
	}
	return links, rows.Err()
}

// WorkItemSteps returns the step ids of a work item in recorded order.
func (idx *DB) WorkItemSteps(workItemID string) ([]string, error) {
	rows, err := idx.db.Query(`
		SELECT ws.step_id FROM work_item_steps ws
		LEFT JOIN steps s ON s.id = ws.step_id
		WHERE ws.work_item_id = ?
		ORDER BY s.ts_nanos, ws.step_id
	`, workItemID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var steps []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		steps = append(steps, id)
	}
	return steps, rows.Err()
}

// WorkItemFiles returns the files the work item's steps changed.
func (idx *DB) WorkItemFiles(workItemID string) ([]string, error) {
	rows, err := idx.db.Query(`SELECT path FROM work_item_files WHERE work_item_id = ? ORDER BY path`, workItemID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var files []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		files = append(files, p)
	}
	return files, rows.Err()
}

// WorkItemsForFile returns the ids of work items that changed a path, most
// recently started first.
func (idx *DB) WorkItemsForFile(path string) ([]string, error) {
	rows, err := idx.db.Query(`
		SELECT f.work_item_id FROM work_item_files f JOIN work_items w ON w.id = f.work_item_id
		WHERE f.path = ? ORDER BY w.start_ts DESC
	`, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// WorkItemsForSteps maps step ids to the work items containing them.
func (idx *DB) WorkItemsForSteps(stepIDs []string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, stepID := range stepIDs {
		rows, err := idx.db.Query(`SELECT work_item_id FROM work_item_steps WHERE step_id = ?`, stepID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out[stepID] = append(out[stepID], id)
		}
		_ = rows.Close()
	}
	return out, nil
}

// EntityTypesInUse lists the entity types already present, most used first,
// so the prompt can ask the model to reuse them.
func (idx *DB) EntityTypesInUse(limit int) ([]string, error) {
	if limit <= 0 {
		limit = 30
	}
	rows, err := idx.db.Query(`SELECT type FROM entities GROUP BY type ORDER BY COUNT(*) DESC, type LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var types []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		types = append(types, t)
	}
	return types, rows.Err()
}

// FindEntities matches --entity arguments: ref exactly, else name prefix,
// else full-text over name and ref.
func (idx *DB) FindEntities(query string, limit int) ([]Entity, error) {
	if limit <= 0 {
		limit = 20
	}
	typ, rest := "", query
	if i := strings.Index(query, ":"); i > 0 && !strings.Contains(query[:i], "/") && !strings.HasPrefix(query, "http") {
		typ, rest = NormalizeEntityType(query[:i]), query[i+1:]
	}
	scan := func(rows *sql.Rows) ([]Entity, error) {
		defer func() { _ = rows.Close() }()
		var out []Entity
		for rows.Next() {
			var e Entity
			var ref sql.NullString
			if err := rows.Scan(&e.ID, &e.Type, &e.Name, &ref); err != nil {
				return nil, err
			}
			e.Ref = ref.String
			out = append(out, e)
		}
		return out, rows.Err()
	}
	typeClause, args := "", []any{}
	if typ != "" {
		typeClause = " AND type = ?"
		args = append(args, typ)
	}
	rows, err := idx.db.Query(`SELECT id, type, name, ref FROM entities WHERE ref = ?`+typeClause+` LIMIT ?`, append(append([]any{rest}, args...), limit)...)
	if err != nil {
		return nil, err
	}
	if out, err := scan(rows); err != nil || len(out) > 0 {
		return out, err
	}
	rows, err = idx.db.Query(`SELECT id, type, name, ref FROM entities WHERE lower(name) LIKE lower(?)`+typeClause+` ORDER BY name LIMIT ?`, append(append([]any{rest + "%"}, args...), limit)...)
	if err != nil {
		return nil, err
	}
	if out, err := scan(rows); err != nil || len(out) > 0 {
		return out, err
	}
	rows, err = idx.db.Query(`
		SELECT e.id, e.type, e.name, e.ref FROM entities_fts f JOIN entities e ON e.rowid = f.rowid
		WHERE entities_fts MATCH ?`+strings.Replace(typeClause, "type", "e.type", 1)+` ORDER BY rank LIMIT ?`,
		append(append([]any{ftsQuery(rest)}, args...), limit)...)
	if err != nil {
		return nil, err
	}
	return scan(rows)
}

// WorkItemsForEntity returns the ids of work items linked to an entity.
func (idx *DB) WorkItemsForEntity(entityID string) ([]string, error) {
	rows, err := idx.db.Query(`SELECT DISTINCT work_item_id FROM work_item_entities WHERE entity_id = ?`, entityID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// FTSHit is one full-text match with its BM25 rank (lower is better).
type FTSHit struct {
	ID   string
	Rank float64
}

// MessageHit is a full-text match on a message, with enough context to show
// a session that has no work item yet.
type MessageHit struct {
	MessageID string
	SessionID string
	StepID    string
	TurnID    string
	SeqNum    int
	Timestamp time.Time
	Snippet   string
	Rank      float64
}

// WorkItemsCovering returns the ids of the session's work items whose span
// contains ts, an open item counting as unbounded. It is how a message with
// no step (a turn that used no tools) is mapped to the item that read it.
func (idx *DB) WorkItemsCovering(sessionID string, ts time.Time) ([]string, error) {
	rows, err := idx.db.Query(`
		SELECT id FROM work_items
		WHERE session_id = ? AND start_ts <= ? AND (end_ts IS NULL OR end_ts >= ?)
		ORDER BY start_ts DESC
	`, sessionID, ts.UnixNano(), ts.UnixNano())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ftsQuery turns free text into an FTS5 query: every word a quoted token, so
// punctuation in the user's text cannot be read as FTS syntax; words are
// joined with OR and matched by prefix, so "flaky uploads" still finds
// "the uploader is flaky" and BM25 ranks the rows that match more words
// higher. An AND of exact words is the wrong default for a search box: one
// word the writer did not use would hide everything.
func ftsQuery(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return `""`
	}
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		token := `"` + strings.ReplaceAll(f, `"`, `""`) + `"`
		if len([]rune(f)) >= 3 {
			token += "*"
		}
		parts = append(parts, token)
	}
	return strings.Join(parts, " OR ")
}

// SearchWorkItemsFTS matches goal, approach, and outcome.
func (idx *DB) SearchWorkItemsFTS(text string, limit int) ([]FTSHit, error) {
	rows, err := idx.db.Query(`
		SELECT w.id, rank FROM work_items_fts f JOIN work_items w ON w.rowid = f.rowid
		WHERE work_items_fts MATCH ? ORDER BY rank LIMIT ?
	`, ftsQuery(text), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var hits []FTSHit
	for rows.Next() {
		var h FTSHit
		if err := rows.Scan(&h.ID, &h.Rank); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// SearchEntitiesFTS matches entity names and refs and returns the work
// items linked to the matching entities, best entity match first.
func (idx *DB) SearchEntitiesFTS(text string, limit int) ([]FTSHit, error) {
	rows, err := idx.db.Query(`
		SELECT l.work_item_id, MIN(rank) FROM entities_fts f
		JOIN entities e ON e.rowid = f.rowid
		JOIN work_item_entities l ON l.entity_id = e.id
		WHERE entities_fts MATCH ? GROUP BY l.work_item_id ORDER BY MIN(rank) LIMIT ?
	`, ftsQuery(text), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var hits []FTSHit
	for rows.Next() {
		var h FTSHit
		if err := rows.Scan(&h.ID, &h.Rank); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// SearchMessagesFTS matches recorded message text.
func (idx *DB) SearchMessagesFTS(text string, limit int) ([]MessageHit, error) {
	rows, err := idx.db.Query(`
		SELECT m.id, m.session_id, COALESCE(m.step_id, ''), COALESCE(m.turn_id, ''), m.seq_num, m.timestamp,
		       snippet(messages_fts, 0, '', '', '…', 12), rank
		FROM messages_fts f JOIN messages m ON m.rowid = f.rowid
		WHERE messages_fts MATCH ? ORDER BY rank LIMIT ?
	`, ftsQuery(text), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var hits []MessageHit
	for rows.Next() {
		var h MessageHit
		var ts int64
		if err := rows.Scan(&h.MessageID, &h.SessionID, &h.StepID, &h.TurnID, &h.SeqNum, &ts, &h.Snippet, &h.Rank); err != nil {
			return nil, err
		}
		h.Timestamp = time.Unix(0, ts)
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// StoredEmbedding is one vector with its owner.
type StoredEmbedding struct {
	OwnerID string
	Vector  []float32
}

// Embeddings returns every stored vector of one kind under one provider and
// model, for a brute-force cosine scan.
func (idx *DB) Embeddings(kind, provider, model string) ([]StoredEmbedding, error) {
	rows, err := idx.db.Query(`SELECT owner_id, vector FROM embeddings WHERE owner_kind = ? AND provider = ? AND model = ?`, kind, provider, model)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []StoredEmbedding
	for rows.Next() {
		var e StoredEmbedding
		var blob []byte
		if err := rows.Scan(&e.OwnerID, &blob); err != nil {
			return nil, err
		}
		e.Vector = DecodeVector(blob)
		out = append(out, e)
	}
	return out, rows.Err()
}

// WorkItemsMissingEmbedding lists work items with no vector under the given
// provider and model, so a newly configured embedder can catch up.
func (idx *DB) WorkItemsMissingEmbedding(provider, model string, limit int) ([]WorkItem, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := idx.db.Query(`
		SELECT `+workItemColumns+` FROM work_items w
		WHERE NOT EXISTS (
			SELECT 1 FROM embeddings e WHERE e.owner_kind = 'work_item' AND e.owner_id = w.id AND e.provider = ? AND e.model = ?
		) ORDER BY updated_at DESC LIMIT ?
	`, provider, model, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var items []WorkItem
	for rows.Next() {
		w, err := scanWorkItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, w)
	}
	return items, rows.Err()
}

// WriteEmbedding stores one vector outside a work-item write.
func (idx *DB) WriteEmbedding(kind, id, provider, model string, vector []float32) error {
	tx, err := idx.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := writeEmbedding(tx, kind, id, provider, model, vector, time.Now().UnixNano()); err != nil {
		return err
	}
	return tx.Commit()
}
