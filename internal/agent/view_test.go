package agent

import (
	"slices"
	"strings"
	"testing"

	"winky/internal/llm"
	"winky/internal/transcript"
)

// viewOf builds a view from unsequenced events, numbering them as a recorder
// would.
func viewOf(t *testing.T, events ...transcript.Event) *view {
	t.Helper()
	sequenced := make([]transcript.Event, len(events))
	for i, ev := range events {
		sequenced[i] = transcript.WithSeq(ev, int64(i+1))
	}
	return mustProject(t, sequenced)
}

// mustProject is the only way a view is produced, so the tests run the same
// fold the agent runs.
func mustProject(t *testing.T, events []transcript.Event) *view {
	t.Helper()
	v, err := project(events)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	return v
}

// mustStats folds an agent's log for assertions.
func mustStats(t *testing.T, a *Agent) stats {
	t.Helper()
	_, s, err := a.derive()
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	return s
}

// mustView projects an agent's log for assertions.
func mustView(t *testing.T, a *Agent) *view {
	t.Helper()
	v, err := a.project()
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	return v
}

func roles(msgs []llm.Message) []llm.Role {
	out := make([]llm.Role, len(msgs))
	for i, m := range msgs {
		out[i] = m.Role
	}
	return out
}

// The original bug: an unanswered user message was adjacent to the next one.
// The failure terminal now closes it.
func TestFailedTurnClosesDanglingUserMessage(t *testing.T) {
	v := viewOf(t,
		transcript.TurnStart{},
		transcript.UserMessage{Content: "这是第三条测试消息，请做一些耗时操作"},
		transcript.StepStart{},
		transcript.StepEnd{},
		transcript.TurnFailed{Error: "context canceled"},
		transcript.TurnStart{},
		transcript.UserMessage{Content: "今天几点了"},
	)

	msgs := v.messages()
	if got, want := roles(msgs), []llm.Role{
		llm.RoleUser, llm.RoleAssistant, llm.RoleUser,
	}; !slices.Equal(got, want) {
		t.Fatalf("roles = %v, want %v", got, want)
	}
	if !strings.Contains(msgs[1].Content, "failed") {
		t.Fatalf("closing message = %q, want a failure marker", msgs[1].Content)
	}
}

// A partial answer followed by an abort is one assistant message, not two.
func TestAbortedMarkerMergesIntoPartialOutput(t *testing.T) {
	v := viewOf(t,
		transcript.TurnStart{},
		transcript.UserMessage{Content: "go"},
		transcript.StepStart{},
		transcript.AssistantOutputMessage{Content: "working on it"},
		transcript.TurnAborted{Reason: "user"},
	)

	msgs := v.messages()
	if len(msgs) != 2 {
		t.Fatalf("messages = %+v, want user + one assistant", msgs)
	}
	if msgs[1].Role != llm.RoleAssistant {
		t.Fatalf("messages[1].Role = %q", msgs[1].Role)
	}
	if !strings.Contains(msgs[1].Content, "working on it") ||
		!strings.Contains(msgs[1].Content, "aborted") {
		t.Fatalf("content = %q, want partial output and abort marker", msgs[1].Content)
	}
}

// A tool call committed before an abort is still paired, and the turn closes.
func TestAbortedToolCallIsPaired(t *testing.T) {
	v := viewOf(t,
		transcript.TurnStart{},
		transcript.UserMessage{Content: "read it"},
		transcript.StepStart{},
		transcript.AssistantOutputMessage{
			ToolCalls: []transcript.ToolCall{{CallID: "c1", Name: "read_file"}},
		},
		transcript.ToolResult{CallID: "c1", Output: transcript.AbortedToolResult, Aborted: true},
		transcript.TurnAborted{Reason: "user"},
	)

	msgs := v.messages()
	if len(msgs) != 4 {
		t.Fatalf("messages = %+v, want user, assistant, tool, assistant", msgs)
	}
	if msgs[2].Role != llm.RoleToolOutput || msgs[2].ToolCallID != "c1" {
		t.Fatalf("messages[2] = %+v", msgs[2])
	}
	if msgs[3].Role != llm.RoleAssistant || !strings.Contains(msgs[3].Content, "aborted") {
		t.Fatalf("messages[3] = %+v, want the abort marker", msgs[3])
	}
}

// An interrupted turn is closed when the next user message arrives.
func TestInterruptedTurnIsClosedByNextUserMessage(t *testing.T) {
	v := viewOf(t,
		transcript.TurnStart{},
		transcript.UserMessage{Content: "first"},
		transcript.StepStart{},
		transcript.AssistantOutputMessage{Content: "partial"},
		transcript.StepStart{},
		transcript.UserMessage{Content: "second"},
	)

	msgs := v.messages()
	if len(msgs) != 3 {
		t.Fatalf("messages = %+v, want user, assistant, user", msgs)
	}
	if msgs[1].Role != llm.RoleAssistant || !strings.Contains(msgs[1].Content, "interrupted") {
		t.Fatalf("messages[1] = %+v, want an interruption marker", msgs[1])
	}
}

// The request view never emits two assistant messages in a row.
func TestMessagesMergeAdjacentAssistants(t *testing.T) {
	msgs := mergeAdjacentAssistants([]llm.Message{
		{Role: llm.RoleAssistant, Content: "one"},
		{Role: llm.RoleAssistant, Content: "two"},
	})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "one") ||
		!strings.Contains(msgs[0].Content, "two") {
		t.Fatalf("messages = %+v, want one merged assistant", msgs)
	}
}
