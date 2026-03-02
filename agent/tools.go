package agent

import (
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
