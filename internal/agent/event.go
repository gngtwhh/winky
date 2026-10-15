package agent

import (
	"time"
	"winky/internal/llm"
)

type Event interface{ agentEventType() string }

type TurnStart struct{ timestamp time.Time }

type AssistantDelta struct{ Text string }
type AssistantMessage struct{ Message llm.Message }
type ReasoningDelta struct{ Text string }
type ToolCallRequested struct{ Call llm.ToolCall }
type TurnEnd struct {
	Message llm.Message
	Usage   llm.TokenUsage
}
type TurnFailed struct{ Err error }

func (TurnStart) agentEventType() string         { return "turn_start" }
func (AssistantDelta) agentEventType() string    { return "assistant_delta" }
func (AssistantMessage) agentEventType() string  { return "assistant_message" }
func (ReasoningDelta) agentEventType() string    { return "reasoning_delta" }
func (ToolCallRequested) agentEventType() string { return "tool_call_requested" }
func (TurnEnd) agentEventType() string           { return "turn_end" }
func (TurnFailed) agentEventType() string        { return "turn_failed" }
