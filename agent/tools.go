package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
)

// AddTool registers a tool that the LLM can call.
func (a *Agent) AddTool(t ToolDef) error {
	if t.Name == "" {
		return fmt.Errorf("agent: tool name is required")
	}
	if t.Handler == nil {
		return fmt.Errorf("agent: tool %q has no handler", t.Name)
	}
	a.tools[t.Name] = t
	return nil
}

func (a *Agent) registerBuiltinTools() {
	a.tools["message_user"] = ToolDef{
		Name:        "message_user",
		Description: "Send a message to the user. This ends the current turn.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{
					"type":        "string",
					"description": "The message to display to the user.",
				},
			},
			"required":             []string{"message"},
			"additionalProperties": false,
		},
		Handler: func(_ context.Context, params json.RawMessage) (json.RawMessage, error) {
			var p struct{ Message string }
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("message_user: %w", err)
			}
			a.messageHandler(p.Message)
			return json.Marshal(map[string]string{"status": "delivered"})
		},
		Terminal: true,
	}
}

// toolParams converts registered tools into OpenAI SDK tool params.
func (a *Agent) toolParams() []responses.ToolUnionParam {
	params := make([]responses.ToolUnionParam, 0, len(a.tools))
	for _, t := range a.tools {
		params = append(params, responses.ToolUnionParam{
			OfFunction: &responses.FunctionToolParam{
				Name:        t.Name,
				Description: param.NewOpt(t.Description),
				Parameters:  t.Schema,
			},
		})
	}
	return params
}
