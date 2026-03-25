package workflow

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS runs (
	id              TEXT PRIMARY KEY,
	workflow_name   TEXT NOT NULL,
	workflow_version INTEGER NOT NULL,
	status          TEXT NOT NULL DEFAULT 'pending',
	input           TEXT,
	output          TEXT,
	error           TEXT,
	created_at      DATETIME NOT NULL DEFAULT (datetime('now')),
	updated_at      DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS step_results (
	id          TEXT PRIMARY KEY,
	run_id      TEXT NOT NULL REFERENCES runs(id),
	seq         INTEGER NOT NULL,
	step_name   TEXT NOT NULL,
	attempt     INTEGER NOT NULL DEFAULT 1,
	status      TEXT NOT NULL,
	next_step   TEXT NOT NULL DEFAULT '',
	input       TEXT,
	output      TEXT,
	error       TEXT,
	duration_ms INTEGER,
	created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS run_tags (
	run_id TEXT NOT NULL REFERENCES runs(id),
	key    TEXT NOT NULL,
	value  TEXT NOT NULL,
	PRIMARY KEY (run_id, key)
);

CREATE INDEX IF NOT EXISTS idx_runs_status   ON runs(status);
CREATE INDEX IF NOT EXISTS idx_runs_workflow ON runs(workflow_name);
CREATE INDEX IF NOT EXISTS idx_steps_run     ON step_results(run_id);
CREATE INDEX IF NOT EXISTS idx_steps_run_seq ON step_results(run_id, seq);
`

// SQLiteEngine is the concrete Engine backed by a SQLite database.
type SQLiteEngine struct {
	db        *sql.DB
	mu        sync.RWMutex
	workflows map[string]Workflow
}

// Open creates a new SQLiteEngine. Pass a file path for durable storage,
// or ":memory:" for an in-memory database (useful for tests).
func Open(dsn string) (*SQLiteEngine, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("kodomo: open db: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("kodomo: set WAL: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("kodomo: migrate: %w", err)
	}
	return &SQLiteEngine{db: db, workflows: make(map[string]Workflow)}, nil
}

func (e *SQLiteEngine) Register(w Workflow) error {
	if w.Name == "" {
		return fmt.Errorf("kodomo: workflow name is required")
	}
	if len(w.Steps) == 0 {
		return fmt.Errorf("kodomo: workflow %q has no steps", w.Name)
	}
	if w.Start == "" {
		return fmt.Errorf("kodomo: workflow %q has no start step", w.Name)
	}
	stepMap := buildStepMap(w.Steps)
	if _, ok := stepMap[w.Start]; !ok {
		return fmt.Errorf("kodomo: workflow %q start step %q not found in steps", w.Name, w.Start)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.workflows[w.Name] = w
	return nil
}

func (e *SQLiteEngine) Start(ctx context.Context, workflowName string, input json.RawMessage, opts *StartOpts) (string, error) {
	e.mu.RLock()
	wf, ok := e.workflows[workflowName]
	e.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("kodomo: unknown workflow %q", workflowName)
	}

	runID := newID()
	now := time.Now().UTC()
	_, err := e.db.ExecContext(ctx,
		`INSERT INTO runs (id, workflow_name, workflow_version, status, input, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		runID, wf.Name, wf.Version, StatusRunning, string(input), now, now,
	)
	if err != nil {
		return "", fmt.Errorf("kodomo: insert run: %w", err)
	}

	if opts != nil {
		e.saveTags(runID, opts.Tags)
	}

	if err := e.execute(ctx, runID, wf, wf.Start, input); err != nil {
		return runID, err
	}
	return runID, nil
}

func (e *SQLiteEngine) Resume(ctx context.Context, runID string) error {
	run, err := e.GetRun(runID)
	if err != nil {
		return err
	}
	if run.Status != StatusFailed {
		return fmt.Errorf("kodomo: can only resume failed runs (status=%s)", run.Status)
	}

	e.mu.RLock()
	wf, ok := e.workflows[run.WorkflowName]
	e.mu.RUnlock()
	if !ok {
		return fmt.Errorf("kodomo: workflow %q is not registered", run.WorkflowName)
	}

	results, err := e.GetStepResults(runID)
	if err != nil {
		return err
	}
	if len(results) == 0 {
		return fmt.Errorf("kodomo: run %s has no step results", runID)
	}

	// The last result tells us where we stopped.
	last := results[len(results)-1]
	var resumeStep string
	var resumeInput json.RawMessage

	switch last.Status {
	case StatusFailed:
		// Re-run the step that failed, with the same input it received.
		resumeStep = last.StepName
		resumeInput = last.Input
	case StatusCompleted:
		// Step succeeded but the run still failed — process must have crashed
		// before the next step started. Pick up at the next step.
		resumeStep = last.Next
		resumeInput = last.Output
		if resumeStep == "" {
			return fmt.Errorf("kodomo: run %s last step completed with no next step", runID)
		}
	default:
		return fmt.Errorf("kodomo: unexpected last step status %s", last.Status)
	}

	now := time.Now().UTC()
	_, err = e.db.ExecContext(ctx,
		`UPDATE runs SET status = ?, updated_at = ? WHERE id = ?`,
		StatusRunning, now, runID,
	)
	if err != nil {
		return fmt.Errorf("kodomo: update run status: %w", err)
	}

	return e.execute(ctx, runID, wf, resumeStep, resumeInput)
}

// execute runs the workflow state machine starting at stepName with the given input.
// It follows Next pointers until a step returns Done (Next=="") or an error.
func (e *SQLiteEngine) execute(ctx context.Context, runID string, wf Workflow, stepName string, input json.RawMessage) error {
	steps := buildStepMap(wf.Steps)
	current := input
	currentStep := stepName

	for currentStep != "" {
		step, ok := steps[currentStep]
		if !ok {
			errMsg := fmt.Sprintf("unknown step %q", currentStep)
			e.finishRun(runID, StatusFailed, nil, errMsg)
			return fmt.Errorf("%s", errMsg)
		}

		attempt := e.nextAttempt(runID, currentStep)

		start := time.Now()
		out, err := step.Fn(ctx, current)
		dur := time.Since(start)

		if err != nil {
			e.recordStep(runID, currentStep, attempt, StatusFailed, "", current, nil, err.Error(), dur)
			e.finishRun(runID, StatusFailed, nil, err.Error())
			return err
		}

		e.recordStep(runID, currentStep, attempt, StatusCompleted, out.Next, current, out.Data, "", dur)
		current = out.Data
		currentStep = out.Next
	}

	e.finishRun(runID, StatusCompleted, current, "")
	return nil
}

func (e *SQLiteEngine) GetRun(runID string) (*Run, error) {
	row := e.db.QueryRow(
		`SELECT id, workflow_name, workflow_version, status, input, output, error, created_at, updated_at
		 FROM runs WHERE id = ?`, runID,
	)
	run, err := scanRun(row)
	if err != nil {
		return nil, err
	}
	run.Tags, _ = e.loadTags(runID)
	return run, nil
}

func (e *SQLiteEngine) GetStepResults(runID string) ([]StepResult, error) {
	rows, err := e.db.Query(
		`SELECT id, run_id, seq, step_name, attempt, status, next_step, input, output, error, duration_ms, created_at
		 FROM step_results WHERE run_id = ? ORDER BY seq ASC`, runID,
	)
	if err != nil {
		return nil, fmt.Errorf("kodomo: query step results: %w", err)
	}
	defer rows.Close()

	var results []StepResult
	for rows.Next() {
		r, err := scanStepResult(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (e *SQLiteEngine) ListRuns(opts *ListRunsOpts) ([]Run, error) {
	query := `SELECT DISTINCT r.id, r.workflow_name, r.workflow_version, r.status, r.input, r.output, r.error, r.created_at, r.updated_at FROM runs r`
	var args []any
	var joins int

	if opts != nil {
		for k, v := range opts.Tags {
			alias := fmt.Sprintf("t%d", joins)
			query += fmt.Sprintf(` JOIN run_tags %s ON %s.run_id = r.id AND %s.key = ? AND %s.value = ?`, alias, alias, alias, alias)
			args = append(args, k, v)
			joins++
		}
	}

	query += ` WHERE 1=1`
	if opts != nil {
		if opts.WorkflowName != "" {
			query += ` AND r.workflow_name = ?`
			args = append(args, opts.WorkflowName)
		}
		if opts.Status != "" {
			query += ` AND r.status = ?`
			args = append(args, opts.Status)
		}
	}
	query += ` ORDER BY r.created_at DESC`
	if opts != nil && opts.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, opts.Limit)
	}

	rows, err := e.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("kodomo: list runs: %w", err)
	}
	defer rows.Close()

	var runs []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range runs {
		runs[i].Tags, _ = e.loadTags(runs[i].ID)
	}
	return runs, nil
}

func (e *SQLiteEngine) Close() error {
	return e.db.Close()
}

// --- internal helpers ---

func (e *SQLiteEngine) saveTags(runID string, tags map[string]string) {
	for k, v := range tags {
		_, _ = e.db.Exec(`INSERT INTO run_tags (run_id, key, value) VALUES (?, ?, ?)`, runID, k, v)
	}
}

func (e *SQLiteEngine) loadTags(runID string) (map[string]string, error) {
	rows, err := e.db.Query(`SELECT key, value FROM run_tags WHERE run_id = ?`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		tags[k] = v
	}
	if len(tags) == 0 {
		return nil, rows.Err()
	}
	return tags, rows.Err()
}

func buildStepMap(steps []Step) map[string]Step {
	m := make(map[string]Step, len(steps))
	for _, s := range steps {
		m[s.Name] = s
	}
	return m
}

func (e *SQLiteEngine) recordStep(runID, stepName string, attempt int, status Status, next string, input, output json.RawMessage, errMsg string, dur time.Duration) {
	_, _ = e.db.Exec(
		`INSERT INTO step_results (id, run_id, seq, step_name, attempt, status, next_step, input, output, error, duration_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		newID(), runID, e.nextSequence(runID), stepName, attempt, status, next,
		nullableJSON(input), nullableJSON(output), errMsg, dur.Milliseconds(),
	)
}

func (e *SQLiteEngine) finishRun(runID string, status Status, output json.RawMessage, errMsg string) {
	now := time.Now().UTC()
	_, _ = e.db.Exec(
		`UPDATE runs SET status = ?, output = ?, error = ?, updated_at = ? WHERE id = ?`,
		status, nullableJSON(output), errMsg, now, runID,
	)
}

func (e *SQLiteEngine) nextAttempt(runID, stepName string) int {
	var max int
	_ = e.db.QueryRow(
		`SELECT COALESCE(MAX(attempt), 0) FROM step_results WHERE run_id = ? AND step_name = ?`,
		runID, stepName,
	).Scan(&max)
	return max + 1
}

func (e *SQLiteEngine) nextSequence(runID string) int {
	var max int
	_ = e.db.QueryRow(
		`SELECT COALESCE(MAX(seq), 0) FROM step_results WHERE run_id = ?`,
		runID,
	).Scan(&max)
	return max + 1
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func nullableJSON(data json.RawMessage) *string {
	if data == nil {
		return nil
	}
	s := string(data)
	return &s
}

// scannable is satisfied by both *sql.Row and *sql.Rows.
type scannable interface {
	Scan(dest ...any) error
}

func scanRun(s scannable) (*Run, error) {
	var r Run
	var input, output, errMsg *string
	err := s.Scan(&r.ID, &r.WorkflowName, &r.WorkflowVersion, &r.Status,
		&input, &output, &errMsg, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("kodomo: scan run: %w", err)
	}
	if input != nil {
		r.Input = json.RawMessage(*input)
	}
	if output != nil {
		r.Output = json.RawMessage(*output)
	}
	if errMsg != nil {
		r.Error = *errMsg
	}
	return &r, nil
}

func scanStepResult(s scannable) (StepResult, error) {
	var r StepResult
	var input, output, errMsg *string
	var durMs sql.NullInt64
	err := s.Scan(&r.ID, &r.RunID, &r.Seq, &r.StepName, &r.Attempt, &r.Status, &r.Next,
		&input, &output, &errMsg, &durMs, &r.CreatedAt)
	if err != nil {
		return r, fmt.Errorf("kodomo: scan step result: %w", err)
	}
	if durMs.Valid {
		r.Duration = time.Duration(durMs.Int64) * time.Millisecond
	}
	if input != nil {
		r.Input = json.RawMessage(*input)
	}
	if output != nil {
		r.Output = json.RawMessage(*output)
	}
	if errMsg != nil {
		r.Error = *errMsg
	}
	return r, nil
}
