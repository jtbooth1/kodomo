# workflow

A durable workflow engine backed by SQLite. Workflows are state machines: each step runs, then declares which step runs next via `Goto`, or ends the workflow via `Done`. Every step execution is persisted, so failed runs can be resumed from exactly where they stopped.

## Import

```go
import "kodomo/workflow"
```

## Core concepts

| Type | Purpose |
|------|---------|
| `Engine` | Interface — register workflows, start/resume runs, query history |
| `SQLiteEngine` | Concrete implementation returned by `Open(dsn)` |
| `Workflow` | Named + versioned graph of `Step`s with a `Start` step |
| `Step` | Named unit of work containing a `StepFunc` |
| `StepFunc` | `func(ctx context.Context, input json.RawMessage) (*StepOutput, error)` |
| `StepOutput` | Returned by a step — wraps output data + a `Next` step name |
| `Run` | Persisted record of a workflow execution |
| `StepResult` | Persisted record of a single step attempt |
| `Status` | One of `StatusPending`, `StatusRunning`, `StatusCompleted`, `StatusFailed` |

## Engine interface

```go
type Engine interface {
    Register(w Workflow) error
    Start(ctx context.Context, workflowName string, input json.RawMessage, opts *StartOpts) (string, error)
    Resume(ctx context.Context, runID string) error
    GetRun(runID string) (*Run, error)
    GetStepResults(runID string) ([]StepResult, error)
    ListRuns(opts *ListRunsOpts) ([]Run, error)
    Close() error
}
```

## Quick start

```go
e, _ := workflow.Open("workflows.db") // or ":memory:" for tests
defer e.Close()

e.Register(workflow.Workflow{
    Name: "example", Version: 1,
    Start: "fetch",
    Steps: []workflow.Step{
        {Name: "fetch", Fn: fetchData},
        {Name: "process", Fn: processData},
    },
})

runID, _ := e.Start(ctx, "example", json.RawMessage(`{"url":"..."}`), nil)
```

## Step functions and transitions

Every step receives input and returns a `*StepOutput` declaring what happens next:

- **`Done(data)`** — workflow completes successfully with `data` as its final output.
- **`Goto(stepName, data)`** — transition to the named step, passing `data` as its input.

```go
func fetchData(ctx context.Context, input json.RawMessage) (*workflow.StepOutput, error) {
    var req RequestParams
    json.Unmarshal(input, &req)

    resp, err := http.Get(req.URL)
    if err != nil {
        return nil, err // marks run as failed; resumable later
    }
    body, _ := io.ReadAll(resp.Body)
    return workflow.Goto("process", body), nil
}
```

## State machine pattern

Steps can transition to any other step in the workflow, forming arbitrary graphs. A step can also `Goto` itself to loop.

```go
e.Register(workflow.Workflow{
    Name: "agent", Version: 1,
    Start: "llm",
    Steps: []workflow.Step{
        {Name: "llm", Fn: func(ctx context.Context, in json.RawMessage) (*workflow.StepOutput, error) {
            response := callChatGPT(ctx, in)
            if response.HasToolCalls {
                return workflow.Goto("tool", marshal(response.ToolCalls)), nil
            }
            return workflow.Done(marshal(response.Text)), nil
        }},
        {Name: "tool", Fn: func(ctx context.Context, in json.RawMessage) (*workflow.StepOutput, error) {
            results := executeTools(ctx, in)
            return workflow.Goto("llm", marshal(results)), nil
        }},
    },
})
```

Execution trace: `llm → tool → llm → tool → llm(done)`. Each arrow is a persisted `StepResult`.

## Resuming failed runs

`Resume` re-executes a failed run from the exact step that failed, with the same input it originally received.

```go
run, _ := e.GetRun(runID)
if run.Status == workflow.StatusFailed {
    e.Resume(ctx, runID)
}
```

## Inspecting history

```go
steps, _ := e.GetStepResults(runID)
for _, s := range steps {
    fmt.Printf("step=%s attempt=%d status=%s next=%s duration=%s err=%s\n",
        s.StepName, s.Attempt, s.Status, s.Next, s.Duration, s.Error)
}

failed, _ := e.ListRuns(&workflow.ListRunsOpts{
    WorkflowName: "agent",
    Status:       workflow.StatusFailed,
})
```

## StepResult fields

Each `StepResult` records:

- `StepName` — which step in the workflow
- `Attempt` — incrementing counter per step name within the run
- `Status` — `completed` or `failed`
- `Next` — the step that was transitioned to (empty if the workflow completed)
- `Input` / `Output` — JSON snapshots of what went in and came out
- `Error` — error message if the step failed
- `Duration` — wall-clock time for the step execution

## Tags

Runs can be tagged with arbitrary key-value metadata via `StartOpts`:

```go
e.Start(ctx, "agent", input, &workflow.StartOpts{
    Tags: map[string]string{"conversation_id": "conv-123", "user": "alice"},
})
```

Tags are stored in a `run_tags` table and loaded automatically with runs. Filter by tags in `ListRuns`:

```go
runs, _ := e.ListRuns(&workflow.ListRunsOpts{
    Tags: map[string]string{"conversation_id": "conv-123"},
})
```

## Storage

All state lives in a single SQLite file with three tables: `runs`, `step_results`, and `run_tags`. You can query it directly with the `sqlite3` CLI for ad-hoc debugging. The database uses WAL mode for safe concurrent reads.
