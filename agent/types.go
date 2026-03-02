package agent

import (
	"context"
	"encoding/json"

	"github.com/openai/openai-go/v3/shared"
)

// Config sets the defaults for all runs on an Agent.
type Config struct {
	Model           string                 // e.g. "gpt-4.1", "o3"
	ReasoningEffort shared.ReasoningEffort // "low", "medium", "high", etc.
	Instructions    string                 // system prompt
}

// RunOpts overrides Config defaults for a single run.
// Zero values are ignored (the Config default is used).
type RunOpts struct {
	Model           string
	ReasoningEffort shared.ReasoningEffort
	ConversationID  string // tags the workflow run for grouping
	PrevResponseID  string // continues an OpenAI conversation
}

// ToolDef defines a tool the agent can call.
type ToolDef struct {
	Name        string
	Description string
	Schema      map[string]any // JSON Schema for parameters
	Handler     func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
}

// toolCall is a parsed function call from the LLM response.
type toolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// toolResult is the output of executing a tool call.
type toolResult struct {
	CallID string          `json:"call_id"`
	Output json.RawMessage `json:"output"`
}

// stepState is the JSON structure flowing between the llm and tool steps.
type stepState struct {
	PrevResponseID string       `json:"prev_response_id,omitempty"`
	UserMessage    string       `json:"user_message,omitempty"`
	ToolCalls      []toolCall   `json:"tool_calls,omitempty"`
	ToolResults    []toolResult `json:"tool_results,omitempty"`
	Message        string       `json:"message,omitempty"` // text response from the LLM (final output)
}
