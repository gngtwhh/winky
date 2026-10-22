package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
)

// tickInterval coalesces streamed deltas into a bounded number of re-renders.
const tickInterval = 80 * time.Millisecond

// startPump launches the single goroutine that owns agent.Events(). The agent
// writes to that channel with a blocking send, so the pump must start before the
// program runs. done closes once the pump returns, letting Run wait for it.
func startPump(ctx context.Context, ag Agent, out chan<- AgentEvent, done chan<- struct{}) {
	go func() {
		defer close(done)

		if ag == nil {
			// Running without an agent is allowed (sends are rejected with a
			// notice), so the pump must simply stop instead of panicking.
			return
		}

		src := ag.Events()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-src:
				if !ok {
					return
				}
				select {
				case out <- AgentEvent{Event: ev}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
}

// waitEvent pulls exactly one agent event. Update must re-arm it after every
// AgentEvent; that re-arming is the invariant that keeps agent.emit unblocked.
func waitEvent(ch <-chan AgentEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return tea.Quit()
		}
		return ev
	}
}

// doTurn runs one blocking agent turn off the update loop.
func doTurn(ctx context.Context, ag Agent, id int, input string) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		res, err := ag.Turn(ctx, input)
		return turnResult{
			TurnID:   id,
			Result:   res,
			Err:      err,
			Duration: time.Since(start),
		}
	}
}

// doCompact runs one manual compaction off the update loop.
func doCompact(ctx context.Context, ag Agent) tea.Cmd {
	return func() tea.Msg {
		changed, err := ag.Compact(ctx)
		return compactResult{Changed: changed, Err: err}
	}
}

// tickCmd drives the deferred rebuild while streaming.
func tickCmd(id int) tea.Cmd {
	return tea.Tick(tickInterval, func(time.Time) tea.Msg {
		return tickMsg{ID: id}
	})
}
