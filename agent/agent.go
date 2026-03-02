package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"kodomo/workflow"

	openai "github.com/openai/openai-go/v3"
)

// Agent orchestrates LLM calls, tool execution, and workflow persistence.
type Agent struct {
	engine *workflow.SQLiteEngine
	client openai.Client
	config Config
	tools  map[string]ToolDef

	// set per-run by Start, read by llmStep
	runOpts RunOpts
}

// New creates an Agent. The OpenAI client reads OPENAI_API_KEY from the environment.
func New(engine *workflow.SQLiteEngine, config Config) (*Agent, error) {
	if config.Model == "" {
		return nil, fmt.Errorf("agent: Config.Model is required")
	}

	a := &Agent{
		engine: engine,
		client: openai.NewClient(),
		config: config,
		tools:  make(map[string]ToolDef),
	}
	a.registerWorkflow()
	return a, nil
}

// Start begins a new agent run. The prompt is sent to the LLM as a user message.
// Returns the workflow run ID.
func (a *Agent) Start(ctx context.Context, prompt string, opts *RunOpts) (string, error) {
	if opts != nil {
		a.runOpts = *opts
	} else {
		a.runOpts = RunOpts{}
	}

	state, _ := json.Marshal(stepState{
		UserMessage:    prompt,
		PrevResponseID: a.runOpts.PrevResponseID,
	})

	var startOpts *workflow.StartOpts
	if a.runOpts.ConversationID != "" {
		startOpts = &workflow.StartOpts{
			Tags: map[string]string{"conversation_id": a.runOpts.ConversationID},
		}
	}

	return a.engine.Start(ctx, "agent", state, startOpts)
}

// Resume retries a failed agent run from the point of failure.
func (a *Agent) Resume(ctx context.Context, runID string) error {
	return a.engine.Resume(ctx, runID)
}

// RunResult holds the output of a completed agent run.
type RunResult struct {
	ResponseID string // OpenAI response ID for chaining turns
	Message    string // text response from the LLM
}

// Result returns the output of a completed run.
func (a *Agent) Result(runID string) (*RunResult, error) {
	run, err := a.engine.GetRun(runID)
	if err != nil {
		return nil, err
	}
	if run.Output == nil {
		return &RunResult{}, nil
	}
	var state stepState
	if err := json.Unmarshal(run.Output, &state); err != nil {
		return nil, fmt.Errorf("agent: unmarshal run output: %w", err)
	}
	return &RunResult{
		ResponseID: state.PrevResponseID,
		Message:    state.Message,
	}, nil
}

func (a *Agent) registerWorkflow() {
	a.engine.Register(workflow.Workflow{
		Name:    "agent",
		Version: 1,
		Start:   "llm",
		Steps: []workflow.Step{
			{Name: "llm", Fn: a.llmStep},
			{Name: "tool", Fn: a.toolStep},
		},
	})
}

// toolStep executes tool calls from the LLM and sends results back to llm.
func (a *Agent) toolStep(ctx context.Context, input json.RawMessage) (*workflow.StepOutput, error) {
	var state stepState
	if err := json.Unmarshal(input, &state); err != nil {
		return nil, fmt.Errorf("tool: unmarshal state: %w", err)
	}

	var results []toolResult
	for _, tc := range state.ToolCalls {
		tool, ok := a.tools[tc.Name]
		if !ok {
			return nil, fmt.Errorf("tool: unknown tool %q", tc.Name)
		}

		out, err := tool.Handler(ctx, json.RawMessage(tc.Arguments))
		if err != nil {
			return nil, fmt.Errorf("tool: %s: %w", tc.Name, err)
		}

		results = append(results, toolResult{
			CallID: tc.ID,
			Output: out,
		})
	}

	next, _ := json.Marshal(stepState{
		PrevResponseID: state.PrevResponseID,
		ToolResults:    results,
	})
	return workflow.Goto("llm", next), nil
}
