package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"kodomo/workflow"

	openai "github.com/openai/openai-go/v3"
)

// Option configures an Agent.
type Option func(*Agent)

// WithMessageHandler sets the function called when the LLM invokes message_user.
// Defaults to fmt.Println.
func WithMessageHandler(fn func(string)) Option {
	return func(a *Agent) { a.messageHandler = fn }
}

// Agent orchestrates LLM calls, tool execution, and workflow persistence.
type Agent struct {
	engine         *workflow.SQLiteEngine
	client         openai.Client
	config         Config
	tools          map[string]ToolDef
	messageHandler func(string)

	// set per-run by Start, read by llmStep
	runOpts RunOpts
}

// New creates an Agent. The OpenAI client reads OPENAI_API_KEY from the environment.
func New(engine *workflow.SQLiteEngine, config Config, opts ...Option) (*Agent, error) {
	if config.Model == "" {
		return nil, fmt.Errorf("agent: Config.Model is required")
	}

	a := &Agent{
		engine:         engine,
		client:         openai.NewClient(),
		config:         config,
		tools:          make(map[string]ToolDef),
		messageHandler: func(msg string) { fmt.Println(msg) },
	}
	for _, opt := range opts {
		opt(a)
	}
	a.registerBuiltinTools()
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

// LastResponseID returns the OpenAI response ID from a completed run,
// used to chain conversation turns via PrevResponseID.
func (a *Agent) LastResponseID(runID string) (string, error) {
	run, err := a.engine.GetRun(runID)
	if err != nil {
		return "", err
	}
	if run.Output == nil {
		return "", nil
	}
	var state stepState
	if err := json.Unmarshal(run.Output, &state); err != nil {
		return "", fmt.Errorf("agent: unmarshal run output: %w", err)
	}
	return state.PrevResponseID, nil
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

// toolStep executes tool calls from the LLM and returns results.
// If any tool is terminal, the run completes after execution.
func (a *Agent) toolStep(ctx context.Context, input json.RawMessage) (*workflow.StepOutput, error) {
	var state stepState
	if err := json.Unmarshal(input, &state); err != nil {
		return nil, fmt.Errorf("tool: unmarshal state: %w", err)
	}

	var results []toolResult
	terminal := false

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
			CallID:   tc.ID,
			Output:   out,
			Terminal: tool.Terminal,
		})

		if tool.Terminal {
			terminal = true
		}
	}

	next, _ := json.Marshal(stepState{
		PrevResponseID: state.PrevResponseID,
		ToolResults:    results,
	})

	if terminal {
		return workflow.Done(next), nil
	}
	return workflow.Goto("llm", next), nil
}
