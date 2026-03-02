package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func testEngine(t *testing.T) *SQLiteEngine {
	t.Helper()
	e, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func TestHappyPath(t *testing.T) {
	e := testEngine(t)

	e.Register(Workflow{
		Name:    "greet",
		Version: 1,
		Start:   "upper",
		Steps: []Step{
			{Name: "upper", Fn: func(_ context.Context, in json.RawMessage) (*StepOutput, error) {
				var name string
				json.Unmarshal(in, &name)
				out, _ := json.Marshal("HELLO " + name)
				return Goto("wrap", out), nil
			}},
			{Name: "wrap", Fn: func(_ context.Context, in json.RawMessage) (*StepOutput, error) {
				var msg string
				json.Unmarshal(in, &msg)
				out, _ := json.Marshal("<<" + msg + ">>")
				return Done(out), nil
			}},
		},
	})

	id, err := e.Start(context.Background(), "greet", json.RawMessage(`"world"`), nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	run, err := e.GetRun(id)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if run.Status != StatusCompleted {
		t.Fatalf("want completed, got %s (error: %s)", run.Status, run.Error)
	}
	var got string
	json.Unmarshal(run.Output, &got)
	if got != "<<HELLO world>>" {
		t.Fatalf("unexpected output: %s", got)
	}

	steps, _ := e.GetStepResults(id)
	if len(steps) != 2 {
		t.Fatalf("want 2 step results, got %d", len(steps))
	}
	if steps[0].StepName != "upper" || steps[1].StepName != "wrap" {
		t.Fatalf("unexpected step names: %s, %s", steps[0].StepName, steps[1].StepName)
	}
}

func TestFailureAndResume(t *testing.T) {
	e := testEngine(t)
	calls := 0

	e.Register(Workflow{
		Name:    "flaky",
		Version: 1,
		Start:   "step1",
		Steps: []Step{
			{Name: "step1", Fn: func(_ context.Context, in json.RawMessage) (*StepOutput, error) {
				out, _ := json.Marshal("step1-done")
				return Goto("step2", out), nil
			}},
			{Name: "step2", Fn: func(_ context.Context, in json.RawMessage) (*StepOutput, error) {
				calls++
				if calls == 1 {
					return nil, fmt.Errorf("api timeout")
				}
				out, _ := json.Marshal("step2-done")
				return Done(out), nil
			}},
		},
	})

	id, _ := e.Start(context.Background(), "flaky", nil, nil)

	run, _ := e.GetRun(id)
	if run.Status != StatusFailed {
		t.Fatalf("want failed, got %s", run.Status)
	}
	if run.Error != "api timeout" {
		t.Fatalf("want 'api timeout', got %q", run.Error)
	}

	// Resume — calls==2, step2 succeeds
	err := e.Resume(context.Background(), id)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	run, _ = e.GetRun(id)
	if run.Status != StatusCompleted {
		t.Fatalf("want completed, got %s (err: %s)", run.Status, run.Error)
	}

	steps, _ := e.GetStepResults(id)
	// step1: completed | step2: failed, then completed
	if len(steps) != 3 {
		t.Fatalf("want 3 step results, got %d", len(steps))
	}
}

func TestStateMachine(t *testing.T) {
	e := testEngine(t)

	// Simulates the agent pattern: llm ↔ tool loop.
	// llm decides to call tools twice, then finishes.
	llmCalls := 0
	e.Register(Workflow{
		Name:    "agent",
		Version: 1,
		Start:   "llm",
		Steps: []Step{
			{Name: "llm", Fn: func(_ context.Context, in json.RawMessage) (*StepOutput, error) {
				llmCalls++
				if llmCalls <= 2 {
					out, _ := json.Marshal(map[string]any{
						"tool_calls": []string{"read_file"},
						"turn":       llmCalls,
					})
					return Goto("tool", out), nil
				}
				out, _ := json.Marshal("final answer")
				return Done(out), nil
			}},
			{Name: "tool", Fn: func(_ context.Context, in json.RawMessage) (*StepOutput, error) {
				out, _ := json.Marshal(map[string]any{"tool_result": "file contents"})
				return Goto("llm", out), nil
			}},
		},
	})

	id, err := e.Start(context.Background(), "agent", json.RawMessage(`{"prompt":"hello"}`), nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	run, _ := e.GetRun(id)
	if run.Status != StatusCompleted {
		t.Fatalf("want completed, got %s (error: %s)", run.Status, run.Error)
	}

	var result string
	json.Unmarshal(run.Output, &result)
	if result != "final answer" {
		t.Fatalf("unexpected output: %s", result)
	}

	steps, _ := e.GetStepResults(id)
	// llm(1) → tool(1) → llm(2) → tool(2) → llm(3,done) = 5
	if len(steps) != 5 {
		t.Fatalf("want 5 step results, got %d", len(steps))
	}

	expected := []string{"llm", "tool", "llm", "tool", "llm"}
	for i, name := range expected {
		if steps[i].StepName != name {
			t.Fatalf("step %d: want %s, got %s", i, name, steps[i].StepName)
		}
	}
}

func TestStateMachineFailureAndResume(t *testing.T) {
	e := testEngine(t)

	toolCalls := 0
	e.Register(Workflow{
		Name:    "agent",
		Version: 1,
		Start:   "llm",
		Steps: []Step{
			{Name: "llm", Fn: func(_ context.Context, in json.RawMessage) (*StepOutput, error) {
				out, _ := json.Marshal("call a tool")
				return Goto("tool", out), nil
			}},
			{Name: "tool", Fn: func(_ context.Context, in json.RawMessage) (*StepOutput, error) {
				toolCalls++
				if toolCalls == 1 {
					return nil, fmt.Errorf("api timeout")
				}
				out, _ := json.Marshal("tool result")
				return Goto("llm", out), nil
			}},
		},
	})

	id, _ := e.Start(context.Background(), "agent", nil, nil)

	run, _ := e.GetRun(id)
	if run.Status != StatusFailed {
		t.Fatalf("want failed, got %s", run.Status)
	}

	// Re-register with an llm that finishes after getting tool results
	e.Register(Workflow{
		Name:    "agent",
		Version: 1,
		Start:   "llm",
		Steps: []Step{
			{Name: "llm", Fn: func(_ context.Context, in json.RawMessage) (*StepOutput, error) {
				out, _ := json.Marshal("done")
				return Done(out), nil
			}},
			{Name: "tool", Fn: func(_ context.Context, in json.RawMessage) (*StepOutput, error) {
				toolCalls++
				if toolCalls == 2 {
					return nil, fmt.Errorf("still broken")
				}
				out, _ := json.Marshal("tool result")
				return Goto("llm", out), nil
			}},
		},
	})

	// Resume — toolCalls==2, fails again
	e.Resume(context.Background(), id)
	run, _ = e.GetRun(id)
	if run.Status != StatusFailed {
		t.Fatalf("want failed, got %s", run.Status)
	}

	// Resume — toolCalls==3, succeeds, then llm returns Done
	err := e.Resume(context.Background(), id)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	run, _ = e.GetRun(id)
	if run.Status != StatusCompleted {
		t.Fatalf("want completed, got %s (err: %s)", run.Status, run.Error)
	}

	steps, _ := e.GetStepResults(id)
	// llm(ok) → tool(fail) | tool(fail) | tool(ok) → llm(done) = 5
	if len(steps) != 5 {
		t.Fatalf("want 5 step results, got %d", len(steps))
	}
}

func TestLoopViaSelfGoto(t *testing.T) {
	e := testEngine(t)

	e.Register(Workflow{
		Name:    "counter",
		Version: 1,
		Start:   "count",
		Steps: []Step{
			{Name: "count", Fn: func(_ context.Context, in json.RawMessage) (*StepOutput, error) {
				var n int
				json.Unmarshal(in, &n)
				n++
				out, _ := json.Marshal(n)
				if n < 3 {
					return Goto("count", out), nil
				}
				return Done(out), nil
			}},
		},
	})

	id, _ := e.Start(context.Background(), "counter", json.RawMessage(`0`), nil)

	run, _ := e.GetRun(id)
	if run.Status != StatusCompleted {
		t.Fatalf("want completed, got %s (error: %s)", run.Status, run.Error)
	}
	if string(run.Output) != "3" {
		t.Fatalf("want 3, got %s", run.Output)
	}

	steps, _ := e.GetStepResults(id)
	if len(steps) != 3 {
		t.Fatalf("want 3 step results, got %d", len(steps))
	}
}

func TestListRuns(t *testing.T) {
	e := testEngine(t)

	e.Register(Workflow{
		Name: "a", Version: 1, Start: "s",
		Steps: []Step{{Name: "s", Fn: func(_ context.Context, in json.RawMessage) (*StepOutput, error) {
			return Done(in), nil
		}}},
	})
	e.Register(Workflow{
		Name: "b", Version: 1, Start: "s",
		Steps: []Step{{Name: "s", Fn: func(_ context.Context, in json.RawMessage) (*StepOutput, error) {
			return nil, fmt.Errorf("boom")
		}}},
	})

	e.Start(context.Background(), "a", nil, nil)
	e.Start(context.Background(), "a", nil, nil)
	e.Start(context.Background(), "b", nil, nil)

	all, _ := e.ListRuns(nil)
	if len(all) != 3 {
		t.Fatalf("want 3 runs, got %d", len(all))
	}

	failed, _ := e.ListRuns(&ListRunsOpts{Status: StatusFailed})
	if len(failed) != 1 {
		t.Fatalf("want 1 failed run, got %d", len(failed))
	}

	limited, _ := e.ListRuns(&ListRunsOpts{Limit: 2})
	if len(limited) != 2 {
		t.Fatalf("want 2 runs, got %d", len(limited))
	}
}

func TestResumeRejectsNonFailed(t *testing.T) {
	e := testEngine(t)
	e.Register(Workflow{
		Name: "ok", Version: 1, Start: "s",
		Steps: []Step{{Name: "s", Fn: func(_ context.Context, in json.RawMessage) (*StepOutput, error) {
			return Done(in), nil
		}}},
	})
	id, _ := e.Start(context.Background(), "ok", nil, nil)
	err := e.Resume(context.Background(), id)
	if err == nil {
		t.Fatal("expected error resuming completed run")
	}
}

func TestTags(t *testing.T) {
	e := testEngine(t)

	e.Register(Workflow{
		Name: "w", Version: 1, Start: "s",
		Steps: []Step{{Name: "s", Fn: func(_ context.Context, in json.RawMessage) (*StepOutput, error) {
			return Done(in), nil
		}}},
	})

	id1, _ := e.Start(context.Background(), "w", nil, &StartOpts{
		Tags: map[string]string{"conversation_id": "conv-1", "user": "alice"},
	})
	id2, _ := e.Start(context.Background(), "w", nil, &StartOpts{
		Tags: map[string]string{"conversation_id": "conv-2", "user": "alice"},
	})
	e.Start(context.Background(), "w", nil, nil)

	run, _ := e.GetRun(id1)
	if run.Tags["conversation_id"] != "conv-1" {
		t.Fatalf("want conv-1, got %v", run.Tags)
	}
	if run.Tags["user"] != "alice" {
		t.Fatalf("want alice, got %v", run.Tags)
	}

	// Filter by single tag
	conv1, _ := e.ListRuns(&ListRunsOpts{Tags: map[string]string{"conversation_id": "conv-1"}})
	if len(conv1) != 1 || conv1[0].ID != id1 {
		t.Fatalf("want 1 run for conv-1, got %d", len(conv1))
	}

	// Filter by multiple tags
	alice, _ := e.ListRuns(&ListRunsOpts{Tags: map[string]string{"user": "alice"}})
	if len(alice) != 2 {
		t.Fatalf("want 2 runs for alice, got %d", len(alice))
	}

	// Filter by tag + status
	aliceConv2, _ := e.ListRuns(&ListRunsOpts{
		Tags: map[string]string{"conversation_id": "conv-2"},
	})
	if len(aliceConv2) != 1 || aliceConv2[0].ID != id2 {
		t.Fatalf("want 1 run for conv-2, got %d", len(aliceConv2))
	}

	// Run without tags has nil tags
	noTag, _ := e.ListRuns(&ListRunsOpts{Tags: map[string]string{"conversation_id": "conv-1"}})
	for _, r := range noTag {
		if r.Tags["conversation_id"] != "conv-1" {
			t.Fatalf("unexpected run %s in conv-1 results", r.ID)
		}
	}
}

func TestRegisterValidation(t *testing.T) {
	e := testEngine(t)

	err := e.Register(Workflow{Name: "bad", Version: 1, Start: "missing", Steps: []Step{
		{Name: "a", Fn: func(_ context.Context, in json.RawMessage) (*StepOutput, error) { return Done(in), nil }},
	}})
	if err == nil {
		t.Fatal("expected error for missing start step")
	}

	err = e.Register(Workflow{Name: "bad2", Version: 1, Steps: []Step{
		{Name: "a", Fn: func(_ context.Context, in json.RawMessage) (*StepOutput, error) { return Done(in), nil }},
	}})
	if err == nil {
		t.Fatal("expected error for empty start")
	}
}
