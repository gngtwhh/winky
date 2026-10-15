package openai

import (
	"context"
	"fmt"
	"winky/internal/llm"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"
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
) (<-chan llm.Chunk, error) {
	params, err := buildResponsesParams(req)
	if err != nil {
		return nil, fmt.Errorf("failed to build responses params: %w", err)
	}

	stream := rp.client.Responses.NewStreaming(ctx, params)
	ch := make(chan llm.Chunk, 32)

	go func() {
		defer close(ch)
		defer stream.Close()

		consumeResponsesStream(ctx, stream, ch)
	}()

	return ch, nil
}

func (*ResponsesProtocol) Name() string {
	return "openai-responses"
}

func consumeResponsesStream(
	ctx context.Context,
	stream *ssestream.Stream[responses.ResponseStreamEventUnion],
	ch chan<- llm.Chunk,
) {
	hasFuncCall := false
	for stream.Next() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		event := stream.Current()

		switch e := event.AsAny().(type) {
		case responses.ResponseTextDeltaEvent:
			ch <- llm.TextDelta{Text: e.Delta}
		case responses.ResponseFunctionCallArgumentsDeltaEvent:
			if !hasFuncCall {
				hasFuncCall = true
				ch <- llm.ToolCallStart{
					ID: e.ItemID, /* TODO: error, change to CallID */
				}
			} else {
				ch <- llm.ToolCallArgsDelta{
					ID:        e.ItemID,
					Arguments: e.Delta,
				}
			}
		case responses.ResponseFunctionCallArgumentsDoneEvent:
			hasFuncCall = false
			ch <- llm.ToolCallEnd{
				ID:        e.ItemID,
				Arguments: e.Arguments,
			}
		case responses.ResponseReasoningTextDeltaEvent:
			ch <- llm.ReasoningDelta{Text: e.Delta}
		case responses.ResponseReasoningTextDoneEvent:
		case responses.ResponseCompletedEvent:
			ch <- llm.FinishChunk{
				Reason: "stop",
			}
			return
		case responses.ResponseFailedEvent:
			ch <- llm.ErrorChunk{
				Err: fmt.Errorf("%s", e.RawJSON()),
			}
			return
		case responses.ResponseIncompleteEvent:
			ch <- llm.ErrorChunk{
				Err: fmt.Errorf("%s", e.RawJSON()),
			}
			return
		case responses.ResponseErrorEvent:
			ch <- llm.ErrorChunk{
				Err: fmt.Errorf("response error: code=%q msg=%q", e.Code, e.Message),
			}
			return
		default:
			ch <- llm.UnknownChunk{
				Raw: event.RawJSON(),
			}
		}
	}

	if err := stream.Err(); err != nil {
		ch <- llm.ErrorChunk{
			Err: fmt.Errorf("%w", err),
		}
		return
	}

	ch <- llm.FinishChunk{Reason: "stop"}
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

func docDontCallThis() {
	var event responses.ResponseStreamEventUnion
	switch v := event.AsAny().(type) {
	case responses.ResponseAudioDeltaEvent:
	case responses.ResponseAudioDoneEvent:
	case responses.ResponseAudioTranscriptDeltaEvent:
	case responses.ResponseAudioTranscriptDoneEvent:
	case responses.ResponseCodeInterpreterCallCodeDeltaEvent:
	case responses.ResponseCodeInterpreterCallCodeDoneEvent:
	case responses.ResponseCodeInterpreterCallCompletedEvent:
	case responses.ResponseCodeInterpreterCallInProgressEvent:
	case responses.ResponseCodeInterpreterCallInterpretingEvent:
	case responses.ResponseCompactionCompactingEvent:
	case responses.ResponseCompletedEvent:
	case responses.ResponseContentPartAddedEvent:
	case responses.ResponseContentPartDoneEvent:
	case responses.ResponseCreatedEvent:
	case responses.ResponseErrorEvent:
	case responses.ResponseFileSearchCallCompletedEvent:
	case responses.ResponseFileSearchCallInProgressEvent:
	case responses.ResponseFileSearchCallSearchingEvent:
	case responses.ResponseFunctionCallArgumentsDeltaEvent:
	case responses.ResponseFunctionCallArgumentsDoneEvent:
	case responses.ResponseShellCallCommandAddedEvent:
	case responses.ResponseShellCallCommandDeltaEvent:
	case responses.ResponseShellCallCommandDoneEvent:
	case responses.ResponseShellCallOutputContentDeltaEvent:
	case responses.ResponseShellCallOutputContentDoneEvent:
	case responses.ResponseInProgressEvent:
	case responses.ResponseFailedEvent:
	case responses.ResponseIncompleteEvent:
	case responses.ResponseOutputItemAddedEvent:
	case responses.ResponseOutputItemDoneEvent:
	case responses.ResponseReasoningSummaryPartAddedEvent:
	case responses.ResponseReasoningSummaryPartDoneEvent:
	case responses.ResponseReasoningSummaryTextDeltaEvent:
	case responses.ResponseReasoningSummaryTextDoneEvent:
	case responses.ResponseReasoningTextDeltaEvent:
	case responses.ResponseReasoningTextDoneEvent:
	case responses.ResponseRefusalDeltaEvent:
	case responses.ResponseRefusalDoneEvent:
	case responses.ResponseTextDeltaEvent:
	case responses.ResponseTextDoneEvent:
	case responses.ResponseWebSearchCallCompletedEvent:
	case responses.ResponseWebSearchCallInProgressEvent:
	case responses.ResponseWebSearchCallSearchingEvent:
	case responses.ResponseImageGenCallCompletedEvent:
	case responses.ResponseImageGenCallGeneratingEvent:
	case responses.ResponseImageGenCallInProgressEvent:
	case responses.ResponseImageGenCallPartialImageEvent:
	case responses.ResponseMcpCallArgumentsDeltaEvent:
	case responses.ResponseMcpCallArgumentsDoneEvent:
	case responses.ResponseMcpCallCompletedEvent:
	case responses.ResponseMcpCallFailedEvent:
	case responses.ResponseMcpCallInProgressEvent:
	case responses.ResponseMcpListToolsCompletedEvent:
	case responses.ResponseMcpListToolsFailedEvent:
	case responses.ResponseMcpListToolsInProgressEvent:
	case responses.ResponseOutputTextAnnotationAddedEvent:
	case responses.ResponseQueuedEvent:
	case responses.ResponseCustomToolCallInputDeltaEvent:
	case responses.ResponseCustomToolCallInputDoneEvent:
	default:
		fmt.Println(v)
	}
}
