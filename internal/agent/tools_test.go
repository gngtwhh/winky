package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"winky/internal/llm"
	"winky/internal/schema"
	"winky/internal/tool"
	"winky/internal/transcript"
)

// fnToolset is a tool set whose behaviour, deadline, and concurrency mode are
// supplied by the test.
type fnToolset struct {
	sequential bool
	deadline   time.Duration
	deny       error // returned by Allow when set
	run        func(ctx context.Context, c tool.Call) tool.Result
}

func (f *fnToolset) Allow(context.Context, tool.Call) error { return f.deny }

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

	res := ag.runTools(context.Background(), []tool.Call{call("c1", "slow", "{}")}, func(ToolResult) {})
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
		done <- ag.runTools(context.Background(), []tool.Call{call("c1", "stuck", "{}")}, func(ToolResult) {})
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
	}, func(ToolResult) {})
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

	res := ag.runTools(ctx, []tool.Call{call("c1", "slow", "{}")}, func(ToolResult) {})
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
		}, func(ToolResult) {})
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
		}, func(ToolResult) {})
	}
	if runs != 5 {
		t.Fatalf("runs = %d, want all 5 executed", runs)
	}
}

// The resolved budget travels with the result, so the UI can show the limit that
// was actually applied rather than the one the tool asked for.
func TestToolResultCarriesItsBudget(t *testing.T) {
	ts := &fnToolset{
		deadline: 30 * time.Second,
		run:      func(context.Context, tool.Call) tool.Result { return tool.Result{Output: "ok"} },
	}
	ag := New(&scriptedStreamer{}, WithTools(ts))

	res := ag.runTools(context.Background(), []tool.Call{call("c1", "echo", "{}")}, func(ToolResult) {})
	if res[0].Budget != 30*time.Second {
		t.Fatalf("budget = %v, want the declared 30s", res[0].Budget)
	}

	// A tool that declares nothing gets the agent's default ceiling.
	free := &fnToolset{run: func(context.Context, tool.Call) tool.Result { return tool.Result{} }}
	ag = New(&scriptedStreamer{}, WithTools(free))
	res = ag.runTools(context.Background(), []tool.Call{call("c2", "echo", "{}")}, func(ToolResult) {})
	if res[0].Budget != defaultCallRuntime {
		t.Fatalf("budget = %v, want the default %v", res[0].Budget, defaultCallRuntime)
	}
}

// drainEvents reads whatever the agent has already emitted, without waiting.
func drainEvents(ag *Agent) []schema.Event {
	var out []schema.Event
	for {
		select {
		case ev := <-ag.Events():
			out = append(out, ev)
		default:
			return out
		}
	}
}

// A dispatched call announces itself with the budget that is about to be
// enforced, so the UI can show the limit while the call is still running.
func TestToolCallBeginAnnouncesTheBudget(t *testing.T) {
	ts := &fnToolset{
		deadline: 30 * time.Second,
		run:      func(context.Context, tool.Call) tool.Result { return tool.Result{Output: "ok"} },
	}
	ag := New(&scriptedStreamer{}, WithTools(ts))

	ag.runTools(context.Background(), []tool.Call{call("c1", "echo", "{}")}, func(ToolResult) {})

	events := drainEvents(ag)
	if len(events) != 1 {
		t.Fatalf("events = %d, want one begin: %+v", len(events), events)
	}
	begin, ok := events[0].(schema.ToolCallBegin)
	if !ok {
		t.Fatalf("event = %T, want schema.ToolCallBegin", events[0])
	}
	if begin.CallID != "c1" || begin.Name != "echo" || begin.Budget != 30*time.Second {
		t.Fatalf("begin = %+v, want c1/echo/30s", begin)
	}
}

