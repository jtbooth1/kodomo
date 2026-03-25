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
// Tool calls transition to the "tool" step. A text response is the final
// answer — it ends the run with the message as output.
func (a *Agent) llmStep(ctx context.Context, input json.RawMessage) (*workflow.StepOutput, error) {
	var state stepState
	if err := json.Unmarshal(input, &state); err != nil {
		return nil, fmt.Errorf("llm: unmarshal state: %w", err)
	}

	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(a.resolveModel(state)),
		Tools: a.toolParams(),
	}

	if a.config.Instructions != "" {
		params.Instructions = param.NewOpt(a.config.Instructions)
	}

	effort := a.resolveReasoningEffort(state)
	if effort != "" {
		params.Reasoning = shared.ReasoningParam{Effort: effort}
	}

	if state.PrevResponseID != "" {
		params.PreviousResponseID = param.NewOpt(state.PrevResponseID)
	}

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

	if len(calls) > 0 {
		next, _ := json.Marshal(stepState{
			Model:           state.Model,
			ReasoningEffort: state.ReasoningEffort,
			PrevResponseID:  resp.ID,
			ToolCalls:       calls,
		})
		return workflow.Goto("tool", next), nil
	}

	// Text response — this is the message to the user. Done.
	out, _ := json.Marshal(stepState{
		Model:           state.Model,
		ReasoningEffort: state.ReasoningEffort,
		PrevResponseID:  resp.ID,
		Message:         resp.OutputText(),
	})
	return workflow.Done(out), nil
}

func (a *Agent) resolveModel(state stepState) string {
	if state.Model != "" {
		return state.Model
	}
	return a.config.Model
}

func (a *Agent) resolveReasoningEffort(state stepState) shared.ReasoningEffort {
	if state.ReasoningEffort != "" {
		return state.ReasoningEffort
	}
	return a.config.ReasoningEffort
}
