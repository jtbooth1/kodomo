package browser

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	_ "modernc.org/sqlite"
)

// Server provides a basic SSR view of the SQLite database.
type Server struct {
	db        *sql.DB
	indexTmpl *template.Template
	runTmpl   *template.Template
	mux       *http.ServeMux
}

// New opens the SQLite database at dbPath and prepares the HTTP handlers.
func New(dbPath string) (*Server, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("browser: open db: %w", err)
	}

	base, err := template.New("layout").Funcs(template.FuncMap{
		"statusClass": statusClass,
	}).Parse(layoutTemplate)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("browser: parse layout template: %w", err)
	}

	indexTmpl, err := base.Clone()
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("browser: clone layout template: %w", err)
	}
	if _, err := indexTmpl.Parse(indexTemplate); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("browser: parse index template: %w", err)
	}

	runTmpl, err := base.Clone()
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("browser: clone layout template: %w", err)
	}
	if _, err := runTmpl.Parse(runTemplate); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("browser: parse run template: %w", err)
	}

	s := &Server{
		db:        db,
		indexTmpl: indexTmpl,
		runTmpl:   runTmpl,
		mux:       http.NewServeMux(),
	}
	s.routes()
	return s, nil
}

// Close releases the database connection.
func (s *Server) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Handler returns the HTTP handler for the browser.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// Serve starts an HTTP server on the given address.
func (s *Server) Serve(addr string) error {
	return http.ListenAndServe(addr, s.mux)
}

// ServeContext starts an HTTP server and stops it when the context is done.
func (s *Server) ServeContext(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.mux}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	return srv.ListenAndServe()
}

func (s *Server) routes() {
	s.mux.HandleFunc("/", s.handleIndex)
	s.mux.HandleFunc("/runs/", s.handleRun)
}

type stepState struct {
	PrevResponseID string `json:"prev_response_id"`
	UserMessage    string `json:"user_message"`
	Message        string `json:"message"`
}

type runRow struct {
	ID               string
	ConversationID   string
	WorkflowName     string
	Status           string
	CreatedAt        string
	UpdatedAt        string
	UserMessage      string
	AssistantMessage string
	Error            string
	InputJSON        string
	OutputJSON       string
}

type stepRow struct {
	ID         string
	Seq        int
	StepName   string
	Attempt    int
	Status     string
	NextStep   string
	CreatedAt  string
	DurationMs int64
	InputJSON  string
	OutputJSON string
	Error      string
}

type indexData struct {
	ConversationIDs []string
	SelectedConvID  string
	Runs            []runRow
}

