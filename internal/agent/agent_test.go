package agent

import (
	"context"
	"testing"

	"winky/internal/llm"
)

// fakeStreamer replays a fixed chunk sequence through a channel.
type fakeStreamer struct {
	chunks []llm.Chunk
}

func (f *fakeStreamer) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Chunk, error) {
	ch := make(chan llm.Chunk, len(f.chunks))
	go func() {
		defer close(ch)
		for _, c := range f.chunks {
			select {
			case ch <- c:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func (f *fakeStreamer) Name() string { return "fake" }

func usage(in, out, total, cached int) *llm.TokenUsage {
	return &llm.TokenUsage{
		PromptTokens:     in,
		CompletionTokens: out,
		TotalTokens:      total,
		CachedTokens:     cached,
	}
}

// Each successful Turn emits exactly TurnStart, one delta per chunk, and
// TurnEnd, so reading three events reaches the terminal event.
func TestTurnEndCarriesUsage(t *testing.T) {
	ag := New(&fakeStreamer{chunks: []llm.Chunk{
		llm.AgentStart{},
		llm.OutputContentDelta{Delta: "hi"},
		llm.AgentEnd{StopReason: llm.StopReasonDone, Usage: usage(31, 31, 62, 0)},
	}})

	res, err := ag.Turn(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if res.Output != "hi" {
		t.Fatalf("output = %q, want %q", res.Output, "hi")
	}

	var end TurnEnd
	for i := 0; i < 3; i++ {
		if te, ok := (<-ag.Events()).(TurnEnd); ok {
			end = te
		}
	}
	if end.Usage.TotalTokens != 62 {
		t.Fatalf("TurnEnd usage = %+v, want total 62", end.Usage)
	}
	if end.TotalUsage.TotalTokens != 62 {
		t.Fatalf("TurnEnd totalUsage = %+v, want total 62", end.TotalUsage)
	}
	if ag.totalUsage.TotalTokens != 62 {
		t.Fatalf("agent total usage = %+v, want 62", ag.totalUsage)
	}
}

// A failed turn still records the usage the provider reported before failing.
func TestTurnFailedCarriesUsage(t *testing.T) {
	ag := New(&fakeStreamer{chunks: []llm.Chunk{
		llm.AgentStart{},
		llm.OutputContentDelta{Delta: "partial"},
		llm.AgentEnd{StopReason: llm.StopReasonIncomplete, Usage: usage(20, 10, 30, 0)},
	}})

	if _, err := ag.Turn(context.Background(), "hi"); err == nil {
		t.Fatal("expected an error for an incomplete stream")
	}

	var failed TurnFailed
	for i := 0; i < 3; i++ {
		if tf, ok := (<-ag.Events()).(TurnFailed); ok {
			failed = tf
		}
	}
	if failed.Usage.TotalTokens != 30 {
		t.Fatalf("TurnFailed usage = %+v, want total 30", failed.Usage)
	}
	if ag.totalUsage.TotalTokens != 30 {
		t.Fatalf("agent total usage = %+v, want 30", ag.totalUsage)
	}
}