// A refused call never runs, so it must not announce a start.
func TestRefusedCallDoesNotAnnounce(t *testing.T) {
	ts := &fnToolset{run: func(context.Context, tool.Call) tool.Result {
		return tool.Result{Output: "ok"}
	}}
	ag := New(&scriptedStreamer{}, WithTools(ts))

	for i := 0; i < 3; i++ {
		ag.runTools(context.Background(), []tool.Call{call("c1", "echo", "{}")}, func(ToolResult) {})
	}

	var begins int
	for _, ev := range drainEvents(ag) {
		if _, ok := ev.(schema.ToolCallBegin); ok {
			begins++
		}
	}
	if begins != 2 {
		t.Fatalf("begin events = %d, want 2 (the third call is refused)", begins)
	}
}

// A fast call is recorded and reported the moment it settles instead of waiting
// for a slow sibling, which is what makes its own elapsed time honest.
func TestResultsAreReportedAsTheySettle(t *testing.T) {
	release := make(chan struct{})
	settled := make(chan string, 2)

	ts := &fnToolset{run: func(_ context.Context, c tool.Call) tool.Result {
		if c.Name == "stuck" {
			<-release
			return tool.Result{Output: "late"}
		}
		return tool.Result{Output: "early"}
	}}
	ag := New(&scriptedStreamer{}, WithTools(ts))

	done := make(chan struct{})
	go func() {
		ag.runTools(context.Background(), []tool.Call{
			call("c1", "stuck", "{}"),
			call("c2", "fast", "{}"),
		}, func(r ToolResult) { settled <- r.Name })
		close(done)
	}()

	select {
	case name := <-settled:
		if name != "fast" {
			t.Fatalf("first settled call = %q, want fast", name)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no result was reported while a sibling was still running")
	}

	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runTools did not finish")
	}
}

// A call the policy refuses never starts: it reports the refusal and announces
// no start, so no stopwatch runs for work that never happened.
func TestDeniedCallNeverStarts(t *testing.T) {
	ran := false
	ts := &fnToolset{
		deny: errors.New("permission denied: shell needs full access"),
		run: func(context.Context, tool.Call) tool.Result {
			ran = true
			return tool.Result{Output: "should not run"}
		},
	}
	ag := New(&scriptedStreamer{}, WithTools(ts))

	res := ag.runTools(
		context.Background(),
		[]tool.Call{call("c1", "echo", "{}")},
		func(ToolResult) {},
	)
	if ran {
		t.Fatal("a denied call was executed")
	}
	if !res[0].IsError || !strings.Contains(res[0].Output, "permission denied") {
		t.Fatalf("result = %+v, want the refusal", res[0])
	}
	for _, ev := range drainEvents(ag) {
		if _, ok := ev.(schema.ToolCallBegin); ok {
			t.Fatal("a denied call announced a start")
		}
	}
}

// The permission prompt is answered before the call starts, so the wait is not
// billed to the call: nothing is announced and no stopwatch runs until the user
// says yes.
func TestPermissionPromptComesBeforeTheCallStarts(t *testing.T) {
	asked := make(chan struct{}, 1)
	answer := make(chan struct{})

	policy := tool.NewPolicy(tool.WorkspaceWrite)
	policy.SetPrompter(func(ctx context.Context, _ tool.Request) (bool, error) {
		asked <- struct{}{}
		select {
		case <-answer:
			return true, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	})
	reg, err := tool.NewDefault(t.TempDir(), policy)
	if err != nil {
		t.Fatalf("NewDefault: %v", err)
	}
	ag := New(&scriptedStreamer{}, WithTools(reg))

	settled := make(chan ToolResult, 1)
	go func() {
		ag.runTools(
			context.Background(),
			[]tool.Call{call("c1", "shell", `{"command":"echo hi"}`)},
			func(r ToolResult) { settled <- r },
		)
	}()

	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the prompter was never asked")
	}
	if evs := drainEvents(ag); len(evs) != 0 {
		t.Fatalf("events = %d before approval, want none: %+v", len(evs), evs)
	}

	close(answer)

	select {
	case res := <-settled:
		if res.IsError {
			t.Fatalf("result = %+v, want the command to run", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the call did not settle after approval")
	}
	var begins int
	for _, ev := range drainEvents(ag) {
		if _, ok := ev.(schema.ToolCallBegin); ok {
			begins++
		}
	}
	if begins != 1 {
		t.Fatalf("begin events = %d after approval, want 1", begins)
	}
}