type runData struct {
	Run      runRow
	StepRows []stepRow
	RunID    string
	BackHref string
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	convIDs, err := s.listConversationIDs(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	selected := r.URL.Query().Get("conversation")
	runs, err := s.listRuns(r.Context(), selected)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := indexData{
		ConversationIDs: convIDs,
		SelectedConvID:  selected,
		Runs:            runs,
	}

	if err := s.indexTmpl.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	runID := strings.TrimPrefix(r.URL.Path, "/runs/")
	if runID == "" {
		http.NotFound(w, r)
		return
	}

	run, steps, err := s.loadRun(r.Context(), runID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if run == nil {
		http.NotFound(w, r)
		return
	}

	backHref := "/"
	if conv := r.URL.Query().Get("conversation"); conv != "" {
		backHref = "/?conversation=" + conv
	}

	data := runData{
		Run:      *run,
		StepRows: steps,
		RunID:    runID,
		BackHref: backHref,
	}

	if err := s.runTmpl.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) listConversationIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT value FROM run_tags WHERE key = 'conversation_id' ORDER BY value`)
	if err != nil {
		return nil, fmt.Errorf("browser: list conversation ids: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("browser: scan conversation id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("browser: list conversation ids: %w", err)
	}
	return ids, nil
}

func (s *Server) listRuns(ctx context.Context, convID string) ([]runRow, error) {
	var args []any
	query := `
		SELECT r.id, r.workflow_name, r.status, r.input, r.output, r.error, r.created_at, r.updated_at,
			COALESCE(rt.value, '') AS conversation_id
		FROM runs r
		LEFT JOIN run_tags rt ON r.id = rt.run_id AND rt.key = 'conversation_id'
	`
	if convID != "" {
		query += " WHERE rt.value = ?"
		args = append(args, convID)
	}
	query += " ORDER BY r.created_at DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("browser: list runs: %w", err)
	}
	defer rows.Close()

	var runs []runRow
	for rows.Next() {
		var row runRow
		var input, output, errStr *string
		if err := rows.Scan(&row.ID, &row.WorkflowName, &row.Status, &input, &output, &errStr, &row.CreatedAt, &row.UpdatedAt, &row.ConversationID); err != nil {
			return nil, fmt.Errorf("browser: scan run: %w", err)
		}
		row.InputJSON = derefString(input)
		row.OutputJSON = derefString(output)
		row.Error = derefString(errStr)
		row.UserMessage, row.AssistantMessage = parseMessages(row.InputJSON, row.OutputJSON)
		runs = append(runs, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("browser: list runs: %w", err)
	}
	return runs, nil
}

func (s *Server) loadRun(ctx context.Context, runID string) (*runRow, []stepRow, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT r.id, r.workflow_name, r.status, r.input, r.output, r.error, r.created_at, r.updated_at,
			COALESCE(rt.value, '') AS conversation_id
		FROM runs r
		LEFT JOIN run_tags rt ON r.id = rt.run_id AND rt.key = 'conversation_id'
		WHERE r.id = ?`, runID)

	var run runRow
	var input, output, errStr *string
	if err := row.Scan(&run.ID, &run.WorkflowName, &run.Status, &input, &output, &errStr, &run.CreatedAt, &run.UpdatedAt, &run.ConversationID); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("browser: load run: %w", err)
	}
	run.InputJSON = derefString(input)
	run.OutputJSON = derefString(output)
	run.Error = derefString(errStr)
	run.UserMessage, run.AssistantMessage = parseMessages(run.InputJSON, run.OutputJSON)

	stepRows, err := s.loadSteps(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	return &run, stepRows, nil
}

func (s *Server) loadSteps(ctx context.Context, runID string) ([]stepRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, seq, step_name, attempt, status, next_step, input, output, error, duration_ms, created_at
		FROM step_results WHERE run_id = ? ORDER BY seq ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("browser: load steps: %w", err)
	}
	defer rows.Close()

	var steps []stepRow
	for rows.Next() {
		var row stepRow
		var input, output, errStr *string
		var durationMs sql.NullInt64
		if err := rows.Scan(&row.ID, &row.Seq, &row.StepName, &row.Attempt, &row.Status, &row.NextStep, &input, &output, &errStr, &durationMs, &row.CreatedAt); err != nil {
			return nil, fmt.Errorf("browser: scan step: %w", err)
		}
		if durationMs.Valid {
			row.DurationMs = durationMs.Int64
		}
		row.InputJSON = derefString(input)
		row.OutputJSON = derefString(output)
		row.Error = derefString(errStr)
		steps = append(steps, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("browser: load steps: %w", err)
	}
	return steps, nil
}

