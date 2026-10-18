package llm

import "time"

// Chunk is one semantic event emitted while streaming a single model response.
// It is deliberately coarse: the full protocol result arrives once on
// ResponseEnd rather than as item/part lifecycle events.
type Chunk interface {
	llmChunkType() string
}

// Stop reasons for a response.
const (
	StopReasonDone       = "done"
	StopReasonError      = "error"
	StopReasonFailed     = "failed"
	StopReasonIncomplete = "incomplete"
	StopReasonUnknown    = "unknown"
)

// ResponseStart marks the beginning of one model response.
type ResponseStart struct{ Timestamp time.Time }

// ResponseEnd marks the end of one model response. Response carries the fully
// accumulated result; it is nil when the response errored before producing
// output.
type ResponseEnd struct {
	Timestamp  time.Time
	StopReason string
	Err        error
	Response   *Response
}

// Text deltas carry ItemID so the live view can group fragments into the same
// item structure as Response.Output.

// ReasoningDelta is a fragment of the model's reasoning text (reasoning_text).
type ReasoningDelta struct {
	ItemID string
	Text   string
}

// ReasoningSummaryDelta is a fragment of the concise reasoning summary
// (summary_text).
type ReasoningSummaryDelta struct {
	ItemID string
	Text   string
}

// OutputDelta is a fragment of the assistant's answer text (output_text).
type OutputDelta struct {
	ItemID string
	Text   string
}

// RefusalDelta is a fragment of a refusal (refusal).
type RefusalDelta struct {
	ItemID string
	Text   string
}

// ToolCallStart announces a function tool call. CallID is the call_id used to
// round-trip the result as a function_call_output item. It is distinct from the
// item_id carried by text deltas: call_id is what the provider expects back.
type ToolCallStart struct {
	CallID string
	Name   string
}

// ToolCallArgsDelta carries a partial argument fragment for a tool call. CallID
// matches ToolCallStart.CallID.
type ToolCallArgsDelta struct {
	CallID string
	Delta  string
}

// ToolCallEnd carries the authoritative complete arguments for a tool call.
type ToolCallEnd struct {
	CallID    string
	Arguments string
}

// UnknownChunk carries a raw event the adapter did not map. Debug only.
type UnknownChunk struct{ Raw string }

func (ResponseStart) llmChunkType() string         { return "response_start" }
func (ResponseEnd) llmChunkType() string           { return "response_end" }
func (ReasoningDelta) llmChunkType() string        { return "reasoning_delta" }
func (ReasoningSummaryDelta) llmChunkType() string { return "reasoning_summary_delta" }
func (OutputDelta) llmChunkType() string           { return "output_delta" }
func (RefusalDelta) llmChunkType() string          { return "refusal_delta" }
func (ToolCallStart) llmChunkType() string         { return "tool_call_start" }
func (ToolCallArgsDelta) llmChunkType() string     { return "tool_call_args_delta" }
func (ToolCallEnd) llmChunkType() string           { return "tool_call_end" }
func (UnknownChunk) llmChunkType() string          { return "unknown_chunk" }
