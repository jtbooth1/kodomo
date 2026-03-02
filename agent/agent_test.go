package agent

import (
	"context"
	"encoding/json"
	"testing"

	"kodomo/workflow"
)

func testAgent(t *testing.T, opts ...Option) (*Agent, *workflow.SQLiteEngine) {
	t.Helper()
	e, err := workflow.Open(":memory:")
	if err != nil {
		t.Fatalf("open engine: %v", err)
	}
	t.Cleanup(func() { e.Close() })

	a, err := New(e, Config{Model: "gpt-4.1"}, opts...)
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	return a, e
}

func TestAddTool(t *testing.T) {
	a, _ := testAgent(t)

	err := a.AddTool(ToolDef{
		Name:        "greet",
		Description: "Say hello",
		Schema:      map[string]any{"type": "object"},
		Handler: func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
			return json.Marshal("hello")
		},
	})
	if err != nil {
		t.Fatalf("add tool: %v", err)
	}

	if _, ok := a.tools["greet"]; !ok {
		t.Fatal("tool not registered")
	}
}

func TestAddToolValidation(t *testing.T) {
	a, _ := testAgent(t)

	err := a.AddTool(ToolDef{Name: "", Handler: func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) { return nil, nil }})
	if err == nil {
		t.Fatal("expected error for empty name")
	}

	err = a.AddTool(ToolDef{Name: "x"})
	if err == nil {
		t.Fatal("expected error for nil handler")
	}
}

func TestBuiltinMessageUser(t *testing.T) {
	var received string
	a, _ := testAgent(t, WithMessageHandler(func(msg string) { received = msg }))

	tool := a.tools["message_user"]
	if !tool.Terminal {
		t.Fatal("message_user should be terminal")
	}

	params, _ := json.Marshal(map[string]string{"message": "hello world"})
	_, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if received != "hello world" {
		t.Fatalf("want 'hello world', got %q", received)
	}
}

func TestToolStep(t *testing.T) {
	var received string
	a, _ := testAgent(t, WithMessageHandler(func(msg string) { received = msg }))

	state, _ := json.Marshal(stepState{
		PrevResponseID: "resp-123",
		ToolCalls: []toolCall{
			{ID: "call-1", Name: "message_user", Arguments: `{"message":"test output"}`},
		},
	})

	out, err := a.toolStep(context.Background(), state)
	if err != nil {
		t.Fatalf("tool step: %v", err)
	}

	// message_user is terminal, so the step should return Done
	if out.Next != "" {
		t.Fatalf("expected Done (empty Next), got %q", out.Next)
	}
	if received != "test output" {
		t.Fatalf("want 'test output', got %q", received)
	}
}

func TestToolStepNonTerminal(t *testing.T) {
	a, _ := testAgent(t)
	a.AddTool(ToolDef{
		Name:        "echo",
		Description: "Echo input",
		Schema:      map[string]any{"type": "object"},
		Handler: func(_ context.Context, params json.RawMessage) (json.RawMessage, error) {
			return params, nil
		},
	})

	state, _ := json.Marshal(stepState{
		PrevResponseID: "resp-123",
		ToolCalls: []toolCall{
			{ID: "call-1", Name: "echo", Arguments: `{"x":1}`},
		},
	})

	out, err := a.toolStep(context.Background(), state)
	if err != nil {
		t.Fatalf("tool step: %v", err)
	}

	if out.Next != "llm" {
		t.Fatalf("expected Goto(llm), got %q", out.Next)
	}

	var result stepState
	json.Unmarshal(out.Data, &result)
	if len(result.ToolResults) != 1 {
		t.Fatalf("want 1 tool result, got %d", len(result.ToolResults))
	}
	if result.ToolResults[0].CallID != "call-1" {
		t.Fatalf("wrong call ID: %s", result.ToolResults[0].CallID)
	}
}

func TestToolStepUnknownTool(t *testing.T) {
	a, _ := testAgent(t)

	state, _ := json.Marshal(stepState{
		ToolCalls: []toolCall{
			{ID: "call-1", Name: "nonexistent", Arguments: `{}`},
		},
	})

	_, err := a.toolStep(context.Background(), state)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

func TestToolParams(t *testing.T) {
	a, _ := testAgent(t)
	a.AddTool(ToolDef{
		Name:        "greet",
		Description: "Say hello",
		Schema:      map[string]any{"type": "object"},
		Handler: func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
			return nil, nil
		},
	})

	params := a.toolParams()
	if len(params) != 2 { // message_user + greet
		t.Fatalf("want 2 tool params, got %d", len(params))
	}

	names := make(map[string]bool)
	for _, p := range params {
		if p.OfFunction != nil {
			names[p.OfFunction.Name] = true
		}
	}
	if !names["message_user"] || !names["greet"] {
		t.Fatalf("missing tools: %v", names)
	}
}

func TestNewRequiresModel(t *testing.T) {
	e, _ := workflow.Open(":memory:")
	defer e.Close()

	_, err := New(e, Config{})
	if err == nil {
		t.Fatal("expected error for empty model")
	}
}

func TestWorkflowRegistered(t *testing.T) {
	_, e := testAgent(t)

	// The agent workflow should be registered. Starting it will fail at the
	// llm step (no real API key), but the run should be created.
	id, err := e.Start(context.Background(), "agent", json.RawMessage(`{"user_message":"hi"}`), nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	run, err := e.GetRun(id)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	// Run fails because there's no real API key, but it proves the
	// workflow was registered and the llm step was invoked.
	if run.Status != workflow.StatusFailed {
		t.Fatalf("expected failed (no API key), got %s", run.Status)
	}
}
