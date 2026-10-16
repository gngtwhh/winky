package llm

import "time"

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

/* Agent Response */

const (
	StopReasonDone       = "done"
	StopReasonError      = "error"
	StopReasonFailed     = "failed"
	StopReasonIncomplete = "incomplete"
	StopReasonUnknown    = "unknown"
)

type AgentStart struct{ Timestamp time.Time }
type AgentEnd struct {
	Timestamp  time.Time
	StopReason string
	Err        error
	Usage      *TokenUsage
}

/* Reasoning Item */

type ReasoningItemAdd struct{ ItemID string }

type ReasoningItemDone struct{ ItemID string }

type ReasoningContentAdd struct{ ItemID string }
type ReasoningContentDelta struct {
	ItemID string
	Delta  string
}

type ReasoningContentDone struct {
	ItemID      string
	FullContent string
}

/* Output Item */

type OutputItemAdd struct{ ItemID string }

type OutputItemDone struct{ ItemID string }

type OutputContentAdd struct{ ItemID string }
type OutputContentDelta struct {
	ItemID string
	Delta  string
}

type OutputContentDone struct {
	ItemID      string
	FullContent string
}

/* Tool Call */

type ToolCallStart struct{ ID, Name string }
type ToolCallArgsDelta struct {
	ID        string
	Arguments string
}
type ToolCallEnd struct{ ID, Arguments string } // 或直接给重组后的完整 ToolCall

type UnknownChunk struct{ Raw string }

func (c AgentStart) llmChunkType() string {
	return "agent_start"
}

func (c AgentEnd) llmChunkType() string {
	return "agent_end"
}
func (c ReasoningItemAdd) llmChunkType() string {
	return "reasoning_item_add"
}

func (c ReasoningItemDone) llmChunkType() string {
	return "reasoning_item_done"
}

func (c ReasoningContentAdd) llmChunkType() string {
	return "reasoning_content_add"
}

func (c ReasoningContentDelta) llmChunkType() string {
	return "reasoning_content_delta"
}

func (c ReasoningContentDone) llmChunkType() string {
	return "reasoning_content_done"
}

func (c OutputItemAdd) llmChunkType() string {
	return "output_item_add"
}
func (c OutputItemDone) llmChunkType() string {
	return "output_item_done"
}

func (c OutputContentAdd) llmChunkType() string {
	return "output_content_add"
}
func (c OutputContentDelta) llmChunkType() string {
	return "output_content_delta"
}
func (c OutputContentDone) llmChunkType() string {
	return "output_content_done"
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

func (c UnknownChunk) llmChunkType() string {
	return "unknown_chunk"
}
