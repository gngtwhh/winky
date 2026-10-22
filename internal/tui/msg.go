package tui

import (
	"time"
	"winky/internal/schema"

	"winky/internal/tool"
)

// AgentEvent wraps one schema.Event as a tea.Msg. Exactly one pump goroutine
// forwards every event here.
type AgentEvent struct {
	Event schema.Event
}

// turnResult is what the tea.Cmd running agent.Turn resolves to. TurnID discards
// the result of a superseded or cancelled turn.
type turnResult struct {
	TurnID   int
	Result   schema.Turn
	Err      error
	Duration time.Duration
}

// compactResult is what the tea.Cmd running a manual compaction resolves to.
// The visible result arrives as ContextCompacted; this only carries the outcome.
type compactResult struct {
	Changed bool
	Err     error
}

// tickMsg requests a coalesced re-render of streamed deltas.
type tickMsg struct {
	ID int
}

// permRequest asks the user to authorize one tool operation; the decision is
// delivered on reply.
type permRequest struct {
	req   tool.Request
	reply chan bool
}
