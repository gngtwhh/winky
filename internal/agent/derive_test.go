package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"winky/internal/llm"
	"winky/internal/schema"
	"winky/internal/transcript"
)

// retryingStreamer fails its first attempt with a retryable error after
// reporting usage, then answers.
type retryingStreamer struct{ calls int }

func (r *retryingStreamer) Stream(_ context.Context, _ llm.Request) (<-chan llm.Chunk, error) {
	r.calls++
	ch := make(chan llm.Chunk, 2)
	ch <- llm.ResponseStart{}
	if r.calls == 1 {
		ch <- llm.ResponseEnd{
			StopReason: llm.StopReasonIncomplete,
			Err:        &llm.RetryableError{Err: errors.New("503")},
			Response:   &llm.Response{Usage: usage(900, 100, 1000, 0)},
		}
		close(ch)
		return ch, nil
	}
	ch <- llm.ResponseEnd{
		StopReason: llm.StopReasonDone,
		Response: &llm.Response{
			Output: []llm.OutputItem{messageItem("m", "ok")},
			Usage:  usage(50, 10, 60, 0),
		},
	}
	close(ch)
	return ch, nil
}

func (r *retryingStreamer) Name() string { return "retrying" }

// A discarded retry attempt is not billed. Its usage never reaches the
// transcript, so counting it live would make -r report a different total than
// the session reported before it exited.
func TestRetriedUsageIsNotCounted(t *testing.T) {
	ag := New(&retryingStreamer{}, WithTranscriptRecorder(&memRecorder{}))

	if _, err := ag.Turn(context.Background(), "hi"); err != nil {
		t.Fatalf("Turn: %v", err)
	}

	var end schema.TurnEnd
	for _, ev := range drainTurn(t, ag) {
		if te, ok := ev.(schema.TurnEnd); ok {
			end = te
		}
	}
	if end.Usage.TotalTokens != 60 {
		t.Fatalf("turn usage = %+v, want only the successful attempt", end.Usage)
	}
	if end.TotalUsage.TotalTokens != 60 {
		t.Fatalf("turn total = %+v, want only the successful attempt", end.TotalUsage)
	}
	if got := mustStats(t, ag).total.TotalTokens; got != 60 {
		t.Fatalf("stats total = %d, want 60", got)
	}
}

// The anchor describes the request as it was sent, so what came after it counts
// as growth. Anchoring on a later view would make the estimate equal the
// measurement and then never grow.
func TestAnchorDescribesTheRequestNotTheReply(t *testing.T) {
	ag := New(&scriptedStreamer{}, WithTranscriptRecorder(&memRecorder{}))

	events := []transcript.Event{
		transcript.TurnStart{},
		transcript.UserMessage{Content: strings.Repeat("x", 4000)},
		transcript.StepStart{},
		transcript.AssistantOutputMessage{Content: strings.Repeat("y", 400)},
		transcript.StepEnd{Usage: &transcript.Usage{PromptTokens: 1000, TotalTokens: 1100}},
		transcript.TurnEnd{},
	}
	for _, ev := range events {
		if err := ag.record(ev); err != nil {
			t.Fatalf("record %s: %v", ev.Type(), err)
		}
	}

	v, s, err := ag.derive()
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if est := ag.estimateContext(v, s); est <= 1000 {
		t.Fatalf("estimate = %d, want more than the measured 1000", est)
	}
}

// Cost and context size are functions of the log: what a running session
// reports equals what a fresh agent reports after replaying the same events.
func TestDerivedAccountingSurvivesResume(t *testing.T) {
	rec := &memRecorder{}
	ag := New(&scriptedStreamer{}, WithTranscriptRecorder(rec), WithMaxOutputTokens(100))

	recordTurn(t, ag, 4000)
	recordTurnMeasured(t, ag, 4000, &transcript.Usage{PromptTokens: 2600, TotalTokens: 2800})
	liveTotal := mustStats(t, ag).total
	liveEstimate := mustEstimate(t, ag)

	resumed := New(&scriptedStreamer{}, WithTranscriptRecorder(rec), WithMaxOutputTokens(100))
	if err := resumed.loadLog(); err != nil {
		t.Fatalf("loadLog: %v", err)
	}
	if got := mustStats(t, resumed).total; got != liveTotal {
		t.Fatalf("resumed total = %+v, live = %+v", got, liveTotal)
	}
	if got := mustEstimate(t, resumed); got != liveEstimate {
		t.Fatalf("resumed estimate = %d, live = %d", got, liveEstimate)
	}
}

// A compaction carries the size of what replaced the pruned turns, so the
// anchor moves with it and the estimate actually shrinks.
func TestCompactionShrinksTheEstimate(t *testing.T) {
	rec := &memRecorder{}
	ag := New(
		&scriptedStreamer{resps: []*llm.Response{
			{Output: []llm.OutputItem{messageItem("s1", "handover")}},
		}},
		WithModel("test-model"),
		WithMaxOutputTokens(100),
		WithContextWindow(4000),
		WithCompaction(CompactionOptions{Enabled: true}),
		WithTranscriptRecorder(rec),
	)

	recordTurn(t, ag, 4000)
	recordTurnMeasured(t, ag, 4000, &transcript.Usage{PromptTokens: 2600, TotalTokens: 2800})
	before := mustEstimate(t, ag)

	if err := ag.maybeCompact(context.Background(), false); err != nil {
		t.Fatalf("maybeCompact: %v", err)
	}
	if after := mustEstimate(t, ag); after >= before {
		t.Fatalf("estimate after compaction = %d, want below %d", after, before)
	}

	// The compacted view fits again, so a second pass has nothing to do.
	if err := ag.maybeCompact(context.Background(), false); err != nil {
		t.Fatalf("second maybeCompact: %v", err)
	}
	var compactions int
	for _, ev := range rec.events {
		if _, ok := ev.(transcript.CompactionSummary); ok {
			compactions++
		}
	}
	if compactions != 1 {
		t.Fatalf("compaction events = %d, want 1", compactions)
	}
}
