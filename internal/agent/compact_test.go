package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"winky/internal/llm"
	"winky/internal/transcript"
)

// memRecorder sequences events like the JSONL recorder, without a file.
type memRecorder struct {
	events []transcript.Event
}

func (r *memRecorder) Append(ev transcript.Event) (transcript.Event, error) {
	sequenced := transcript.WithSeq(ev, int64(len(r.events)+1))
	r.events = append(r.events, sequenced)
	return sequenced, nil
}

func (r *memRecorder) ReadAll() ([]transcript.Event, error) {
	return append([]transcript.Event(nil), r.events...), nil
}

func (r *memRecorder) Subscribe() <-chan transcript.Event { return nil }

// recordTurn appends one completed turn with a large user message.
func recordTurn(t *testing.T, ag *Agent, size int) {
	t.Helper()
	events := []transcript.Event{
		transcript.TurnStart{},
		transcript.UserMessage{Content: strings.Repeat("x", size)},
		transcript.StepStart{},
		transcript.AssistantOutputMessage{Content: "ok"},
		transcript.StepEnd{},
		transcript.TurnEnd{},
	}
	for _, ev := range events {
		if err := ag.record(ev); err != nil {
			t.Fatalf("record %s: %v", ev.Type(), err)
		}
	}
}

// A resumed session must replay to exactly what the live projection holds.
func TestCompactionReplayMatchesLive(t *testing.T) {
	rec := &memRecorder{}
	ag := New(
		&scriptedStreamer{resps: []*llm.Response{
			{Output: []llm.OutputItem{messageItem("s1", "handover text")}},
		}},
		WithModel("test-model"),
		WithMaxOutputTokens(100),
		WithContextWindow(4000),
		WithCompaction(CompactionOptions{Enabled: true}),
		WithTranscriptRecorder(rec),
	)

	recordTurn(t, ag, 4000)
	recordTurn(t, ag, 4000)

	// Pretend the provider measured the last request: the estimate is anchored
	// on that number, so this is what pushes the context over the limit.
	ag.lastPromptEstimate = ag.estimateRequest(mustView(t, ag).Messages())
	ag.lastPromptTokens = 2600

	if err := ag.maybeCompact(context.Background(), false); err != nil {
		t.Fatalf("maybeCompact: %v", err)
	}

	live := mustView(t, ag).Messages()
	if len(live) != 3 {
		t.Fatalf("messages = %d, want summary + last turn: %+v", len(live), live)
	}
	if live[0].Role != llm.RoleSystem || live[0].Content != "handover text" {
		t.Fatalf("messages[0] = %+v, want the system summary", live[0])
	}

	last := rec.events[len(rec.events)-1]
	cs, ok := last.(transcript.CompactionSummary)
	if !ok {
		t.Fatalf("last event = %T, want CompactionSummary", last)
	}
	if cs.Kind != "auto_turn" || cs.ReplacedStartSeq != 2 || cs.ReplacedEndSeq <= cs.ReplacedStartSeq {
		t.Fatalf("compaction event = %+v", cs)
	}

	if got := projectEvents(t, rec.events).Messages(); !reflect.DeepEqual(got, live) {
		t.Fatalf("replayed messages = %+v, want %+v", got, live)
	}
}

// A range written by something other than planCompaction may split a tool-call
// group. It is applied as written; the request stays valid because the leftover
// result becomes an orphan Messages drops.
func TestCompactionSplitsToolGroupKeepsRequestValid(t *testing.T) {
	events := []transcript.Event{
		transcript.WithSeq(transcript.UserMessage{Content: "read it"}, 1),
		transcript.WithSeq(transcript.AssistantOutputMessage{
			ToolCalls: []transcript.ToolCall{{CallID: "c1", Name: "read_file"}},
		}, 2),
		transcript.WithSeq(transcript.ToolResult{CallID: "c1", Output: "contents"}, 3),
		transcript.WithSeq(transcript.CompactionSummary{
			Summary:          "s",
			ReplacedStartSeq: 1,
			ReplacedEndSeq:   2,
		}, 4),
	}

	h := projectEvents(t, events)

	msgs := h.Messages()
	if len(msgs) != 1 || msgs[0].Role != llm.RoleSystem || msgs[0].Content != "s" {
		t.Fatalf("messages = %+v, want only the summary", msgs)
	}
}

