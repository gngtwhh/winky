package openai

import (
	"context"
	"fmt"
	"winky/internal/llm"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// ResponsesProtocol holds an OpenAI API client and implements the llm.Streamer interface
type ResponsesProtocol struct {
	client *openai.Client
}

func NewResponses(cfg Config) *ResponsesProtocol {
	return &ResponsesProtocol{
		client: newClient(cfg),
	}
}

func (rp *ResponsesProtocol) Stream(
	ctx context.Context, req llm.Request,
) (<-chan llm.StreamChunk, error) {
	params, err := buildResponsesParams(req)
	if err != nil {
		return nil, fmt.Errorf("failed to build responses params: %w", err)
	}

	stream := rp.client.Responses.NewStreaming(ctx, params)
	ch := make(chan llm.StreamChunk, 32)

	go func() {
		defer close(ch)
		defer stream.Close()

		consumeResponseStream(ctx, stream, ch)
	}()

	return ch, nil
}

func (*ResponsesProtocol) Name() string {
	return "openai-responses"
}

func buildResponsesParams(req llm.Request) (responses.ResponseNewParams, error) {
	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(req.Model),
	}

	if req.SystemPrompt != "" {
		params.Instructions = openai.String(req.SystemPrompt)
	}

	if len(req.Messages) > 0 {
		input, err := convertMessagesToInput(req.Messages)
		if err != nil {
			return params, err
		}
		params.Input = input
	}

	if len(req.Tools) > 0 {
		params.Tools = convertToolsToParams(req.Tools)
	}

	if req.Temperature != 0 {
		params.Temperature = openai.Float(req.Temperature)
	}
	if req.MaxTokens > 0 {
		params.MaxOutputTokens = openai.Int(int64(req.MaxTokens))
	}

	return params, nil
}

func convertMessagesToInput(msgs []llm.Message) (responses.ResponseNewParamsInputUnion, error) {
	var items responses.ResponseInputParam

	for _, m := range msgs {
		switch m.Role {
		case llm.RoleUser:
			items = append(
				items, responses.ResponseInputItemParamOfMessage(
					m.Content, responses.EasyInputMessageRoleUser,
				),
			)

		case llm.RoleAssistant:
			if len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					items = append(
						items, responses.ResponseInputItemUnionParam{
							OfFunctionCall: &responses.ResponseFunctionToolCallParam{
								CallID:    tc.ID,
								Name:      tc.Name,
								Arguments: tc.Arguments,
							},
						},
					)
				}
			} else {
				items = append(
					items, responses.ResponseInputItemParamOfMessage(
						m.Content, responses.EasyInputMessageRoleAssistant,
					),
				)
			}

		case llm.RoleToolOutput:
			items = append(
				items, responses.ResponseInputItemUnionParam{
					OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
						CallID: openai.String(m.ToolCallID),
						Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{
							OfString: openai.String(m.Content),
						},
					},
				},
			)
		}
	}

	return responses.ResponseNewParamsInputUnion{
		OfInputItemList: items,
	}, nil
}

func convertToolsToParams(specs []llm.ToolSpec) []responses.ToolUnionParam {
	tools := make([]responses.ToolUnionParam, len(specs))
	for i, s := range specs {
		tool := responses.ToolParamOfFunction(s.Name, s.Parameters, s.Strict)
		tool.OfFunction.Description = openai.String(s.Description)
		tools[i] = tool
	}
	return tools
}
