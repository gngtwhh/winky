package agent

import (
	"context"
	"fmt"
	"time"

	"winky/internal/llm"
	"winky/internal/schema"
	"winky/internal/tool"
	"winky/internal/transcript"
)

// Toolset is the tool surface an Agent exposes to the model. *tool.Registry
// satisfies it; tests can inject a scripted one.
type Toolset interface {
	Specs() []llm.ToolSpec
	Run(ctx context.Context, call tool.Call) tool.Result
	Sequential(name string) bool
	// Deadline reports how long this call may run; zero defers to the agent's
	// own ceiling.
	Deadline(call tool.Call) time.Duration
	// Allow decides whether the call may run, asking the user when the policy
	// requires it. It is called before the call starts, so the permission wait
	// is not billed to the call's budget or its elapsed time.
	Allow(ctx context.Context, call tool.Call) error
}

// Tool-call budgets. These are a safety net, not a policy: a tool declares its
// own deadline (shell reads one from its arguments), and the numbers here only
// make sure the turn eventually becomes interactive again.
const (
	// defaultCallRuntime applies when a tool declares no budget at all.
	defaultCallRuntime = 10 * time.Minute
	// maxCallRuntime caps a declared budget, so one call cannot hold the session
	// for hours.
	maxCallRuntime = 60 * time.Minute
	// callGracePeriod is how long a cancelled call may take to hand back the
	// output it already produced before the agent stops waiting for it.
	callGracePeriod = 2 * time.Second
	// repeatLimit is how many identical consecutive calls a turn may make before
	// the next one is refused.
	repeatLimit = 3
)

// callGuard breaks a model loop that repeats one identical call. It counts only
// consecutive repeats, so "edit, test, edit, test" is untouched.
type callGuard struct {
	key    string
	streak int
}

// exhausted reports whether this call repeats the previous one too often, and
// records it either way.
func (g *callGuard) exhausted(c tool.Call) bool {
	key := c.Name + "\x00" + c.Arguments
	if key != g.key {
		g.key, g.streak = key, 1
		return false
	}
	g.streak++
	return g.streak >= repeatLimit
}

// runTools executes the calls of one step and always returns one result per
// call, because a missing result breaks the tool-call group in the transcript.
//
// Calls run concurrently unless any tool requires sequential execution, in which
// case all run in issue order. Each call gets its own cancellation and its own
// budget, so one slow tool cannot cut its siblings short.
//
// sink is called on the calling goroutine the moment each call settles, so a
// fast call is recorded and reported without waiting for a slow sibling.
func (a *Agent) runTools(ctx context.Context, calls []tool.Call, sink func(ToolResult)) []ToolResult {
	results := make([]ToolResult, len(calls))
	settle := func(i int, res ToolResult) {
		results[i] = res
		sink(res)
	}

	if a.tools == nil {
		for i, c := range calls {
			settle(i, ToolResult{
				CallID: c.ID, Name: c.Name,
				Output: "error: no tools are registered", IsError: true,
			})
		}
		return results
	}

	// Refusal and authorization happen here, in issue order, on this goroutine:
	// both are properties of the sequence of calls, and neither may be decided
	// from the goroutines that run them.
	dispatchable := func(i int, c tool.Call) bool {
		if a.calls.exhausted(c) {
			settle(i, repeatedCallResult(c))
			return false
		}
		if err := a.tools.Allow(ctx, c); err != nil {
			if ctx.Err() != nil {
				settle(i, abortedToolResult(c))
			} else {
				settle(i, deniedToolResult(c, err))
			}
			return false
		}
		return true
	}

	if a.sequential(calls) {
		for i, c := range calls {
			if ctx.Err() != nil {
				abortFrom(calls, settle, i)
				return results
			}
			if !dispatchable(i, c) {
				continue
			}
			settle(i, a.dispatch(ctx, c))
			if results[i].Aborted {
				abortFrom(calls, settle, i+1)
				return results
			}
		}
		return results
	}

	type settled struct {
		i   int
		res ToolResult
	}
	// Buffered for every call, so a worker never blocks on a leader that is busy
	// recording an earlier result.
	done := make(chan settled, len(calls))
	running := 0
	for i, c := range calls {
		if ctx.Err() != nil {
			abortFrom(calls, settle, i)
			break
		}
		if !dispatchable(i, c) {
			continue
		}
		running++
		go func(i int, c tool.Call) { done <- settled{i, a.dispatch(ctx, c)} }(i, c)
	}
	for n := 0; n < running; n++ {
		s := <-done
		settle(s.i, s.res)
	}
	return results
}

// sequential reports whether any call in the step must run alone.
func (a *Agent) sequential(calls []tool.Call) bool {
	for _, c := range calls {
		if a.tools.Sequential(c.Name) {
			return true
		}
	}
	return false
}

// abortFrom settles every call from index i as aborted.
func abortFrom(calls []tool.Call, settle func(int, ToolResult), i int) {
	for ; i < len(calls); i++ {
		settle(i, abortedToolResult(calls[i]))
	}
}

