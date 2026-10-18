package openai

import (
	"context"
	"fmt"
	"time"
	"winky/internal/llm"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// ResponsesProtocol implements llm.Streamer over the Responses API.
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

// consumeResponsesStream translates Responses SSE events into semantic
// llm.Chunk values. The authoritative result is accumulated by the terminal
// event and delivered on ResponseEnd.
func consumeResponsesStream(
	ctx context.Context,
	stream *ssestream.Stream[responses.ResponseStreamEventUnion],
	ch chan<- llm.Chunk,
) {
	// itemID -> call_id for function calls: the arguments delta/done events are
	// keyed by item_id while the round-trip needs call_id.
	toolCallIDs := make(map[string]string)

	for stream.Next() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		event := stream.Current()

		switch e := event.AsAny().(type) {
		// Lifecycle.
		case responses.ResponseCreatedEvent:
			ch <- llm.ResponseStart{Timestamp: time.Now()}

		case responses.ResponseCompletedEvent:
			ch <- llm.ResponseEnd{
				Timestamp:  time.Now(),
				StopReason: llm.StopReasonDone,
				Response:   buildResponse(e.Response),
			}
			return

		case responses.ResponseFailedEvent:
			ch <- llm.ResponseEnd{
				Timestamp:  time.Now(),
				StopReason: llm.StopReasonFailed,
				Response:   buildResponse(e.Response),
			}
			return

		case responses.ResponseErrorEvent:
			ch <- llm.ResponseEnd{
				Timestamp:  time.Now(),
				StopReason: llm.StopReasonError,
				Err:        fmt.Errorf("errcode=%q,errmsg=%q", e.Code, e.Message),
			}
			return

		case responses.ResponseIncompleteEvent:
			ch <- llm.ResponseEnd{
				Timestamp:  time.Now(),
				StopReason: llm.StopReasonIncomplete,
				Response:   buildResponse(e.Response),
			}
			return

		// Function tool call: item_id-keyed events resolve to the item's call_id.
		case responses.ResponseOutputItemAddedEvent:
			if e.Item.Type == "function_call" {
				f := e.Item.AsFunctionCall()
				toolCallIDs[e.Item.ID] = f.CallID
				ch <- llm.ToolCallStart{CallID: f.CallID, Name: f.Name}
			}

		case responses.ResponseFunctionCallArgumentsDeltaEvent:
			ch <- llm.ToolCallArgsDelta{
				CallID: toolCallID(toolCallIDs, e.ItemID),
				Delta:  e.Delta,
			}

		case responses.ResponseFunctionCallArgumentsDoneEvent:
			ch <- llm.ToolCallEnd{
				CallID:    toolCallID(toolCallIDs, e.ItemID),
				Arguments: e.Arguments,
			}

		// Text deltas. Part order is surfaced by the terminal Response; the
		// content_index / summary_index carried here are not needed.
		case responses.ResponseReasoningTextDeltaEvent:
			ch <- llm.ReasoningDelta{ItemID: e.ItemID, Text: e.Delta}

		case responses.ResponseReasoningSummaryTextDeltaEvent:
			ch <- llm.ReasoningSummaryDelta{ItemID: e.ItemID, Text: e.Delta}

		case responses.ResponseTextDeltaEvent:
			ch <- llm.OutputDelta{ItemID: e.ItemID, Text: e.Delta}

		case responses.ResponseRefusalDeltaEvent:
			ch <- llm.RefusalDelta{ItemID: e.ItemID, Text: e.Delta}

		// Authoritative done events; their data already rides the terminal
		// response.
		case responses.ResponseOutputItemDoneEvent,
			responses.ResponseContentPartAddedEvent,
			responses.ResponseContentPartDoneEvent,
			responses.ResponseReasoningTextDoneEvent,
			responses.ResponseReasoningSummaryTextDoneEvent,
			responses.ResponseTextDoneEvent,
			responses.ResponseRefusalDoneEvent:
			// intentionally empty

		// Everything else (hosted tools, audio, queued, compaction, mcp, ...) is
		// intentionally ignored until a specific tool type is supported.
		default:
			// debug only:
			// ch <- llm.UnknownChunk{Raw: event.RawJSON()}
		}
	}

	if err := stream.Err(); err != nil {
		ch <- llm.ResponseEnd{
			Timestamp:  time.Now(),
			StopReason: llm.StopReasonError,
			Err:        fmt.Errorf("OpenAIStreamError: %w", err),
		}
		return
	}

	ch <- llm.ResponseEnd{
		Timestamp:  time.Now(),
		StopReason: llm.StopReasonUnknown,
	}
}

// toolCallID resolves an item_id to its call_id, falling back to the item_id
// when the function_call item was not observed.
func toolCallID(m map[string]string, itemID string) string {
	if id := m[itemID]; id != "" {
		return id
	}
	return itemID
}

// buildResponse converts a terminal provider response into the llm.Response.
func buildResponse(r responses.Response) *llm.Response {
	out := &llm.Response{
		ID:    r.ID,
		Usage: toTokenUsage(r.Usage),
	}
	for _, item := range r.Output {
		out.Output = append(out.Output, convertOutputItem(item))
	}
	return out
}

func convertOutputItem(u responses.ResponseOutputItemUnion) llm.OutputItem {
	out := llm.OutputItem{
		Type:   llm.ItemType(u.Type),
		ID:     u.ID,
		Status: u.Status,
		Raw:    u.RawJSON(),
	}

	switch u.Type {
	case "message":
		m := u.AsMessage()
		out.Phase = string(m.Phase)
		for _, p := range m.Content {
			part := llm.ContentPart{Type: llm.PartType(p.Type)}
			switch p.Type {
			case "output_text":
				part.Text = p.AsOutputText().Text
			case "refusal":
				part.Text = p.AsRefusal().Refusal
			}
			out.Content = append(out.Content, part)
		}

	case "reasoning":
		r := u.AsReasoning()
		out.EncryptedContent = r.EncryptedContent
		for _, p := range r.Content {
			out.Content = append(
				out.Content, llm.ContentPart{
					Type: llm.PartReasoningText,
					Text: p.Text,
				},
			)
		}
		for _, p := range r.Summary {
			out.Summary = append(
				out.Summary, llm.ContentPart{
					Type: llm.PartReasoningSummary,
					Text: p.Text,
				},
			)
		}

	case "function_call":
		f := u.AsFunctionCall()
		out.CallID = f.CallID
		out.Name = f.Name
		out.Arguments = f.Arguments
	}

	return out
}

// toTokenUsage maps a provider usage struct to the shared llm.TokenUsage.
//
// It returns nil only when nothing at all was reported: keying the check off
// total_tokens alone would drop the whole record (including the cache
// breakdown) for gateways that omit it. A missing total is derived from
// input+output, since downstream code treats a zero TotalTokens as "no usage".
func toTokenUsage(u responses.ResponseUsage) *llm.TokenUsage {
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.TotalTokens == 0 {
		return nil
	}
	total := int(u.TotalTokens)
	if total == 0 {
		total = int(u.InputTokens + u.OutputTokens)
	}
	return &llm.TokenUsage{
		PromptTokens:     int(u.InputTokens),
		CompletionTokens: int(u.OutputTokens),
		TotalTokens:      total,
		CachedTokens:     int(u.InputTokensDetails.CachedTokens),
	}
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
	if req.MaxOutputTokens > 0 {
		params.MaxOutputTokens = openai.Int(int64(req.MaxOutputTokens))
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
