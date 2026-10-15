package agent

import "time"

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

// TurnEnd marks a successfully completed turn and carries the full
// reasoning and output text accumulated during it.
type TurnEnd struct {
	Reasoning string
	Output    string
}

// TurnFailed marks a turn that ended with an error.
type TurnFailed struct {
	Err error
}

func (TurnStart) agentEventType() string      { return "turn_start" }
func (ReasoningDelta) agentEventType() string { return "reasoning_delta" }
func (OutputDelta) agentEventType() string    { return "output_delta" }
func (TurnEnd) agentEventType() string        { return "turn_end" }
func (TurnFailed) agentEventType() string     { return "turn_failed" }
