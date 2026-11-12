package agent

import (
	"strings"
	"testing"

	"winky/internal/transcript"
)

// systemPromptText is 4000 ASCII bytes, which estimateTokens counts as 1000 tokens.
var systemPromptText = strings.Repeat("x", 4000)

type memRecorder struct {
	events []transcript.Event
}

func (r *memRecorder) Append(ev transcript.Event) (transcript.Event, error) {
	ev = transcript.WithSeq(ev, int64(len(r.events)+1))
	r.events = append(r.events, ev)
	return ev, nil
}

func (r *memRecorder) ReadAll() ([]transcript.Event, error) { return r.events, nil }

func (r *memRecorder) Subscribe() <-chan transcript.Event { return nil }

// TestEstimateTracksSystemPromptGrowth is the regression test for the bias
// calculation: the anchor's provider count was measured with the old system
// prompt, so growth added afterwards must not cancel out.
func TestEstimateTracksSystemPromptGrowth(t *testing.T) {
	a := &Agent{log: []transcript.Event{
		at(1, transcript.TurnStart{}),
		at(2, transcript.UserMessage{Content: "u"}),
		at(3, transcript.StepStart{}),
		at(4, transcript.StepEnd{Usage: &transcript.Usage{PromptTokens: 12000}}),
		at(5, transcript.PromptChanged{Slot: "base", Name: "base", Text: systemPromptText}),
	}}

	v, s, err := a.derive()
	if err != nil {
		t.Fatalf("derive() = %v", err)
	}
	if got, want := a.estimateContextTokens(v, s), 13000; got != want {
		t.Fatalf("estimateContextTokens() = %d, want %d", got, want)
	}
}

func TestEstimateCountsSystemPromptOnce(t *testing.T) {
	a := &Agent{log: []transcript.Event{
		at(1, transcript.PromptChanged{Slot: "base", Name: "base", Text: systemPromptText}),
		at(2, transcript.UserMessage{Content: "u"}),
		at(3, transcript.StepStart{}),
		at(4, transcript.StepEnd{Usage: &transcript.Usage{PromptTokens: 12000}}),
	}}

	v, s, err := a.derive()
	if err != nil {
		t.Fatalf("derive() = %v", err)
	}
	if got, want := a.estimateContextTokens(v, s), 12000; got != want {
		t.Fatalf("estimateContextTokens() = %d, want %d", got, want)
	}
}

func TestSeedInitialPromptWritesOnce(t *testing.T) {
	rec := &memRecorder{}
	a := New(nil, WithInitialSystemPrompt("first"), WithTranscriptRecorder(rec))

	if err := a.loadLog(); err != nil {
		t.Fatalf("loadLog() = %v", err)
	}
	if err := a.loadLog(); err != nil {
		t.Fatalf("second loadLog() = %v", err)
	}

	if n := len(rec.events); n != 1 {
		t.Fatalf("recorded %d events, want 1", n)
	}
	if _, ok := rec.events[0].(transcript.PromptChanged); !ok {
		t.Fatalf("recorded %T, want PromptChanged", rec.events[0])
	}

	v, err := a.project()
	if err != nil {
		t.Fatalf("project() = %v", err)
	}
	if got, want := v.systemPrompt(), "first"; got != want {
		t.Fatalf("systemPrompt() = %q, want %q", got, want)
	}
}

func TestSeedInitialPromptSkippedWhenRecorded(t *testing.T) {
	rec := &memRecorder{}
	if _, err := rec.Append(transcript.PromptChanged{
		Slot: "base", Name: "base", Text: "recorded",
	}); err != nil {
		t.Fatalf("Append() = %v", err)
	}

	a := New(nil, WithInitialSystemPrompt("ignored"), WithTranscriptRecorder(rec))
	if err := a.loadLog(); err != nil {
		t.Fatalf("loadLog() = %v", err)
	}

	if n := len(rec.events); n != 1 {
		t.Fatalf("recorded %d events, want the transcript left alone", n)
	}
	v, err := a.project()
	if err != nil {
		t.Fatalf("project() = %v", err)
	}
	if got, want := v.systemPrompt(), "recorded"; got != want {
		t.Fatalf("systemPrompt() = %q, want %q", got, want)
	}
}

func TestSeedInitialPromptWithoutPrompt(t *testing.T) {
	rec := &memRecorder{}
	a := New(nil, WithTranscriptRecorder(rec))

	if err := a.loadLog(); err != nil {
		t.Fatalf("loadLog() = %v", err)
	}
	if n := len(rec.events); n != 0 {
		t.Fatalf("recorded %d events, want none", n)
	}
}
