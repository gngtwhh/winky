package agent

import (
	"errors"
	"testing"

	"winky/internal/llm"
	"winky/internal/prompt"
	"winky/internal/transcript"
)

func at(seq int64, ev transcript.Event) transcript.Event {
	return transcript.WithSeq(ev, seq)
}

func TestProjectFoldsPromptChanges(t *testing.T) {
	events := []transcript.Event{
		at(1, transcript.PromptChanged{Slot: "base", Name: "base", Text: "first"}),
		at(2, transcript.UserMessage{Content: "hi"}),
		at(3, transcript.PromptChanged{Slot: "base", Name: "base", Text: "second"}),
	}

	v, err := project(events)
	if err != nil {
		t.Fatalf("project() = %v", err)
	}
	if got, want := v.systemPrompt(), "second"; got != want {
		t.Fatalf("systemPrompt() = %q, want %q", got, want)
	}
	if n := len(v.entries); n != 1 {
		t.Fatalf("len(entries) = %d, want 1: a prompt change must not add a message", n)
	}
}

func TestProjectRejectsUnknownPromptSlot(t *testing.T) {
	events := []transcript.Event{
		at(1, transcript.PromptChanged{Slot: "addendum", Name: "plugin", Text: "text"}),
	}
	if _, err := project(events); !errors.Is(err, prompt.ErrUnknownSlot) {
		t.Fatalf("project() = %v, want %v", err, prompt.ErrUnknownSlot)
	}
}

// TestCompactionKeepsSystemPrompt records the prompt change inside the range the
// compaction replaces: the range addresses request messages only.
func TestCompactionKeepsSystemPrompt(t *testing.T) {
	events := []transcript.Event{
		at(1, transcript.TurnStart{}),
		at(2, transcript.UserMessage{Content: "old"}),
		at(3, transcript.PromptChanged{Slot: "base", Name: "base", Text: "v2"}),
		at(4, transcript.AssistantOutputMessage{Content: "old answer"}),
		at(5, transcript.TurnEnd{}),
		at(6, transcript.UserMessage{Content: "new"}),
		at(7, transcript.AssistantOutputMessage{Content: "new answer"}),
		at(8, transcript.TurnEnd{}),
		at(9, transcript.CompactionSummary{
			Summary:          "old summarized",
			ReplacedStartSeq: 2,
			ReplacedEndSeq:   4,
		}),
	}

	v, err := project(events)
	if err != nil {
		t.Fatalf("project() = %v", err)
	}
	if got, want := v.systemPrompt(), "v2"; got != want {
		t.Fatalf("systemPrompt() = %q, want %q after compaction", got, want)
	}

	msgs := v.messages()
	if len(msgs) != 3 {
		t.Fatalf("len(messages) = %d, want 3", len(msgs))
	}
	if msgs[0].Role != llm.RoleSystem || msgs[0].Content != "old summarized" {
		t.Fatalf("messages[0] = %+v, want the compaction summary", msgs[0])
	}
	if msgs[1].Content != "new" || msgs[2].Content != "new answer" {
		t.Fatalf("kept messages = %+v, want the two after the replaced range", msgs[1:])
	}
}
