package transcript

import (
	"strings"
	"testing"
)

func hasProblem(problems []Problem, substr string) bool {
	for _, p := range problems {
		if strings.Contains(p.Message, substr) {
			return true
		}
	}
	return false
}

// A process killed mid-turn leaves an open turn; Repair closes it.
func TestRepairClosesOpenTurn(t *testing.T) {
	events := []Event{
		WithSeq(TurnStart{}, 1),
		WithSeq(UserMessage{Content: "hi"}, 2),
		WithSeq(StepStart{}, 3),
		WithSeq(AssistantOutputMessage{Content: "partial"}, 4),
	}

	healed, problems := Repair(events)
	if len(healed) != 1 {
		t.Fatalf("healed = %+v, want one TurnAborted", healed)
	}
	aborted, ok := healed[0].(TurnAborted)
	if !ok {
		t.Fatalf("healed[0] = %T, want TurnAborted", healed[0])
	}
	if aborted.Reason == "" {
		t.Fatal("TurnAborted has no reason")
	}
	if !hasProblem(problems, "aborted marker") {
		t.Fatalf("problems = %+v, want the appended marker reported", problems)
	}

	// Repair must be idempotent: the second pass sees the terminal event.
	events = append(events, WithSeq(aborted, 5))
	healed, _ = Repair(events)
	if len(healed) != 0 {
		t.Fatalf("second repair appended %+v, want nothing", healed)
	}
}

// Two user messages in one open turn are the original bug shape.
func TestRepairDetectsAdjacentUserMessages(t *testing.T) {
	events := []Event{
		WithSeq(TurnStart{}, 1),
		WithSeq(UserMessage{Content: "first"}, 2),
		WithSeq(UserMessage{Content: "second"}, 3),
	}

	healed, problems := Repair(events)
	if len(healed) != 1 {
		t.Fatalf("healed = %+v, want one TurnAborted", healed)
	}
	if !hasProblem(problems, "adjacent") {
		t.Fatalf("problems = %+v, want adjacent user messages reported", problems)
	}
}

// A killed tool group still gets its missing results.
func TestRepairFillsMissingToolResults(t *testing.T) {
	events := []Event{
		WithSeq(TurnStart{}, 1),
		WithSeq(UserMessage{Content: "run"}, 2),
		WithSeq(AssistantOutputMessage{
			ToolCalls: []ToolCall{{CallID: "c1", Name: "shell"}},
		}, 3),
	}

	healed, _ := Repair(events)
	var results, abort bool
	for _, ev := range healed {
		switch ev.(type) {
		case ToolResult:
			results = true
		case TurnAborted:
			abort = true
		}
	}
	if !results {
		t.Fatalf("healed = %+v, want a missing tool result", healed)
	}
	if !abort {
		t.Fatalf("healed = %+v, want the open turn closed", healed)
	}
}
