package agent

import (
	"context"
	"encoding/json"
	"testing"

	"kodomo/workflow"
)

func testAgent(t *testing.T) (*Agent, *workflow.SQLiteEngine) {
	t.Helper()
	e, err := workflow.Open(":memory:")
	if err != nil {
		t.Fatalf("open engine: %v", err)
	}
	t.Cleanup(func() { e.Close() })

	a, err := New(e, Config{Model: "gpt-5.2-codex"})
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

func TestToolStep(t *testing.T) {
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
		Model:           "gpt-test",
		ReasoningEffort: "high",
		PrevResponseID:  "resp-123",
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
	if result.PrevResponseID != "resp-123" {
		t.Fatalf("PrevResponseID not carried through: %s", result.PrevResponseID)
	}
	if result.Model != "gpt-test" {
		t.Fatalf("model not carried through: %s", result.Model)
	}
	if result.ReasoningEffort != "high" {
		t.Fatalf("reasoning effort not carried through: %s", result.ReasoningEffort)
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
	if len(params) != 1 {
		t.Fatalf("want 1 tool param, got %d", len(params))
	}
	if params[0].OfFunction == nil || params[0].OfFunction.Name != "greet" {
		t.Fatal("expected greet tool param")
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

	id, err := e.Start(context.Background(), "agent", json.RawMessage(`{"user_message":"hi"}`), nil)
	if id == "" {
		t.Fatal("expected a run ID even on failure")
	}
	if err == nil {
		t.Fatal("expected error (no API key)")
	}

	run, err := e.GetRun(id)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if run.Status != workflow.StatusFailed {
		t.Fatalf("expected failed (no API key), got %s", run.Status)
	}
}

func TestStartPersistsEffectiveRunOpts(t *testing.T) {
	a, e := testAgent(t)

	runID, err := a.Start(context.Background(), "hi", &RunOpts{
		Model:           "gpt-override",
		ReasoningEffort: "low",
		ConversationID:  "conv-123",
		PrevResponseID:  "resp-123",
	})
	if runID == "" {
		t.Fatal("expected a run ID even on failure")
	}
	if err == nil {
		t.Fatal("expected error (no API key)")
	}

	run, err := e.GetRun(runID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if run.Tags["conversation_id"] != "conv-123" {
		t.Fatalf("unexpected conversation_id tag: %v", run.Tags)
	}

	var state stepState
	if err := json.Unmarshal(run.Input, &state); err != nil {
		t.Fatalf("unmarshal run input: %v", err)
	}
	if state.Model != "gpt-override" {
		t.Fatalf("unexpected model: %s", state.Model)
	}
	if state.ReasoningEffort != "low" {
		t.Fatalf("unexpected reasoning effort: %s", state.ReasoningEffort)
	}
	if state.PrevResponseID != "resp-123" {
		t.Fatalf("unexpected prev response id: %s", state.PrevResponseID)
	}
}