// dispatch resolves the budget, tells the UI the call is starting, and runs it.
// The budget is attached to the result too, so a settled call can show how close
// it came to the limit.
func (a *Agent) dispatch(ctx context.Context, c tool.Call) ToolResult {
	budget := a.callBudget(c)
	a.emit(schema.ToolCallBegin{CallID: c.ID, Name: c.Name, Budget: budget})
	res := a.supervise(ctx, c, budget)
	res.Budget = budget
	return res
}

// repeatedCallResult refuses a call that would repeat the previous one again,
// instead of spending another budget on it.
func repeatedCallResult(c tool.Call) ToolResult {
	return ToolResult{
		CallID: c.ID, Name: c.Name, IsError: true,
		Output: fmt.Sprintf(
			"error: this exact call has already run %d times in this turn with nothing else in "+
				"between, so it was not run again. Change the arguments, or say what you expected "+
				"to learn from it.", repeatLimit-1,
		),
	}
}

// deniedToolResult reports a call the policy refused, or one that was cancelled
// while the user was still deciding.
func deniedToolResult(c tool.Call, err error) ToolResult {
	return ToolResult{
		CallID: c.ID, Name: c.Name,
		Output: "error: " + err.Error(), IsError: true,
	}
}

// callBudget resolves how long one call may run: what the tool declared, bounded
// by the agent's ceiling.
func (a *Agent) callBudget(c tool.Call) time.Duration {
	budget := a.tools.Deadline(c)
	switch {
	case budget <= 0:
		return defaultCallRuntime
	case budget > maxCallRuntime:
		return maxCallRuntime
	default:
		return budget
	}
}

// supervise runs one call under its budget.
//
// When the budget is spent the call's context is cancelled, which is a request,
// not a command: a tool that is blocked in a syscall never sees it. So after a
// grace period the agent stops waiting and reports the call as abandoned. That
// goroutine is deliberately leaked — the alternative is a turn that never
// returns, which used to make even Esc useless.
func (a *Agent) supervise(ctx context.Context, c tool.Call, budget time.Duration) ToolResult {
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Buffered, and written only by the goroutine that owns it, so an abandoned
	// call can always finish sending and exit and no slot is written twice.
	done := make(chan ToolResult, 1)
	go func() { done <- a.runOne(callCtx, c) }()

	timer := time.NewTimer(budget)
	defer timer.Stop()

	select {
	case res := <-done:
		return res
	case <-ctx.Done():
		cancel()
		return abortedToolResult(c)
	case <-timer.C:
	}

	// Out of budget: cancel and let the tool hand back what it already produced.
	// Partial output is usually what the model needs to decide the next step.
	cancel()
	grace := time.NewTimer(callGracePeriod)
	defer grace.Stop()

	select {
	case res := <-done:
		return timedOutToolResult(c, res, budget)
	case <-ctx.Done():
		return abortedToolResult(c)
	case <-grace.C:
		return abandonedToolResult(c, budget)
	}
}

// abortedToolResult reports a call the user cancelled.
func abortedToolResult(c tool.Call) ToolResult {
	return ToolResult{
		CallID: c.ID, Name: c.Name,
		Output: transcript.AbortedToolResult, IsError: true, Aborted: true,
	}
}

// timedOutToolResult keeps whatever the tool managed to produce and says why it
// stopped. It is an error but not an abort: the turn continues.
func timedOutToolResult(c tool.Call, res ToolResult, budget time.Duration) ToolResult {
	notice := fmt.Sprintf(
		"[timed out after %s and was cancelled; anything it did before that still took effect]",
		budget,
	)
	out := res.Output
	if out == "" {
		out = notice
	} else {
		out += "\n" + notice
	}
	return ToolResult{
		CallID: c.ID, Name: c.Name, Output: out,
		IsError: true, Duration: res.Duration,
	}
}

// abandonedToolResult reports a call that did not stop when cancelled. Its
// output is lost and its work may still be running.
func abandonedToolResult(c tool.Call, budget time.Duration) ToolResult {
	waited := budget + callGracePeriod
	return ToolResult{
		CallID: c.ID, Name: c.Name, IsError: true, Duration: waited,
		Output: fmt.Sprintf(
			"[gave up after %s: the call did not stop when cancelled, so its output was lost and "+
				"it may still be running. Do not assume it had no effect.]", waited,
		),
	}
}

func (a *Agent) runOne(ctx context.Context, c tool.Call) ToolResult {
	start := time.Now()
	res := a.tools.Run(ctx, c)
	out := ToolResult{
		CallID: c.ID, Name: c.Name, Output: res.Output,
		IsError: res.IsError, Duration: time.Since(start),
	}
	if res.Aborted {
		out.Output = transcript.AbortedToolResult
		out.IsError = true
		out.Aborted = true
	}
	return out
}
