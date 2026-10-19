// Package agent implements a multi-turn conversational agent loop: it owns the
// history and drives the llm.Streamer so consecutive turns share context.
package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"winky/internal/llm"
	"winky/internal/tool"
)

// maxSteps caps how many model requests one turn may make before giving up, so a
// model that keeps asking for tools without answering cannot spin forever.
const maxSteps = 100

// Toolset is the tool surface an Agent exposes to the model. *tool.Registry
// satisfies it; tests can inject a scripted one.
type Toolset interface {
	Specs() []llm.ToolSpec
	Run(ctx context.Context, call tool.Call) tool.Result
	Sequential(name string) bool
}

// Agent keeps an LLM streamer, the accumulated history, and the tools it may run.
type Agent struct {
	streamer        llm.Streamer
	model           string
	systemPrompt    string
	temperature     float64
	maxOutputTokens int
	tools           Toolset
	events          chan Event

	history    []llm.Message
	totalUsage llm.TokenUsage
}

// Option configures an Agent.
type Option func(*Agent)

// WithModel sets the model name sent with every request.
func WithModel(name string) Option {
	return func(a *Agent) { a.model = name }
}

// WithSystemPrompt sets a system prompt (sent as model instructions).
func WithSystemPrompt(prompt string) Option {
	return func(a *Agent) { a.systemPrompt = prompt }
}

// WithTemperature sets the sampling temperature.
func WithTemperature(t float64) Option {
	return func(a *Agent) { a.temperature = t }
}

// WithMaxOutputTokens sets the maximum number of output tokens per request.
func WithMaxOutputTokens(n int) Option {
	return func(a *Agent) { a.maxOutputTokens = n }
}

// WithTools installs the tool set the model may call.
func WithTools(ts Toolset) Option {
	return func(a *Agent) { a.tools = ts }
}

// New builds an Agent around the given streamer.
func New(streamer llm.Streamer, opts ...Option) *Agent {
	a := &Agent{streamer: streamer}
	for _, opt := range opts {
		opt(a)
	}
	a.events = make(chan Event, 32)
	return a
}

// Turn sends one user message and loops until the model answers without calling
// a tool: each response that asks for tools is executed and fed back, then the
// model is asked again.
func (a *Agent) Turn(ctx context.Context, input string) (Turn, error) {
	a.history = append(a.history, llm.Message{
		Role: llm.RoleUser, Content: input, Timestamp: time.Now(),
	})
	a.emit(TurnStart{Timestamp: time.Now()})

	var (
		turn      Turn
		turnUsage llm.TokenUsage
	)

	for step := 0; ; step++ {
		if step >= maxSteps {
			return Turn{}, a.fail(fmt.Errorf("turn exceeded %d model calls", maxSteps), turnUsage)
		}

		req := llm.Request{
			Model:           a.model,
			Messages:        a.history,
			SystemPrompt:    a.systemPrompt,
			Temperature:     a.temperature,
			MaxOutputTokens: a.maxOutputTokens,
		}
		if a.tools != nil {
			req.Tools = a.tools.Specs()
		}

		resp, usage, err := a.stream(ctx, req)
		turnUsage = turnUsage.Add(usage)
		if err != nil {
			return Turn{}, a.fail(err, turnUsage)
		}

		stepTurn := turnFromResponse(resp)
		turn.Blocks = append(turn.Blocks, stepTurn.Blocks...)
		a.history = append(a.history, messageFromStep(stepTurn))

		calls := stepTurn.ToolCalls()
		if len(calls) == 0 {
			break
		}

		tcalls := make([]tool.Call, len(calls))
		for i, b := range calls {
			tcalls[i] = tool.Call{ID: b.ID, Name: b.Name, Arguments: b.Arguments}
		}

		results := a.runTools(ctx, tcalls)
		turn.ToolResults = append(turn.ToolResults, results...)
		for _, r := range results {
			a.emit(ToolResult{
				CallID: r.CallID, Name: r.Name, Output: r.Output,
				IsError: r.IsError, Duration: r.Duration,
			})
			a.history = append(a.history, llm.Message{
				Role: llm.RoleToolOutput, Name: r.Name,
				ToolCallID: r.CallID, Content: r.Output, Timestamp: time.Now(),
			})
		}
	}

	a.totalUsage = a.totalUsage.Add(turnUsage)
	a.emit(TurnEnd{Turn: turn, Usage: turnUsage, TotalUsage: a.totalUsage})
	return turn, nil
}

