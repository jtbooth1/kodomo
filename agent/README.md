# agent

A coding agent built on the `kodomo/workflow` engine and OpenAI's Responses API. The agent runs a durable `llm <-> tool` loop: the LLM produces tool calls, the agent executes them, and the results are fed back. Every step is persisted to SQLite, so failed runs (e.g. API timeouts) can be resumed from exactly where they stopped.

## Import

```go
import "kodomo/agent"
```

## Quick start

```go
e, _ := workflow.Open("kodomo.db")
defer e.Close()

a, _ := agent.New(e, agent.Config{
    Model:           "gpt-5.2-codex",
    ReasoningEffort: shared.ReasoningEffortMedium,
    Instructions:    "You are a coding assistant.",
})

runID, _ := a.Start(ctx, "What files are in this directory?", nil)
```

## How it works

The agent registers a workflow with two steps:

- **`llm`** — Calls the OpenAI Responses API with the registered tools and current conversation state. Parses the response for function calls. Bare text responses are treated as errors — all output must go through a tool like `message_user`.
- **`tool`** — Executes each tool call handler sequentially. If any tool is marked `Terminal`, the run completes. Otherwise, results are sent back to the `llm` step.

```
Start("prompt") -> llm -> tool -> llm -> tool -> ... -> tool(terminal) -> Done
```

Each arrow is a persisted `StepResult` in SQLite. The full conversation history (LLM inputs, tool calls, tool outputs) is captured in the step data.

## Configuration

`Config` sets defaults for all runs:

```go
type Config struct {
    Model           string                 // required, e.g. "gpt-5.2-codex"
    ReasoningEffort shared.ReasoningEffort // "low", "medium", "high", etc.
    Instructions    string                 // system prompt
}
```

`RunOpts` overrides per-run (zero values fall back to Config):

```go
type RunOpts struct {
    Model           string
    ReasoningEffort shared.ReasoningEffort
    ConversationID  string // tags the run for conversation grouping
    PrevResponseID  string // continues an OpenAI conversation
}
```

## Tools

Tools are registered with `AddTool`. Each tool has a name, JSON Schema for parameters, and a handler function:

```go
a.AddTool(agent.ToolDef{
    Name:        "read_file",
    Description: "Read a file from disk",
    Schema: map[string]any{
        "type": "object",
        "properties": map[string]any{
            "path": map[string]any{"type": "string"},
        },
        "required": []string{"path"},
    },
    Handler: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
        var p struct{ Path string }
        json.Unmarshal(params, &p)
        data, err := os.ReadFile(p.Path)
        if err != nil {
            return nil, err
        }
        return json.Marshal(string(data))
    },
})
```

### Terminal tools

Set `Terminal: true` on a tool to end the run after it executes. The built-in `message_user` tool is terminal — when the LLM calls it, the message is delivered and the run completes.

### Built-in tools

- **`message_user`** — Sends a message to the user. Terminal. The handler calls a configurable function (defaults to `fmt.Println`). Override with `WithMessageHandler(fn)`.

## Conversations

Each `Start` call creates a new workflow run. To group runs into a conversation, pass a `ConversationID` in `RunOpts`:

```go
a.Start(ctx, "Hello", &agent.RunOpts{
    ConversationID: "conv-abc",
    PrevResponseID: lastResponseID, // from a previous run
})
```

The `ConversationID` is stored as a tag on the workflow run, so you can query all runs in a conversation:

```go
runs, _ := engine.ListRuns(&workflow.ListRunsOpts{
    Tags: map[string]string{"conversation_id": "conv-abc"},
})
```

`PrevResponseID` tells OpenAI to continue the conversation server-side, so the model has full context without re-sending message history.

## Resuming failed runs

If a run fails (e.g. API timeout on the LLM step, or a tool error), call `Resume` to retry from the failed step:

```go
a.Resume(ctx, runID)
```

The workflow engine replays from the exact step that failed, with the same input. Already-completed steps are not re-executed.

## API key

The OpenAI client reads `OPENAI_API_KEY` from the environment (the openai-go SDK default).
