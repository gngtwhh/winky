package tui

import (
	"time"

	"winky/internal/agent"
)

// AgentEvent wraps one agent.Event so Update can switch on it as a tea.Msg.
// The events channel of an Agent is read by exactly one pump goroutine, which
// forwards every event here.
type AgentEvent struct {
	Event agent.Event
}

// turnResult is what the tea.Cmd running agent.Turn resolves to. TurnID is used
// to discard the result of a turn that has already been superseded or
// cancelled.
type turnResult struct {
	TurnID   int
	Result   agent.TurnResult
	Err      error
	Duration time.Duration
}

// tickMsg requests a coalesced re-render of streamed deltas.
type tickMsg struct {
	ID int
}