// The estimate grows with the history.
func TestEstimateContextGrowsWithHistory(t *testing.T) {
	rec := &memRecorder{}
	ag := New(&scriptedStreamer{}, WithTranscriptRecorder(rec), WithMaxOutputTokens(100))

	empty := mustEstimate(t, ag)
	recordTurn(t, ag, 4000)
	if got := mustEstimate(t, ag); got <= empty {
		t.Fatalf("estimate did not grow: %d then %d", empty, got)
	}
}

// mustEstimate reports the current context estimate for assertions.
func mustEstimate(t *testing.T, a *Agent) int {
	t.Helper()
	n, err := a.estimateContext()
	if err != nil {
		t.Fatalf("estimateContext: %v", err)
	}
	return n
}

// Manual compaction runs below the automatic thresholds, and reports a no-op
// once everything already fits.
func TestManualCompaction(t *testing.T) {
	rec := &memRecorder{}
	ag := New(
		&scriptedStreamer{resps: []*llm.Response{
			{Output: []llm.OutputItem{messageItem("s1", "handover")}},
		}},
		WithModel("test-model"),
		WithMaxOutputTokens(100),
		WithContextWindow(1_000_000), // far above the automatic threshold
		WithCompaction(CompactionOptions{Enabled: true}),
		WithTranscriptRecorder(rec),
	)

	recordTurn(t, ag, 4000)
	recordTurn(t, ag, 4000)

	changed, err := ag.Compact(context.Background())
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if !changed {
		t.Fatal("Compact reported nothing to replace")
	}
	msgs := mustView(t, ag).Messages()
	if len(msgs) == 0 || msgs[0].Role != llm.RoleSystem {
		t.Fatalf("messages = %+v, want a leading summary", msgs)
	}
	last := rec.events[len(rec.events)-1]
	if cs, ok := last.(transcript.CompactionSummary); !ok || cs.Kind != "manual" {
		t.Fatalf("last event = %#v, want a manual compaction", last)
	}

	changed, err = ag.Compact(context.Background())
	if err != nil {
		t.Fatalf("second Compact: %v", err)
	}
	if changed {
		t.Fatal("second Compact should have nothing to replace")
	}
}

// A single turn larger than the keep budget falls back to a step boundary.
func TestCompactionCutsAtStepBoundary(t *testing.T) {
	rec := &memRecorder{}
	ag := New(
		&scriptedStreamer{resps: []*llm.Response{
			{Output: []llm.OutputItem{messageItem("s1", "handover")}},
		}},
		WithModel("test-model"),
		WithMaxOutputTokens(100),
		WithContextWindow(3000),
		WithCompaction(CompactionOptions{Enabled: true}),
		WithTranscriptRecorder(rec),
	)

	events := []transcript.Event{
		transcript.TurnStart{},
		transcript.UserMessage{Content: strings.Repeat("x", 8000)},
		transcript.StepStart{},
		transcript.AssistantOutputMessage{
			ToolCalls: []transcript.ToolCall{{CallID: "c1", Name: "read_file"}},
		},
		transcript.ToolResult{CallID: "c1", Output: "ok"},
		transcript.StepEnd{},
		transcript.StepStart{},
		transcript.AssistantOutputMessage{Content: "done"},
		transcript.StepEnd{},
		transcript.TurnEnd{},
	}
	for _, ev := range events {
		if err := ag.record(ev); err != nil {
			t.Fatalf("record %s: %v", ev.Type(), err)
		}
	}

	ag.lastPromptEstimate = ag.estimateRequest(mustView(t, ag).Messages())
	ag.lastPromptTokens = 1800 // the hard limit is ~1725 for this window

	if err := ag.maybeCompact(context.Background(), true); err != nil {
		t.Fatalf("maybeCompact: %v", err)
	}

	msgs := mustView(t, ag).Messages()
	if len(msgs) != 4 {
		t.Fatalf("messages = %d, want summary plus the last two steps: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != llm.RoleSystem {
		t.Fatalf("messages[0] = %+v, want the summary", msgs[0])
	}
	last := rec.events[len(rec.events)-1]
	if cs, ok := last.(transcript.CompactionSummary); !ok || cs.Kind != "auto_step" {
		t.Fatalf("last event = %#v, want an auto_step compaction", last)
	}

	if got := projectEvents(t, rec.events).Messages(); !reflect.DeepEqual(got, msgs) {
		t.Fatalf("replayed messages = %+v, want %+v", got, msgs)
	}
}
