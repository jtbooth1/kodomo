package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"kodomo/workflow"

	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// llmStep calls the OpenAI Responses API and parses the result.
// If the response contains function calls, it transitions to the "tool" step.
// A bare text response (no tool calls) is treated as an error.
func (a *Agent) llmStep(ctx context.Context, input json.RawMessage) (*workflow.StepOutput, error) {
	var state stepState
	if err := json.Unmarshal(input, &state); err != nil {
		return nil, fmt.Errorf("llm: unmarshal state: %w", err)
	}

	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(a.resolveModel()),
		Tools: a.toolParams(),
	}

	if a.config.Instructions != "" {
		params.Instructions = param.NewOpt(a.config.Instructions)
	}

	effort := a.resolveReasoningEffort()
	if effort != "" {
		params.Reasoning = shared.ReasoningParam{Effort: effort}
	}

	if state.PrevResponseID != "" {
		params.PreviousResponseID = param.NewOpt(state.PrevResponseID)
	}

	// Build input: either a user message or tool results from the previous tool step.
	if len(state.ToolResults) > 0 {
		var items responses.ResponseInputParam
		for _, tr := range state.ToolResults {
			items = append(items, responses.ResponseInputItemParamOfFunctionCallOutput(
				tr.CallID, string(tr.Output),
			))
		}
		params.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: items}
	} else if state.UserMessage != "" {
		params.Input = responses.ResponseNewParamsInputUnion{
			OfString: param.NewOpt(state.UserMessage),
		}
	}

	resp, err := a.client.Responses.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("llm: api call: %w", err)
	}

	var calls []toolCall
	for _, item := range resp.Output {
		if item.Type == "function_call" {
			calls = append(calls, toolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: item.Arguments,
			})
		}
	}

	if len(calls) == 0 {
		return nil, fmt.Errorf("llm: model returned no tool calls (bare text responses are not allowed)")
	}

	next, _ := json.Marshal(stepState{
		PrevResponseID: resp.ID,
		ToolCalls:      calls,
	})
	return workflow.Goto("tool", next), nil
}

func (a *Agent) resolveModel() string {
	if a.runOpts.Model != "" {
		return a.runOpts.Model
	}
	return a.config.Model
}

func (a *Agent) resolveReasoningEffort() shared.ReasoningEffort {
	if a.runOpts.ReasoningEffort != "" {
		return a.runOpts.ReasoningEffort
	}
	return a.config.ReasoningEffort
}
