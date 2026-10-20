package agent

import (
	"context"
	"testing"
	"time"

	"winky/internal/llm"
	"winky/internal/tool"
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

// messageItem builds a message output item carrying the given text.
func messageItem(id, text string) llm.OutputItem {
	return llm.OutputItem{
		Type: llm.ItemMessage,
		ID:   id,
		Content: []llm.ContentPart{
			{Type: llm.PartOutputText, Text: text},
		},
	}
}

// drainTurn reads events until the turn settles. It keeps the tests independent
// of how many non-terminal events a turn emits.
func drainTurn(t *testing.T, ag *Agent) []Event {
	t.Helper()
	var events []Event
	timeout := time.After(time.Second)
	for {
		select {
		case ev := <-ag.Events():
			events = append(events, ev)
			switch ev.(type) {
			case TurnEnd, TurnFailed:
				return events
			}
		case <-timeout:
			t.Fatal("timed out waiting for the turn to settle")
		}
	}
}

// A successful Turn emits TurnStart, one delta per chunk, ContextUsage when
// the provider reports prompt tokens, then TurnEnd.
func TestTurnEndCarriesUsage(t *testing.T) {
	ag := New(&fakeStreamer{chunks: []llm.Chunk{
		llm.ResponseStart{},
		llm.OutputDelta{ItemID: "msg_1", Text: "hi"},
		llm.ResponseEnd{
			StopReason: llm.StopReasonDone,
			Response: &llm.Response{
				Output: []llm.OutputItem{messageItem("msg_1", "hi")},
				Usage:  usage(31, 31, 62, 0),
			},
		},
	}})

	res, err := ag.Turn(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if res.Output() != "hi" {
		t.Fatalf("output = %q, want %q", res.Output(), "hi")
	}
	if len(res.Blocks) != 1 || res.Blocks[0].Kind != BlockMessage {
		t.Fatalf("blocks = %+v, want one message block", res.Blocks)
	}

	var end TurnEnd
	for _, ev := range drainTurn(t, ag) {
		if te, ok := ev.(TurnEnd); ok {
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
		llm.ResponseStart{},
		llm.OutputDelta{Text: "partial"},
		llm.ResponseEnd{
			StopReason: llm.StopReasonIncomplete,
			Response: &llm.Response{
				Usage: usage(20, 10, 30, 0),
			},
		},
	}})

	if _, err := ag.Turn(context.Background(), "hi"); err == nil {
		t.Fatal("expected an error for an incomplete stream")
	}

	var failed TurnFailed
	for _, ev := range drainTurn(t, ag) {
		if tf, ok := ev.(TurnFailed); ok {
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

// scriptedStreamer returns the i-th response on the i-th call.
type scriptedStreamer struct {
	resps []*llm.Response
	calls int
}

func (s *scriptedStreamer) Stream(_ context.Context, _ llm.Request) (<-chan llm.Chunk, error) {
	i := s.calls
	s.calls++
	ch := make(chan llm.Chunk, 2)
	ch <- llm.ResponseStart{}
	ch <- llm.ResponseEnd{StopReason: llm.StopReasonDone, Response: s.resps[i]}
	close(ch)
	return ch, nil
}

func (s *scriptedStreamer) Name() string { return "scripted" }

type fakeTools struct{ res map[string]tool.Result }

func (f *fakeTools) Specs() []llm.ToolSpec                          { return nil }
func (f *fakeTools) Run(_ context.Context, c tool.Call) tool.Result { return f.res[c.Name] }
func (f *fakeTools) Sequential(string) bool                         { return false }

func TestTurnRunsToolsAndLoops(t *testing.T) {
	ag := New(&scriptedStreamer{resps: []*llm.Response{
		{Output: []llm.OutputItem{{Type: llm.ItemFunctionCall, CallID: "c1", Name: "echo", Arguments: `{}`}}},
		{Output: []llm.OutputItem{messageItem("m2", "done")}},
	}}, WithTools(&fakeTools{res: map[string]tool.Result{"echo": {Output: "ok"}}}))

	turn, err := ag.Turn(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if len(turn.ToolResults) != 1 || turn.ToolResults[0].Output != "ok" {
		t.Fatalf("tool results = %+v", turn.ToolResults)
	}
	if turn.Output() != "done" {
		t.Fatalf("output = %q, want done", turn.Output())
	}

	h := ag.History()
	if len(h) != 4 {
		t.Fatalf("history length = %d: %+v", len(h), h)
	}
	if h[1].Role != llm.RoleAssistant || len(h[1].ToolCalls) != 1 {
		t.Fatalf("history[1] = %+v", h[1])
	}
	if h[2].Role != llm.RoleToolOutput || h[2].ToolCallID != "c1" {
		t.Fatalf("history[2] = %+v", h[2])
	}
	if h[3].Role != llm.RoleAssistant || h[3].Content != "done" {
		t.Fatalf("history[3] = %+v", h[3])
	}
}
