package schema

import (
	"time"

	"winky/internal/llm"
)

// Event is a single notification emitted by an Agent during a Turn.
type Event interface{ agentEventType() string }

// TurnStart marks the beginning of a user turn, before the first request.
type TurnStart struct {
	Timestamp time.Time
}

// ReasoningDelta carries a piece of reasoning text. ItemID locates the block
// the delta belongs to.
type ReasoningDelta struct {
	ItemID string
	Text   string
}

// ReasoningSummaryDelta carries a piece of the concise reasoning summary.
type ReasoningSummaryDelta struct {
	ItemID string
	Text   string
}

// OutputDelta carries a piece of assistant message text.
type OutputDelta struct {
	ItemID string
	Text   string
}

// ToolCallStart announces a tool call. CallID locates the call.
type ToolCallStart struct {
	CallID string
	Name   string
}

// ToolCallArgsDelta carries a partial argument fragment.
type ToolCallArgsDelta struct {
	CallID string
	Delta  string
}

// ToolCallEnd carries the complete arguments for a tool call.
type ToolCallEnd struct {
	CallID    string
	Arguments string
}

// ContextUsage reports the input-token count of the most recent model request:
// how much of the model's context window that request occupied. It is emitted
// once per model call, including a call that then fails.
type ContextUsage struct {
	Tokens int
}

// ContextCompacted reports that old turns were replaced by a summary. Before and
// After are estimated prompt sizes; Kind names the trigger.
type ContextCompacted struct {
	Before int
	After  int
	Kind   string
}

// TurnEnd marks a successfully completed turn and carries the assembled Turn,
// plus this turn's usage and the session total. A zero Usage.TotalTokens means
// the provider reported no usage.
type TurnEnd struct {
	Turn       Turn
	Usage      llm.TokenUsage
	TotalUsage llm.TokenUsage
}

// TurnFailed marks a turn that ended with an error. It may still carry usage
// when the provider reported it before failing.
type TurnFailed struct {
	Err        error
	Usage      llm.TokenUsage
	TotalUsage llm.TokenUsage
}

func (TurnStart) agentEventType() string        { return "turn_start" }
func (ContextUsage) agentEventType() string     { return "context_usage" }
func (ContextCompacted) agentEventType() string { return "context_compacted" }
func (ReasoningDelta) agentEventType() string   { return "reasoning_delta" }
func (ReasoningSummaryDelta) agentEventType() string {
	return "reasoning_summary_delta"
}
func (OutputDelta) agentEventType() string   { return "output_delta" }
func (ToolCallStart) agentEventType() string { return "tool_call_start" }
func (ToolCallArgsDelta) agentEventType() string {
	return "tool_call_args_delta"
}
func (ToolCallEnd) agentEventType() string { return "tool_call_end" }
func (ToolResult) agentEventType() string  { return "tool_result" }
func (TurnEnd) agentEventType() string     { return "turn_end" }
func (TurnFailed) agentEventType() string  { return "turn_failed" }
