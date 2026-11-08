package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"winky/internal/schema"

	"winky/internal/llm"
	"winky/internal/tool"
	"winky/internal/transcript"
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
func drainTurn(t *testing.T, ag *Agent) []schema.Event {
	t.Helper()
	var events []schema.Event
	timeout := time.After(time.Second)
	for {
		select {
		case ev := <-ag.Events():
			events = append(events, ev)
			switch ev.(type) {
			case schema.TurnEnd, schema.TurnFailed, schema.TurnAborted:
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
	ag := New(
		&fakeStreamer{chunks: []llm.Chunk{
			llm.ResponseStart{},
			llm.OutputDelta{ItemID: "msg_1", Text: "hi"},
			llm.ResponseEnd{
				StopReason: llm.StopReasonDone,
				Response: &llm.Response{
					Output: []llm.OutputItem{messageItem("msg_1", "hi")},
					Usage:  usage(31, 31, 62, 0),
				},
			},
		}},
	)

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

	var end schema.TurnEnd
	for _, ev := range drainTurn(t, ag) {
		if te, ok := ev.(schema.TurnEnd); ok {
			end = te
		}
	}
	if end.Usage.TotalTokens != 62 {
		t.Fatalf("TurnEnd usage = %+v, want total 62", end.Usage)
	}
	if end.TotalUsage.TotalTokens != 62 {
		t.Fatalf("TurnEnd totalUsage = %+v, want total 62", end.TotalUsage)
	}
	if got := mustStats(t, ag).total.TotalTokens; got != 62 {
		t.Fatalf("agent total usage = %d, want 62", got)
	}
}

// A failed turn still records the usage the provider reported before failing.
func TestTurnFailedCarriesUsage(t *testing.T) {
	ag := New(
		&fakeStreamer{chunks: []llm.Chunk{
			llm.ResponseStart{},
			llm.OutputDelta{Text: "partial"},
			llm.ResponseEnd{
				StopReason: llm.StopReasonIncomplete,
				Response: &llm.Response{
					Usage: usage(20, 10, 30, 0),
				},
			},
		}},
	)

	if _, err := ag.Turn(context.Background(), "hi"); err == nil {
		t.Fatal("expected an error for an incomplete stream")
	}

	var failed schema.TurnFailed
	for _, ev := range drainTurn(t, ag) {
		if tf, ok := ev.(schema.TurnFailed); ok {
			failed = tf
		}
	}
	if failed.Usage.TotalTokens != 30 {
		t.Fatalf("TurnFailed usage = %+v, want total 30", failed.Usage)
	}
	if got := mustStats(t, ag).total.TotalTokens; got != 30 {
		t.Fatalf("agent total usage = %d, want 30", got)
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
func (f *fakeTools) Deadline(tool.Call) time.Duration               { return 0 }
func (f *fakeTools) Allow(context.Context, tool.Call) error         { return nil }

// gatedStreamer emits a partial output, then reports cancellation like the
// provider adapter.
type gatedStreamer struct {
	started chan struct{}
	release chan struct{}
}

func (g *gatedStreamer) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Chunk, error) {
	ch := make(chan llm.Chunk, 4)
	go func() {
		defer close(ch)
		ch <- llm.ResponseStart{}
		ch <- llm.OutputDelta{ItemID: "m", Text: "partial"}
		close(g.started)
		<-g.release
		<-ctx.Done()
		ch <- llm.ResponseEnd{StopReason: llm.StopReasonCanceled, Err: ctx.Err()}
	}()
	return ch, nil
}

func (g *gatedStreamer) Name() string { return "gated" }

// A cancel during streaming keeps the partial output and closes the turn.
func TestTurnAbortDuringStreamPersistsPartialOutput(t *testing.T) {
	g := &gatedStreamer{started: make(chan struct{}), release: make(chan struct{})}
	ag := New(g)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var (
		turn Turn
		err  error
	)
	go func() {
		turn, err = ag.Turn(ctx, "hi")
		close(done)
	}()

	// Cancel once the delta has reached the view, not before.
	for ev := range ag.Events() {
		if _, ok := ev.(schema.OutputDelta); ok {
			break
		}
	}
	cancel()
	close(g.release)
	<-done

	if !errors.Is(err, ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
	if turn.Output() != "partial" {
		t.Fatalf("turn output = %q, want partial", turn.Output())
	}

	var aborted bool
	for _, ev := range drainTurn(t, ag) {
		if _, ok := ev.(schema.TurnAborted); ok {
			aborted = true
		}
	}
	if !aborted {
		t.Fatal("no TurnAborted event")
	}

	msgs := mustView(t, ag).messages()
	if len(msgs) != 2 || msgs[0].Role != llm.RoleUser || msgs[1].Role != llm.RoleAssistant {
		t.Fatalf("view = %+v, want user + assistant", msgs)
	}
	if !strings.Contains(msgs[1].Content, "partial") || !strings.Contains(msgs[1].Content, "aborted") {
		t.Fatalf("assistant = %q, want partial output and abort marker", msgs[1].Content)
	}
}

// blockingTools waits for cancellation and reports an aborted result.
type blockingTools struct {
	started    chan struct{}
	once       sync.Once
	sequential bool
}

func (b *blockingTools) Specs() []llm.ToolSpec            { return nil }
func (b *blockingTools) Sequential(string) bool           { return b.sequential }
func (b *blockingTools) Deadline(tool.Call) time.Duration { return 0 }
func (b *blockingTools) Allow(context.Context, tool.Call) error {
	return nil
}
func (b *blockingTools) Run(ctx context.Context, c tool.Call) tool.Result {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return tool.Result{Output: "killed", IsError: true, Aborted: true}
}

// A cancel while tools run pairs every call: running ones become aborted and
// sequential calls that never started are filled in too.
func TestTurnAbortDuringToolsPairsEveryCall(t *testing.T) {
	tools := &blockingTools{started: make(chan struct{}), sequential: true}
	ag := New(
		&scriptedStreamer{resps: []*llm.Response{{
			Output: []llm.OutputItem{
				{Type: llm.ItemFunctionCall, CallID: "c1", Name: "slow", Arguments: `{}`},
				{Type: llm.ItemFunctionCall, CallID: "c2", Name: "slow", Arguments: `{}`},
			},
		}}},
		WithTools(tools),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var (
		turn Turn
		err  error
	)
	go func() {
		turn, err = ag.Turn(ctx, "hi")
		close(done)
	}()

	<-tools.started
	cancel()
	<-done

	if !errors.Is(err, ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
	if len(turn.ToolResults) != 2 {
		t.Fatalf("tool results = %+v, want 2", turn.ToolResults)
	}
	for _, r := range turn.ToolResults {
		if !r.Aborted || r.Output != transcript.AbortedToolResult {
			t.Fatalf("result = %+v, want an aborted result", r)
		}
	}

	drainTurn(t, ag)

	msgs := mustView(t, ag).messages()
	if len(msgs) != 5 {
		t.Fatalf("view = %+v, want user, assistant, two results, assistant", msgs)
	}
	if msgs[2].Role != llm.RoleToolOutput || msgs[3].Role != llm.RoleToolOutput {
		t.Fatalf("tool results missing from view: %+v", msgs)
	}
	if msgs[4].Role != llm.RoleAssistant || !strings.Contains(msgs[4].Content, "aborted") {
		t.Fatalf("closing message = %+v, want abort marker", msgs[4])
	}
}

func TestTurnRunsToolsAndLoops(t *testing.T) {
	ag := New(
		&scriptedStreamer{resps: []*llm.Response{
			{Output: []llm.OutputItem{{Type: llm.ItemFunctionCall, CallID: "c1", Name: "echo", Arguments: `{}`}}},
			{Output: []llm.OutputItem{messageItem("m2", "done")}},
		}}, WithTools(&fakeTools{res: map[string]tool.Result{"echo": {Output: "ok"}}}),
	)

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

	msgs := mustView(t, ag).messages()
	if len(msgs) != 4 {
		t.Fatalf("view length = %d: %+v", len(msgs), msgs)
	}
	if msgs[1].Role != llm.RoleAssistant || len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("view[1] = %+v", msgs[1])
	}
	if msgs[2].Role != llm.RoleToolOutput || msgs[2].ToolCallID != "c1" {
		t.Fatalf("view[2] = %+v", msgs[2])
	}
	if msgs[3].Role != llm.RoleAssistant || msgs[3].Content != "done" {
		t.Fatalf("view[3] = %+v", msgs[3])
	}
}

// Replaying the persisted events must reproduce the live view exactly.
func TestAbortReplayMatchesLive(t *testing.T) {
	g := &gatedStreamer{started: make(chan struct{}), release: make(chan struct{})}
	rec := &memRecorder{}
	ag := New(g, WithTranscriptRecorder(rec))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		_, _ = ag.Turn(ctx, "hi")
		close(done)
	}()

	for ev := range ag.Events() {
		if _, ok := ev.(schema.OutputDelta); ok {
			break
		}
	}
	cancel()
	close(g.release)
	<-done

	live := mustView(t, ag).messages()

	got := mustProject(t, rec.events).messages()
	if !reflect.DeepEqual(got, live) {
		t.Fatalf("replayed view differs from live:\nlive: %+v\nreplay: %+v", live, got)
	}
}

// A resumed agent must project the same view as the one that wrote the log:
// reading the transcript and running live are the same fold over the same
// events.
func TestResumedViewMatchesLive(t *testing.T) {
	rec := &memRecorder{}
	writer := New(
		&scriptedStreamer{resps: []*llm.Response{
			{Output: []llm.OutputItem{{Type: llm.ItemFunctionCall, CallID: "c1", Name: "echo", Arguments: `{}`}}},
			{Output: []llm.OutputItem{messageItem("m2", "done")}},
		}},
		WithTools(&fakeTools{res: map[string]tool.Result{"echo": {Output: "ok"}}}),
		WithTranscriptRecorder(rec),
	)
	if _, err := writer.Turn(context.Background(), "hi"); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	live := mustView(t, writer).messages()

	resumed := New(&scriptedStreamer{}, WithTranscriptRecorder(rec))
	if err := resumed.loadLog(); err != nil {
		t.Fatalf("loadLog: %v", err)
	}
	if got := mustView(t, resumed).messages(); !reflect.DeepEqual(got, live) {
		t.Fatalf("resumed view differs from live:\nlive: %+v\nresumed: %+v", live, got)
	}
}
