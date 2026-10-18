package openai

// currently not implemented

// import (
// 	"context"
// 	"fmt"
//
// 	"winky/internal/llm"
//
// 	"github.com/openai/openai-go/v3"
// 	"github.com/openai/openai-go/v3/packages/ssestream"
// 	"github.com/openai/openai-go/v3/shared"
// )
//
// // CompletionsProtocol holds an OpenAI API client and implements the llm.Streamer interface
// // using the OpenAI-compatible Chat Completions endpoint (used by DeepSeek and other
// // OpenAI-compatible providers).
// type CompletionsProtocol struct {
// 	client *openai.Client
// }
//
// func NewCompletions(cfg Config) *CompletionsProtocol {
// 	return &CompletionsProtocol{
// 		client: newClient(cfg),
// 	}
// }
//
// func (p *CompletionsProtocol) Stream(
// 	ctx context.Context, req llm.Request,
// ) (<-chan llm.StreamChunk, error) {
// 	params, err := buildChatCompletionsParams(req)
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to build chat completions params: %w", err)
// 	}
//
// 	stream := p.client.Chat.Completions.NewStreaming(ctx, params)
// 	ch := make(chan llm.StreamChunk, 32)
//
// 	go func() {
// 		defer close(ch)
// 		defer stream.Close()
//
// 		consumeChatCompletionsStream(ctx, stream, ch)
// 	}()
//
// 	return ch, nil
// }
//
// func (*CompletionsProtocol) Name() string {
// 	return "openai-completions"
// }
//
// func consumeChatCompletionsStream(
// 	ctx context.Context,
// 	stream *ssestream.Stream[openai.ChatCompletionChunk],
// 	ch chan<- llm.StreamChunk,
// ) {
// 	for stream.Next() {
// 		select {
// 		case <-ctx.Done():
// 			return
// 		default:
// 		}
//
// 		chunk := stream.Current()
//
// 		for _, choice := range chunk.Choices {
// 			if choice.Delta.Content != "" {
// 				ch <- llm.StreamChunk{Delta: choice.Delta.Content}
// 			}
//
// 			// TODO: tool call argument deltas arrive in fragments and should be
// 			// accumulated per call ID, same gap as the responses protocol.
// 			for _, tc := range choice.Delta.ToolCalls {
// 				ch <- llm.StreamChunk{
// 					ToolCall: &llm.ToolCall{
// 						ID:        tc.ID,
// 						Name:      tc.Function.Name,
// 						Arguments: tc.Function.Arguments,
// 					},
// 				}
// 			}
//
// 			if choice.FinishReason != "" {
// 				ch <- llm.StreamChunk{
// 					FinishReason: choice.FinishReason,
// 					Done:         true,
// 				}
// 			}
// 		}
//
// 		if chunk.Usage.TotalTokens > 0 {
// 			ch <- llm.StreamChunk{
// 				Usage: &llm.TokenUsage{
// 					PromptTokens:     int(chunk.Usage.PromptTokens),
// 					CompletionTokens: int(chunk.Usage.CompletionTokens),
// 					TotalTokens:      int(chunk.Usage.TotalTokens),
// 				},
// 			}
// 		}
// 	}
//
// 	if err := stream.Err(); err != nil {
// 		ch <- llm.StreamChunk{FinishReason: "error"}
// 		return
// 	}
//
// 	ch <- llm.StreamChunk{Done: true}
// }
//
// func buildChatCompletionsParams(req llm.Request) (openai.ChatCompletionNewParams, error) {
// 	params := openai.ChatCompletionNewParams{
// 		Model: shared.ChatModel(req.Model),
// 	}
//
// 	msgs := make([]openai.ChatCompletionMessageParamUnion, 0, len(req.Messages)+1)
// 	if req.SystemPrompt != "" {
// 		msgs = append(msgs, openai.SystemMessage(req.SystemPrompt))
// 	}
// 	for _, m := range req.Messages {
// 		switch m.Role {
// 		case llm.RoleUser:
// 			msgs = append(msgs, openai.UserMessage(m.Content))
// 		case llm.RoleAssistant:
// 			msgs = append(msgs, openai.AssistantMessage(m.Content))
// 		case llm.RoleToolOutput:
// 			msgs = append(msgs, openai.ToolMessage(m.Content, m.ToolCallID))
// 		}
// 	}
// 	params.Messages = msgs
//
// 	if req.Temperature != 0 {
// 		params.Temperature = openai.Float(req.Temperature)
// 	}
// 	if req.MaxOutputTokens > 0 {
// 		params.MaxTokens = openai.Int(int64(req.MaxOutputTokens))
// 	}
//
// 	if len(req.Tools) > 0 {
// 		params.Tools = convertToolsToChatParams(req.Tools)
// 	}
//
// 	return params, nil
// }
//
// func convertToolsToChatParams(specs []llm.ToolSpec) []openai.ChatCompletionToolUnionParam {
// 	tools := make([]openai.ChatCompletionToolUnionParam, len(specs))
// 	for i, s := range specs {
// 		tools[i] = openai.ChatCompletionToolUnionParam{
// 			OfFunction: &openai.ChatCompletionFunctionToolParam{
// 				Function: shared.FunctionDefinitionParam{
// 					Name:        s.Name,
// 					Description: openai.String(s.Description),
// 					Parameters:  shared.FunctionParameters(s.Parameters),
// 					Strict:      openai.Bool(s.Strict),
// 				},
// 			},
// 		}
// 	}
// 	return tools
// }
