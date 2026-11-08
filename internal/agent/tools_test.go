package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"winky/internal/llm"
	"winky/internal/tool"
	"winky/internal/transcript"
)

// fnToolset is a tool set whose behaviour, deadline, and concurrency mode are
// supplied by the test.
type fnToolset struct {
	sequential bool
	deadline   time.Duration
	run        func(ctx context.Context, c tool.Call) tool.Result
}

func (f *fnToolset) Specs() []llm.ToolSpec  { return nil }
func (f *fnToolset) Sequential(string) bool { return f.sequential }
func (f *fnToolset) Deadline(tool.Call) time.Duration {
	return f.deadline
}

func (f *fnToolset) Run(ctx context.Context, c tool.Call) tool.Result { return f.run(ctx, c) }

func call(id, name, args string) tool.Call {
	return tool.Call{ID: id, Name: name, Arguments: args}
}

// A tool that honours cancellation gets its output kept: the partial result is
// what lets the model decide the next step.
func TestToolTimeoutKeepsPartialOutput(t *testing.T) {
	ts := &fnToolset{
		deadline: 20 * time.Millisecond,
		run: func(ctx context.Context, _ tool.Call) tool.Result {
			<-ctx.Done()
			return tool.Result{Output: "partial output", IsError: true}
		},
	}
	ag := New(&scriptedStreamer{}, WithTools(ts))

	res := ag.runTools(context.Background(), []tool.Call{call("c1", "slow", "{}")})
	if len(res) != 1 {
		t.Fatalf("results = %d, want 1", len(res))
	}
	if res[0].Aborted {
		t.Fatal("a spent budget is not a user abort")
	}
	if !res[0].IsError {
		t.Fatalf("result = %+v, want an error", res[0])
	}
	if !strings.Contains(res[0].Output, "partial output") {
		t.Fatalf("output = %q, want the partial output kept", res[0].Output)
	}
	if !strings.Contains(res[0].Output, "timed out") {
		t.Fatalf("output = %q, want a timeout notice", res[0].Output)
	}
}

// A tool blocked where cancellation cannot reach it must not be able to hang the
// turn: the agent stops waiting and says the output is lost.
func TestToolThatIgnoresCancellationIsAbandoned(t *testing.T) {
	blocked := make(chan struct{})
	defer close(blocked)

	ts := &fnToolset{
		deadline: 20 * time.Millisecond,
		run: func(context.Context, tool.Call) tool.Result {
			<-blocked // ignores the context entirely
			return tool.Result{Output: "never seen"}
		},
	}
	ag := New(&scriptedStreamer{}, WithTools(ts))

	done := make(chan []ToolResult, 1)
	go func() {
		done <- ag.runTools(context.Background(), []tool.Call{call("c1", "stuck", "{}")})
	}()

	select {
	case res := <-done:
		if res[0].Aborted {
			t.Fatal("a spent budget is not a user abort")
		}
		if !strings.Contains(res[0].Output, "gave up") {
			t.Fatalf("output = %q, want an abandonment notice", res[0].Output)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("runTools did not return for a tool that ignores cancellation")
	}
}

// One call running out of budget must not cancel its siblings.
func TestToolTimeoutLeavesSiblingsAlone(t *testing.T) {
	blocked := make(chan struct{})
	defer close(blocked)

	ts := &fnToolset{
		deadline: 50 * time.Millisecond,
		run: func(ctx context.Context, c tool.Call) tool.Result {
			if c.Name == "stuck" {
				<-blocked
				return tool.Result{Output: "never seen"}
			}
			return tool.Result{Output: "fast output"}
		},
	}
	ag := New(&scriptedStreamer{}, WithTools(ts))

	res := ag.runTools(context.Background(), []tool.Call{
		call("c1", "stuck", "{}"),
		call("c2", "fast", "{}"),
	})
	if res[1].Output != "fast output" || res[1].IsError {
		t.Fatalf("sibling result = %+v, want its own output", res[1])
	}
	if !res[0].IsError {
		t.Fatalf("stuck result = %+v, want an error", res[0])
	}
}

// A cancelled turn still wins over a budget: the user asked to stop.
func TestUserCancelBeatsBudget(t *testing.T) {
	ts := &fnToolset{
		deadline: time.Hour,
		run: func(ctx context.Context, _ tool.Call) tool.Result {
			<-ctx.Done()
			return tool.Result{Output: "partial", IsError: true}
		},
	}
	ag := New(&scriptedStreamer{}, WithTools(ts))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	res := ag.runTools(ctx, []tool.Call{call("c1", "slow", "{}")})
	if !res[0].Aborted {
		t.Fatalf("result = %+v, want an aborted result", res[0])
	}
	if res[0].Output != transcript.AbortedToolResult {
		t.Fatalf("output = %q, want the abort marker", res[0].Output)
	}
}

// Repeating one identical call is a loop, not progress: the third attempt is
// refused instead of run.
func TestRepeatedCallIsRefused(t *testing.T) {
	runs := 0
	ts := &fnToolset{run: func(context.Context, tool.Call) tool.Result {
		runs++
		return tool.Result{Output: "ok"}
	}}
	ag := New(&scriptedStreamer{}, WithTools(ts))

	var last []ToolResult
	for i := 0; i < 3; i++ {
		last = ag.runTools(context.Background(), []tool.Call{
			call(fmt.Sprintf("c%d", i), "echo", `{"text":"hi"}`),
		})
	}
	if runs != 2 {
		t.Fatalf("runs = %d, want the third call refused", runs)
	}
	if !last[0].IsError || !strings.Contains(last[0].Output, "already run 2 times") {
		t.Fatalf("third result = %+v, want a refusal", last[0])
	}
}

// Alternating calls are not a loop: this is the "edit, test, edit, test" shape.
func TestAlternatingCallsAreNotRefused(t *testing.T) {
	runs := 0
	ts := &fnToolset{run: func(context.Context, tool.Call) tool.Result {
		runs++
		return tool.Result{Output: "ok"}
	}}
	ag := New(&scriptedStreamer{}, WithTools(ts))

	for i := 0; i < 5; i++ {
		ag.runTools(context.Background(), []tool.Call{
			call(fmt.Sprintf("c%d", i), "echo", fmt.Sprintf(`{"n":%d}`, i%2)),
		})
	}
	if runs != 5 {
		t.Fatalf("runs = %d, want all 5 executed", runs)
	}
}
