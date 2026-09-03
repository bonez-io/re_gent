package index

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Insight job states. A job is queued by a hook, claimed by the worker
// (running), and ends done or failed. A worker that dies mid-job leaves it
// running; ResetRunningInsightJobs returns those to queued on the next start.
const (
	InsightJobQueued  = "queued"
	InsightJobRunning = "running"
	InsightJobDone    = "done"
	InsightJobFailed  = "failed"
)

// Insight job kinds. A turn job names the session and, when the turn wrote
// one, the step a Stop hook just finalized. A session job asks the worker to
// read a whole session again from its first step, which is what `rgt insight
// rebuild` enqueues.
const (
	InsightJobKindTurn    = "turn"
	InsightJobKindSession = "session"
)

// InsightJob is one row of insight_jobs.
type InsightJob struct {
	ID        int64
	Kind      string
	SessionID string
	StepID    string
	TurnID    string
	State     string
	Attempts  int
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// EnqueueInsightJob inserts a job unless an identical one (same kind,
// session, step, and turn) is already queued or running, and reports whether
// a row was inserted. A Stop hook that fires twice for the same turn (a
// recovered turn is finalized again) therefore costs one job, not two.
func (idx *DB) EnqueueInsightJob(job InsightJob) (int64, bool, error) {
	if job.SessionID == "" {
		return 0, false, fmt.Errorf("session id is required")
	}
	if job.Kind == "" {
		job.Kind = InsightJobKindTurn
	}
	now := time.Now().UnixNano()

	tx, err := idx.db.Begin()
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback() }()

	var existing int64
	err = tx.QueryRow(`
		SELECT id FROM insight_jobs
		WHERE kind = ? AND session_id = ?
		  AND COALESCE(step_id, '') = ? AND COALESCE(turn_id, '') = ?
		  AND state IN (?, ?)
		LIMIT 1
	`, job.Kind, job.SessionID, job.StepID, job.TurnID, InsightJobQueued, InsightJobRunning).Scan(&existing)
	switch {
	case err == nil:
		return existing, false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, false, err
	}

	result, err := tx.Exec(`
		INSERT INTO insight_jobs (kind, session_id, step_id, turn_id, state, attempts, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 0, ?, ?)
	`, job.Kind, job.SessionID, nullString(job.StepID), nullString(job.TurnID), InsightJobQueued, now, now)
	if err != nil {
		return 0, false, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, false, err
	}
	return id, true, tx.Commit()
}

