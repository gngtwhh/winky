package agent

import (
	"time"

	"winky/internal/llm"
)

// Event is a single notification emitted by an Agent during a Turn.
type Event interface{ agentEventType() string }

// TurnStart marks the beginning of a turn.
type TurnStart struct {
	Timestamp time.Time
}

// ReasoningDelta carries a piece of the model's reasoning text.
type ReasoningDelta struct {
	Text string
}

// OutputDelta carries a piece of the model's output text.
type OutputDelta struct {
	Text string
}

// TurnEnd marks a successfully completed turn and carries the full reasoning
// and output text, plus this turn's usage and the session total. A zero
// Usage.TotalTokens means the provider reported no usage.
type TurnEnd struct {
	Reasoning  string
	Output     string
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

func (TurnStart) agentEventType() string      { return "turn_start" }
func (ReasoningDelta) agentEventType() string { return "reasoning_delta" }
func (OutputDelta) agentEventType() string    { return "output_delta" }
func (TurnEnd) agentEventType() string        { return "turn_end" }
func (TurnFailed) agentEventType() string     { return "turn_failed" }
