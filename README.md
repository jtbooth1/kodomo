# kodomo

`kodomo` is a small Go codebase for running a durable local coding agent.

The project has four main parts:

- `workflow`: a SQLite-backed workflow engine that persists each step attempt and can resume failed runs
- `agent`: a two-step `llm -> tool -> llm` agent built on OpenAI's Responses API
- `tools`: built-in filesystem and shell tools exposed to the agent
- `browser`: a lightweight web UI for inspecting persisted runs in the SQLite database

## Repository layout

| Path | Purpose |
| --- | --- |
| `cmd/kodomo` | Interactive CLI entrypoint |
| `cmd/kodomo-browser` | Browser UI entrypoint |
| `agent/` | Agent runtime, OpenAI integration, tool registration |
| `workflow/` | Durable workflow engine and tests |
| `tools/` | Built-in `read`, `write`, `edit`, and `bash` tools |
| `browser/` | SSR run history viewer backed directly by SQLite |
| `cli/` | Simple REPL wrapper around `agent.Agent` |

## How it works

`cmd/kodomo` creates `~/.kodomo/kodomo.db`, registers the built-in tools against a working directory, and starts a REPL.

Each user message becomes one workflow run:

1. The `agent` package serializes the prompt into workflow state.
2. The `workflow` engine starts the `agent` workflow at the `llm` step.
3. `llmStep` calls the OpenAI Responses API with the configured model and registered tools.
4. If the model emits function calls, `toolStep` executes them and feeds their outputs back into `llmStep`.
5. If the model emits plain text, the workflow completes and the final state is stored as the run output.

All runs and step attempts are written to SQLite:

- `runs`: one row per workflow run
- `step_results`: one row per step attempt
- `run_tags`: arbitrary key/value metadata such as `conversation_id`

## Running locally

Requirements:

- Go 1.23+
- `OPENAI_API_KEY` set in the environment

Start the CLI:

```bash
go run ./cmd/kodomo
```

Optionally pass a working directory for the built-in tools:

```bash
go run ./cmd/kodomo /path/to/project
```

Start the browser UI against the default database:

```bash
go run ./cmd/kodomo-browser
```

Or point it at a specific database:

```bash
go run ./cmd/kodomo-browser -db /path/to/kodomo.db -addr :8080
```

## Built-in tools

The `tools.Register` helper adds four tools to an `agent.Agent`:

- `read`: reads text files with optional line offset and limit
- `write`: writes full file contents, creating parent directories as needed
- `edit`: replaces an exact text fragment once
- `bash`: runs a shell command in the configured working directory with an optional timeout

## Testing

Run the full test suite with:

```bash
go test ./...
```

Current package coverage is strongest in:

- `workflow/engine_test.go`
- `agent/agent_test.go`
- `tools/tools_test.go`

`browser`, `cli`, and the two `cmd/` entrypoints currently have no direct tests.

## Useful files to open first

When returning to this repo later, these are the fastest way back into the code:

- `cmd/kodomo/main.go`
- `agent/agent.go`
- `agent/llm.go`
- `workflow/engine.go`
- `tools/tools.go`
- `browser/browser.go`

There are also package-level notes in `agent/README.md` and `workflow/README.md`, but this root README is intended to reflect the current top-level wiring of the repository.
