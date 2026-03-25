package workflow

import (
	"context"
	"encoding/json"
	"time"
)

// Status represents the current state of a run or step.
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
)

// StepOutput is returned by a StepFunc to indicate its result.
type StepOutput struct {
	Data json.RawMessage
	Next string // name of the next step to execute; empty means the workflow is done
}

// Done returns a StepOutput that completes the workflow with the given data.
func Done(data json.RawMessage) *StepOutput {
	return &StepOutput{Data: data}
}

// Goto returns a StepOutput that transitions to the named step with data as its input.
func Goto(step string, data json.RawMessage) *StepOutput {
	return &StepOutput{Data: data, Next: step}
}

// StepFunc executes a unit of work. It receives input from the previous step
// (or the run input for the starting step) and returns a StepOutput.
// Return Done(data) to finish the workflow, or Goto(step, data) to transition.
// Returning an error marks the step (and the run) as failed.
type StepFunc func(ctx context.Context, input json.RawMessage) (*StepOutput, error)

// Step is a named unit of work inside a workflow.
type Step struct {
	Name string
	Fn   StepFunc
}

// Workflow defines a named, versioned graph of steps.
// Start is the name of the first step to execute.
type Workflow struct {
	Name    string
	Version int
	Start   string
	Steps   []Step
}

// StartOpts provides optional parameters for Start.
type StartOpts struct {
	Tags map[string]string
}

// Run is a persisted record of a single workflow execution.
type Run struct {
	ID              string            `json:"id"`
	WorkflowName    string            `json:"workflow_name"`
	WorkflowVersion int               `json:"workflow_version"`
	Status          Status            `json:"status"`
	Tags            map[string]string `json:"tags,omitempty"`
	Input           json.RawMessage   `json:"input,omitempty"`
	Output          json.RawMessage   `json:"output,omitempty"`
	Error           string            `json:"error,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

// StepResult records one attempt at executing a step.
type StepResult struct {
	ID        string          `json:"id"`
	RunID     string          `json:"run_id"`
	Seq       int             `json:"seq"`
	StepName  string          `json:"step_name"`
	Attempt   int             `json:"attempt"`
	Status    Status          `json:"status"`
	Next      string          `json:"next,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
	Error     string          `json:"error,omitempty"`
	Duration  time.Duration   `json:"duration"`
	CreatedAt time.Time       `json:"created_at"`
}

// ListRunsOpts filters results from ListRuns. All fields are optional.
type ListRunsOpts struct {
	WorkflowName string
	Status       Status
	Tags         map[string]string
	Limit        int
}

// Engine is the top-level interface for the workflow engine.
//
//   - Register makes a workflow definition known to the engine.
//   - Start begins a new run, executing from the workflow's Start step. Returns the run ID.
//   - Resume retries a failed run from the point of failure.
//   - GetRun and GetStepResults let you inspect history.
//   - ListRuns returns runs matching the given filters.
type Engine interface {
	Register(w Workflow) error
	Start(ctx context.Context, workflowName string, input json.RawMessage, opts *StartOpts) (string, error)
	Resume(ctx context.Context, runID string) error
	GetRun(runID string) (*Run, error)
	GetStepResults(runID string) ([]StepResult, error)
	ListRuns(opts *ListRunsOpts) ([]Run, error)
	Close() error
}
