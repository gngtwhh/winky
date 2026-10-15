package llm

type Chunk interface {
	llmChunkType() string
}

// type StreamChunk struct {
// 	Delta        string
// 	ToolCall     *ToolCall
// 	Usage        *TokenUsage
// 	FinishReason string
// 	Done         bool
// }

type TextDelta struct{ Text string }

type ReasoningDelta struct{ Text string }
type ToolCallStart struct{ ID, Name string }
type ToolCallArgsDelta struct {
	ID        string
	Arguments string
}
type ToolCallEnd struct{ ID, Arguments string } // 或直接给重组后的完整 ToolCall
type UsageChunk struct{ Usage TokenUsage }
type FinishChunk struct {
	Reason string
	Err    error
}
type ErrorChunk struct{ Err error }

type UnknownChunk struct{ Raw string }

func (c TextDelta) llmChunkType() string {
	return "text_delta"
}

func (c ReasoningDelta) llmChunkType() string {
	return "reasoning_delta"
}

func (c ToolCallStart) llmChunkType() string {
	return "tool_call_start"
}

func (c ToolCallArgsDelta) llmChunkType() string {
	return "tool_call_args_delta"
}

func (c ToolCallEnd) llmChunkType() string {
	return "tool_call_end"
}

func (c UsageChunk) llmChunkType() string {
	return "usage_chunk"
}
func (c FinishChunk) llmChunkType() string {
	return "finish_chunk"
}

func (c ErrorChunk) llmChunkType() string {
	return "error_chunk"
}

func (c UnknownChunk) llmChunkType() string {
	return "unknown_chunk"
}
