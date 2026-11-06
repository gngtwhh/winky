package transcript

import (
	"fmt"
	"time"
)

// MissingToolResult stands in for a tool result that never reached the
// transcript, for example because the process died mid-turn.
const MissingToolResult = "error: no result was recorded for this tool call"

// AbortedToolResult stands in for a tool call the user cancelled before it
// finished, distinct from a result that was lost.
const AbortedToolResult = "error: aborted by the user before this tool finished"

// Problem reports one semantic hole found while repairing a transcript.
type Problem struct {
	Seq     int64
	Message string
}

// Repair answers the tool calls of a turn that never got a result. A killed
// process leaves a group the request side has to drop, together with the
// results that did arrive, so the missing calls are returned as events the
// recorder can append. A hole closed by a later boundary cannot be appended in
// place any more and is only reported.
//
// Repair also closes a turn left open by a crash: a transcript ending after a
// user message without a terminal event gets a TurnAborted marker.
func Repair(events []Event) (healed []Event, problems []Problem) {
	open := make(map[string]string) // call_id -> tool name
	order := make([]string, 0, 4)

	var (
		openTurn bool
		hasUser  bool
	)

	closeGroup := func(seq int64, appendable bool) {
		for _, id := range order {
			name, ok := open[id]
			if !ok {
				continue
			}
			if appendable {
				healed = append(healed, ToolResult{CallID: id, Output: MissingToolResult})
				problems = append(problems, Problem{Seq: seq, Message: fmt.Sprintf(
					"appended a missing tool result for call %s (%s)", id, name,
				)})
				continue
			}
			problems = append(problems, Problem{Seq: seq, Message: fmt.Sprintf(
				"missing tool result for call %s (%s): the group closed before it was recorded", id, name,
			)})
		}
		open = make(map[string]string)
		order = order[:0]
	}

	var lastSeq int64
	for _, ev := range events {
		lastSeq = ev.Seq()
		switch e := ev.(type) {
		case UserMessage:
			if openTurn && hasUser {
				problems = append(problems, Problem{Seq: ev.Seq(), Message: fmt.Sprintf(
					"user message %q is adjacent to another user message; the request history closes the earlier turn",
					e.Content,
				)})
			}
			openTurn = true
			hasUser = true

		case AssistantOutputMessage:
			for _, tc := range e.ToolCalls {
				if _, ok := open[tc.CallID]; !ok {
					order = append(order, tc.CallID)
				}
				open[tc.CallID] = tc.Name
			}

		case ToolResult:
			if _, ok := open[e.CallID]; !ok {
				problems = append(problems, Problem{Seq: ev.Seq(), Message: fmt.Sprintf(
					"orphan tool result for call %s: no open call in the current group", e.CallID,
				)})
				continue
			}
			delete(open, e.CallID)

		case TurnStart:
			// Same boundaries the projection consumes: nothing may arrive for
			// the group after them.
			closeGroup(ev.Seq(), false)
			openTurn = true
			hasUser = false

		case StepStart, StepEnd:
			// The tool group cannot span a step boundary.
			closeGroup(ev.Seq(), false)

		case TurnEnd, TurnFailed, TurnAborted:
			closeGroup(ev.Seq(), false)
			openTurn = false
			hasUser = false
		}
	}
	closeGroup(lastSeq, true)

	if openTurn && hasUser {
		healed = append(healed, TurnAborted{
			Timestamp: time.Now(),
			Reason:    "recovered after an interrupted turn",
		})
		problems = append(problems, Problem{Seq: lastSeq, Message: "appended an aborted marker for the open turn"})
	}
	return healed, problems
}