// ClaimInsightJob moves the next queued job to running, increments its
// attempt count, and returns it. The second value is false when the queue is
// empty. Fresh jobs go before retries (fewest attempts first, then oldest),
// so one job that keeps failing delays the queue by at most its own retries
// rather than blocking every job behind it. Claim and mark happen in one
// transaction so two workers cannot both take the same row; the lock file
// makes two workers unlikely, the transaction makes them harmless.
func (idx *DB) ClaimInsightJob() (InsightJob, bool, error) {
	tx, err := idx.db.Begin()
	if err != nil {
		return InsightJob{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	job, err := scanInsightJob(tx.QueryRow(`
		SELECT id, kind, session_id, step_id, turn_id, state, attempts, last_error, created_at, updated_at
		FROM insight_jobs
		WHERE state = ?
		ORDER BY attempts, id
		LIMIT 1
	`, InsightJobQueued))
	if errors.Is(err, sql.ErrNoRows) {
		return InsightJob{}, false, nil
	}
	if err != nil {
		return InsightJob{}, false, err
	}

	now := time.Now().UnixNano()
	if _, err := tx.Exec(`
		UPDATE insight_jobs SET state = ?, attempts = attempts + 1, updated_at = ? WHERE id = ?
	`, InsightJobRunning, now, job.ID); err != nil {
		return InsightJob{}, false, err
	}
	job.State = InsightJobRunning
	job.Attempts++
	job.UpdatedAt = time.Unix(0, now)
	return job, true, tx.Commit()
}

// CompleteInsightJob marks a job done.
func (idx *DB) CompleteInsightJob(id int64) error {
	_, err := idx.db.Exec(`
		UPDATE insight_jobs SET state = ?, last_error = NULL, updated_at = ? WHERE id = ?
	`, InsightJobDone, time.Now().UnixNano(), id)
	return err
}

// FailInsightJob records why a job failed. With retry, the job returns to the
// queue and keeps its attempt count; without it, the job is failed for good
// and stays visible to `rgt insight status` until a rebuild replaces it.
func (idx *DB) FailInsightJob(id int64, cause error, retry bool) error {
	state := InsightJobFailed
	if retry {
		state = InsightJobQueued
	}
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	_, err := idx.db.Exec(`
		UPDATE insight_jobs SET state = ?, last_error = ?, updated_at = ? WHERE id = ?
	`, state, nullString(msg), time.Now().UnixNano(), id)
	return err
}

// ResetRunningInsightJobs returns every running job to the queue. The worker
// calls it once at start, holding the lock, so a job a crashed worker left
// half-claimed is retried rather than stranded.
func (idx *DB) ResetRunningInsightJobs() (int64, error) {
	result, err := idx.db.Exec(`
		UPDATE insight_jobs SET state = ?, updated_at = ? WHERE state = ?
	`, InsightJobQueued, time.Now().UnixNano(), InsightJobRunning)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return n, nil
}

// InsightJobCounts returns how many jobs are in each state.
func (idx *DB) InsightJobCounts() (map[string]int, error) {
	rows, err := idx.db.Query(`SELECT state, COUNT(*) FROM insight_jobs GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	counts := map[string]int{}
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		counts[state] = n
	}
	return counts, rows.Err()
}

// LastFailedInsightJob returns the most recently failed job, if any, so
// status can show the last error without the user reading the log.
func (idx *DB) LastFailedInsightJob() (InsightJob, bool, error) {
	job, err := scanInsightJob(idx.db.QueryRow(`
		SELECT id, kind, session_id, step_id, turn_id, state, attempts, last_error, created_at, updated_at
		FROM insight_jobs
		WHERE state = ?
		ORDER BY updated_at DESC
		LIMIT 1
	`, InsightJobFailed))
	if errors.Is(err, sql.ErrNoRows) {
		return InsightJob{}, false, nil
	}
	if err != nil {
		return InsightJob{}, false, err
	}
	return job, true, nil
}

// ListInsightJobs returns jobs in a state, oldest first, up to limit.
func (idx *DB) ListInsightJobs(state string, limit int) ([]InsightJob, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := idx.db.Query(`
		SELECT id, kind, session_id, step_id, turn_id, state, attempts, last_error, created_at, updated_at
		FROM insight_jobs
		WHERE state = ?
		ORDER BY id
		LIMIT ?
	`, state, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var jobs []InsightJob
	for rows.Next() {
		job, err := scanInsightJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// EnqueueSessionInsightJobs queues a session job for every recorded session,
// dropping queued or failed jobs first so a rebuild starts from a clean
// queue. It returns how many sessions were queued.
func (idx *DB) EnqueueSessionInsightJobs() (int, error) {
	tx, err := idx.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM insight_jobs WHERE state IN (?, ?)`, InsightJobQueued, InsightJobFailed); err != nil {
		return 0, err
	}
	now := time.Now().UnixNano()
	result, err := tx.Exec(`
		INSERT INTO insight_jobs (kind, session_id, state, attempts, created_at, updated_at)
		SELECT ?, id, ?, 0, ?, ? FROM sessions ORDER BY started_at
	`, InsightJobKindSession, InsightJobQueued, now, now)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), tx.Commit()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanInsightJob(row rowScanner) (InsightJob, error) {
	var job InsightJob
	var stepID, turnID, lastError sql.NullString
	var createdAt, updatedAt int64
	if err := row.Scan(&job.ID, &job.Kind, &job.SessionID, &stepID, &turnID, &job.State,
		&job.Attempts, &lastError, &createdAt, &updatedAt); err != nil {
		return InsightJob{}, err
	}
	job.StepID = stepID.String
	job.TurnID = turnID.String
	job.LastError = lastError.String
	job.CreatedAt = time.Unix(0, createdAt)
	job.UpdatedAt = time.Unix(0, updatedAt)
	return job, nil
}

// InsightMeta reads one key of insight_meta; a missing key is "" and no error.
func (idx *DB) InsightMeta(key string) (string, error) {
	var value string
	err := idx.db.QueryRow(`SELECT value FROM insight_meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

// SetInsightMeta writes one key of insight_meta.
func (idx *DB) SetInsightMeta(key, value string) error {
	_, err := idx.db.Exec(`
		INSERT INTO insight_meta (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value
	`, key, value)
	return err
}

// RebuildInsightFTS rebuilds the three full-text indexes from their base
// tables. Triggers keep them current for rows written after the schema
// existed; this reaches the rows written before it, and any rows a VACUUM
// renumbered (external-content FTS is keyed by rowid).
func (idx *DB) RebuildInsightFTS() error {
	for _, table := range []string{"messages_fts", "work_items_fts", "entities_fts"} {
		if _, err := idx.db.Exec(fmt.Sprintf(`INSERT INTO %s(%s) VALUES ('rebuild')`, table, table)); err != nil {
			return fmt.Errorf("rebuild %s: %w", table, err)
		}
	}
	return idx.SetInsightMeta("fts_rebuilt_at", time.Now().UTC().Format(time.RFC3339))
}

// InsightCoverage says how much of the recorded data the derived layer has
// reached. It is what `rgt insight status` prints so a reader can tell "no
// results" from "not indexed yet".
type InsightCoverage struct {
	Messages        int
	MessagesIndexed int
	Sessions        int
	WorkItems       int
	// WorkItemsEmbedded counts work items with at least one vector, under
	// any provider; Embeddings counts vectors.
	WorkItemsEmbedded int
	Entities          int
	Embeddings        int
}

// InsightCoverage counts base rows against derived rows.
func (idx *DB) InsightCoverage() (InsightCoverage, error) {
	var c InsightCoverage
	counts := []struct {
		query string
		dest  *int
	}{
		{`SELECT COUNT(*) FROM messages`, &c.Messages},
		// External-content FTS5 answers COUNT(*) from the content table, so the
		// indexed count has to come from the docsize shadow table, which holds
		// one row per document the index actually contains.
		{`SELECT COUNT(*) FROM messages_fts_docsize`, &c.MessagesIndexed},
		{`SELECT COUNT(*) FROM sessions`, &c.Sessions},
		{`SELECT COUNT(*) FROM work_items`, &c.WorkItems},
		{`SELECT COUNT(DISTINCT owner_id) FROM embeddings WHERE owner_kind = 'work_item'`, &c.WorkItemsEmbedded},
		{`SELECT COUNT(*) FROM entities`, &c.Entities},
		{`SELECT COUNT(*) FROM embeddings`, &c.Embeddings},
	}
	for _, q := range counts {
		if err := idx.db.QueryRow(q.query).Scan(q.dest); err != nil {
			return InsightCoverage{}, fmt.Errorf("%s: %w", strings.TrimPrefix(q.query, "SELECT COUNT(*) FROM "), err)
		}
	}
	return c, nil
}