// stream runs one model request, forwarding live fragments to the event channel
// and returning the terminal response and its usage.
func (a *Agent) stream(ctx context.Context, req llm.Request) (*llm.Response, llm.TokenUsage, error) {
	ch, err := a.streamer.Stream(ctx, req)
	if err != nil {
		return nil, llm.TokenUsage{}, err
	}

	stopReason := llm.StopReasonUnknown
	var streamErr error
	var finalResp *llm.Response
	var usage llm.TokenUsage

	for chunk := range ch {
		switch c := chunk.(type) {
		case llm.ReasoningDelta:
			a.emit(ReasoningDelta{ItemID: c.ItemID, Text: c.Text})
		case llm.ReasoningSummaryDelta:
			a.emit(ReasoningSummaryDelta{ItemID: c.ItemID, Text: c.Text})
		case llm.OutputDelta:
			a.emit(OutputDelta{ItemID: c.ItemID, Text: c.Text})
		case llm.RefusalDelta:
			a.emit(OutputDelta{ItemID: c.ItemID, Text: c.Text})
		case llm.ToolCallStart:
			a.emit(ToolCallStart{CallID: c.CallID, Name: c.Name})
		case llm.ToolCallArgsDelta:
			a.emit(ToolCallArgsDelta{CallID: c.CallID, Delta: c.Delta})
		case llm.ToolCallEnd:
			a.emit(ToolCallEnd{CallID: c.CallID, Arguments: c.Arguments})
		case llm.ResponseEnd:
			stopReason = c.StopReason
			streamErr = c.Err
			finalResp = c.Response
			if c.Response != nil && c.Response.Usage != nil {
				usage = *c.Response.Usage
			}
		}
	}

	if stopReason != llm.StopReasonDone {
		if streamErr == nil {
			streamErr = fmt.Errorf("stream stopped: %s", stopReason)
		}
		return nil, usage, streamErr
	}
	return finalResp, usage, nil
}

// runTools executes the calls of one step. Calls run concurrently unless any
// tool requires sequential execution, in which case all run in issue order.
func (a *Agent) runTools(ctx context.Context, calls []tool.Call) []ToolResult {
	results := make([]ToolResult, len(calls))
	if a.tools == nil {
		for i, c := range calls {
			results[i] = ToolResult{CallID: c.ID, Name: c.Name, Output: "error: no tools are registered", IsError: true}
		}
		return results
	}

	sequential := false
	for _, c := range calls {
		if a.tools.Sequential(c.Name) {
			sequential = true
			break
		}
	}
	if sequential {
		for i, c := range calls {
			results[i] = a.runOne(ctx, c)
		}
		return results
	}

	var wg sync.WaitGroup
	for i, c := range calls {
		wg.Add(1)
		go func(i int, c tool.Call) {
			defer wg.Done()
			results[i] = a.runOne(ctx, c)
		}(i, c)
	}
	wg.Wait()
	return results
}

func (a *Agent) runOne(ctx context.Context, c tool.Call) ToolResult {
	start := time.Now()
	res := a.tools.Run(ctx, c)
	return ToolResult{
		CallID: c.ID, Name: c.Name, Output: res.Output,
		IsError: res.IsError, Duration: time.Since(start),
	}
}

// fail records the accumulated usage and emits TurnFailed before returning err.
func (a *Agent) fail(err error, usage llm.TokenUsage) error {
	a.totalUsage = a.totalUsage.Add(usage)
	a.emit(TurnFailed{Err: err, Usage: usage, TotalUsage: a.totalUsage})
	return err
}

// messageFromStep builds the assistant history message for one model response.
func messageFromStep(step Turn) llm.Message {
	m := llm.Message{Role: llm.RoleAssistant, Content: step.Output(), Timestamp: time.Now()}
	for _, b := range step.ToolCalls() {
		m.ToolCalls = append(m.ToolCalls, llm.ToolCall{ID: b.ID, Name: b.Name, Arguments: b.Arguments})
	}
	return m
}

// emit sends one event to the events channel, blocking until a consumer reads
// it. Callers must keep a reader draining the channel during every turn.
func (a *Agent) emit(e Event) {
	a.events <- e
}

func (a *Agent) Events() <-chan Event {
	return a.events
}

// History returns a copy of the conversation so far.
func (a *Agent) History() []llm.Message {
	out := make([]llm.Message, len(a.history))
	copy(out, a.history)
	return out
}