func parseMessages(inputJSON, outputJSON string) (string, string) {
	var inputState stepState
	if inputJSON != "" {
		_ = json.Unmarshal([]byte(inputJSON), &inputState)
	}
	var outputState stepState
	if outputJSON != "" {
		_ = json.Unmarshal([]byte(outputJSON), &outputState)
	}
	return inputState.UserMessage, outputState.Message
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func statusClass(status string) string {
	switch strings.ToLower(status) {
	case "completed":
		return "status-completed"
	case "failed":
		return "status-failed"
	case "running":
		return "status-running"
	default:
		return "status-pending"
	}
}

const layoutTemplate = `
{{define "layout"}}
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>Kodomo Browser</title>
    <style>
      :root {
        color-scheme: light dark;
      }
      body {
        font-family: system-ui, -apple-system, sans-serif;
        margin: 0;
        background: #0f0f10;
        color: #f4f4f5;
      }
      header {
        padding: 16px 24px;
        background: #17171a;
        border-bottom: 1px solid #2b2b31;
      }
      main {
        padding: 20px 24px 48px;
      }
      a {
        color: #9ad5ff;
        text-decoration: none;
      }
      a:hover {
        text-decoration: underline;
      }
      .content {
        max-width: 1200px;
        margin: 0 auto;
      }
      .filters {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        margin: 16px 0 24px;
      }
      .chip {
        display: inline-block;
        padding: 6px 12px;
        border-radius: 999px;
        border: 1px solid #2b2b31;
        background: #1a1a1f;
        color: inherit;
      }
      .chip.active {
        background: #2a3b52;
        border-color: #4c6a91;
      }
      table {
        width: 100%;
        border-collapse: collapse;
        background: #141417;
        border: 1px solid #2b2b31;
      }
      th, td {
        text-align: left;
        padding: 10px 12px;
        border-bottom: 1px solid #24242a;
        vertical-align: top;
        font-size: 14px;
      }
      th {
        background: #1b1b21;
        font-weight: 600;
      }
      tr:hover {
        background: #1b1b21;
      }
      .muted {
        color: #a1a1aa;
      }
      .status {
        padding: 4px 8px;
        border-radius: 8px;
        font-size: 12px;
        display: inline-block;
      }
      .status-completed {
        background: #143d2f;
        color: #a3f7c1;
      }
      .status-failed {
        background: #472020;
        color: #ffb4b4;
      }
      .status-running {
        background: #413b1a;
        color: #ffd48c;
      }
      .status-pending {
        background: #2b2b31;
        color: #d4d4d8;
      }
      pre {
        white-space: pre-wrap;
        word-break: break-word;
        background: #0f0f10;
        padding: 12px;
        border-radius: 8px;
        border: 1px solid #2b2b31;
        font-size: 13px;
      }
      .grid {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
        gap: 16px;
      }
      .card {
        background: #141417;
        border: 1px solid #2b2b31;
        border-radius: 12px;
        padding: 16px;
      }
      .card h3 {
        margin: 0 0 8px;
      }
      @media (prefers-color-scheme: light) {
        body {
          background: #f8f8fb;
          color: #18181b;
        }
        header {
          background: #ffffff;
          border-bottom: 1px solid #e4e4e7;
        }
        table {
          background: #ffffff;
          border-color: #e4e4e7;
        }
        th {
          background: #f1f1f5;
        }
        tr:hover {
          background: #f8f8fb;
        }
        .chip {
          background: #ffffff;
          border-color: #e4e4e7;
        }
        .chip.active {
          background: #dbeafe;
          border-color: #93c5fd;
        }
        pre {
          background: #f8f8fb;
          border-color: #e4e4e7;
        }
        .card {
          background: #ffffff;
          border-color: #e4e4e7;
        }
      }
    </style>
  </head>
  <body>
    <header>
      <div class="content">
        <h1>Kodomo Browser</h1>
        <p class="muted">Chat history and workflow runs stored in the local SQLite database.</p>
      </div>
    </header>
    <main>
      <div class="content">
        {{block "body" .}}{{end}}
      </div>
    </main>
  </body>
</html>
{{end}}
`

const indexTemplate = `
{{define "body"}}
  <section>
    <h2>Conversations</h2>
    <div class="filters">
      <a class="chip {{if eq .SelectedConvID ""}}active{{end}}" href="/">All</a>
      {{range .ConversationIDs}}
        <a class="chip {{if eq $.SelectedConvID .}}active{{end}}" href="/?conversation={{.}}">{{.}}</a>
      {{end}}
    </div>
  </section>

  <section>
    <h2>Runs</h2>
    <table>
      <thead>
        <tr>
          <th>Created</th>
          <th>Conversation</th>
          <th>Status</th>
          <th>User</th>
          <th>Assistant</th>
          <th>Run</th>
        </tr>
      </thead>
      <tbody>
        {{range .Runs}}
          <tr>
            <td>{{.CreatedAt}}</td>
            <td>{{if .ConversationID}}{{.ConversationID}}{{else}}<span class="muted">–</span>{{end}}</td>
            <td><span class="status {{statusClass .Status}}">{{.Status}}</span></td>
            <td>{{if .UserMessage}}{{.UserMessage}}{{else}}<span class="muted">(no input)</span>{{end}}</td>
            <td>{{if .AssistantMessage}}{{.AssistantMessage}}{{else}}<span class="muted">(no output)</span>{{end}}</td>
            <td><a href="/runs/{{.ID}}{{if $.SelectedConvID}}?conversation={{$.SelectedConvID}}{{end}}">{{.ID}}</a></td>
          </tr>
        {{else}}
          <tr>
            <td colspan="6" class="muted">No runs found.</td>
          </tr>
        {{end}}
      </tbody>
    </table>
  </section>
{{end}}
`

const runTemplate = `
{{define "body"}}
  <p><a href="{{.BackHref}}">← Back to runs</a></p>
  <section class="grid">
    <div class="card">
      <h3>Run</h3>
      <p><strong>ID:</strong> {{.Run.ID}}</p>
      <p><strong>Conversation:</strong> {{if .Run.ConversationID}}{{.Run.ConversationID}}{{else}}<span class="muted">–</span>{{end}}</p>
      <p><strong>Status:</strong> <span class="status {{statusClass .Run.Status}}">{{.Run.Status}}</span></p>
      <p><strong>Workflow:</strong> {{.Run.WorkflowName}}</p>
      <p><strong>Created:</strong> {{.Run.CreatedAt}}</p>
      <p><strong>Updated:</strong> {{.Run.UpdatedAt}}</p>
      {{if .Run.Error}}
        <p><strong>Error:</strong> {{.Run.Error}}</p>
      {{end}}
    </div>
    <div class="card">
      <h3>Messages</h3>
      <p><strong>User:</strong> {{if .Run.UserMessage}}{{.Run.UserMessage}}{{else}}<span class="muted">(no input)</span>{{end}}</p>
      <p><strong>Assistant:</strong> {{if .Run.AssistantMessage}}{{.Run.AssistantMessage}}{{else}}<span class="muted">(no output)</span>{{end}}</p>
    </div>
  </section>

  <section>
    <h2>Run JSON</h2>
    <div class="grid">
      <div class="card">
        <h3>Input</h3>
        {{if .Run.InputJSON}}<pre>{{.Run.InputJSON}}</pre>{{else}}<p class="muted">No input JSON.</p>{{end}}
      </div>
      <div class="card">
        <h3>Output</h3>
        {{if .Run.OutputJSON}}<pre>{{.Run.OutputJSON}}</pre>{{else}}<p class="muted">No output JSON.</p>{{end}}
      </div>
    </div>
  </section>

  <section>
    <h2>Step Results</h2>
    <table>
      <thead>
        <tr>
          <th>Seq</th>
          <th>Created</th>
          <th>Step</th>
          <th>Attempt</th>
          <th>Status</th>
          <th>Next</th>
          <th>Duration (ms)</th>
        </tr>
      </thead>
      <tbody>
        {{range .StepRows}}
          <tr>
            <td>{{.Seq}}</td>
            <td>{{.CreatedAt}}</td>
            <td>{{.StepName}}</td>
            <td>{{.Attempt}}</td>
            <td><span class="status {{statusClass .Status}}">{{.Status}}</span></td>
            <td>{{if .NextStep}}{{.NextStep}}{{else}}<span class="muted">–</span>{{end}}</td>
            <td>{{.DurationMs}}</td>
          </tr>
        {{else}}
          <tr>
            <td colspan="7" class="muted">No step results found.</td>
          </tr>
        {{end}}
      </tbody>
    </table>
  </section>

  <section>
    <h2>Step JSON</h2>
    {{range .StepRows}}
      <div class="card" style="margin-bottom: 16px;">
        <h3>#{{.Seq}} {{.StepName}} ({{.Status}})</h3>
        {{if .Error}}<p><strong>Error:</strong> {{.Error}}</p>{{end}}
        <div class="grid">
          <div>
            <h4>Input</h4>
            {{if .InputJSON}}<pre>{{.InputJSON}}</pre>{{else}}<p class="muted">No input JSON.</p>{{end}}
          </div>
          <div>
            <h4>Output</h4>
            {{if .OutputJSON}}<pre>{{.OutputJSON}}</pre>{{else}}<p class="muted">No output JSON.</p>{{end}}
          </div>
        </div>
      </div>
    {{end}}
  </section>
{{end}}
`
