package transcript

import "fmt"

// MissingToolResult stands in for a tool result that never reached the
// transcript, for example because the process died mid-turn.
const MissingToolResult = "error: no result was recorded for this tool call"

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
func Repair(events []Event) (healed []Event, problems []Problem) {
	open := make(map[string]string) // call_id -> tool name
	order := make([]string, 0, 4)

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

		case TurnStart, StepStart, StepEnd, TurnEnd, TurnFailed:
			// Same boundaries the projection consumes: nothing may arrive for
			// the group after them.
			closeGroup(ev.Seq(), false)
		}
	}
	closeGroup(lastSeq, true)
	return healed, problems
}
