// Package agent implements a multi-turn conversational agent loop: it owns the
// history and drives the llm.Streamer so consecutive turns share context.
package agent

import (
	"context"
	"fmt"
	"sync"
	"time"
	"winky/internal/schema"
	"winky/internal/transcript"

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
	events          chan schema.Event

	totalUsage llm.TokenUsage
	history    *historyProjection
	// historyLoaded is false until the persisted transcript has been replayed.
	// A nil recorder means there is nothing to load.
	historyLoaded bool

	tsRecorder transcript.Recorder

	// Context compaction.
	compact       CompactionOptions
	contextWindow int
	toolTokens    int

	// Prompt-size anchors: the provider's last count and the local estimate of
	// that same request. Only the difference since then is estimated later.
	lastPromptTokens    int
	lastPromptEstimate  int
	compactionsThisTurn int
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

// WithTranscriptRecorder installs the persistent transcript recorder. A nil
// recorder disables transcript writes and leaves the in-memory history as the
// only record.
func WithTranscriptRecorder(r transcript.Recorder) Option {
	return func(a *Agent) { a.tsRecorder = r }
}

// New builds an Agent around the given streamer.
func New(streamer llm.Streamer, opts ...Option) *Agent {
	a := &Agent{streamer: streamer, history: newHistoryProjection()}
	for _, opt := range opts {
		opt(a)
	}
	a.normalizeCompaction()
	if a.tools != nil {
		a.toolTokens = estimateTools(a.tools)
	}
	a.events = make(chan schema.Event, 32)
	if a.tsRecorder == nil {
		a.historyLoaded = true
	}
	return a
}

// Turn sends one user message and loops until the model answers without calling
// a tool: each response that asks for tools is executed and fed back, then the
// model is asked again.
func (a *Agent) Turn(ctx context.Context, input string) (Turn, error) {
	startTime := time.Now()

	var (
		turn      Turn
		turnUsage llm.TokenUsage
	)

	if err := a.loadHistory(); err != nil {
		return Turn{}, a.fail(err, turnUsage)
	}
	if err := a.record(transcript.TurnStart{Timestamp: startTime}); err != nil {
		return Turn{}, a.fail(err, turnUsage)
	}
	a.emit(schema.TurnStart{Timestamp: startTime})

	// Compact before the user message is recorded, so this turn is never
	// summarized.
	a.compactionsThisTurn = 0
	if err := a.maybeCompact(ctx, false); err != nil {
		return Turn{}, a.fail(err, turnUsage)
	}
	if err := a.record(transcript.UserMessage{Content: input}); err != nil {
		return Turn{}, a.fail(err, turnUsage)
	}

	for step := 0; ; step++ {
		if step >= maxSteps {
			return Turn{}, a.fail(fmt.Errorf("turn exceeded %d model calls", maxSteps), turnUsage)
		}

		// The previous step is closed, so the history is still whole tool-call
		// groups here.
		if step > 0 {
			if err := a.maybeCompact(ctx, true); err != nil {
				return Turn{}, a.fail(err, turnUsage)
			}
		}

		if err := a.record(transcript.StepStart{Timestamp: time.Now()}); err != nil {
			return Turn{}, a.fail(err, turnUsage)
		}

		req := llm.Request{
			Model:           a.model,
			Messages:        a.history.Messages(),
			SystemPrompt:    a.systemPrompt,
			Temperature:     a.temperature,
			MaxOutputTokens: a.maxOutputTokens,
		}
		if a.tools != nil {
			req.Tools = a.tools.Specs()
		}

		resp, usage, err := a.stream(ctx, req)
		turnUsage = turnUsage.Add(usage)
		if usage.PromptTokens > 0 {
			// The prompt-token count is the exact size of the request just
			// sent, so it is the best available measure of context usage.
			a.lastPromptTokens = usage.PromptTokens
			a.lastPromptEstimate = a.estimateRequest(req.Messages)
			a.emit(schema.ContextUsage{Tokens: usage.PromptTokens})
		}
		if err != nil {
			_ = a.record(transcript.StepEnd{Timestamp: time.Now()})
			return Turn{}, a.fail(err, turnUsage)
		}

		stepTurn := turnFromResponse(resp)
		if err := a.recordStep(stepTurn); err != nil {
			_ = a.record(transcript.StepEnd{Timestamp: time.Now()})
			return Turn{}, a.fail(err, turnUsage)
		}

		turn.Blocks = append(turn.Blocks, stepTurn.Blocks...)

		calls := stepTurn.ToolCalls()
		if len(calls) == 0 {
			if err := a.record(transcript.StepEnd{Timestamp: time.Now()}); err != nil {
				return Turn{}, a.fail(err, turnUsage)
			}
			break
		}

		tcalls := make([]tool.Call, len(calls))
		for i, b := range calls {
			tcalls[i] = tool.Call{ID: b.ID, Name: b.Name, Arguments: b.Arguments}
		}

		results := a.runTools(ctx, tcalls)
		turn.ToolResults = append(turn.ToolResults, results...)
		for _, r := range results {
			if err := a.record(transcript.ToolResult{CallID: r.CallID, Output: r.Output}); err != nil {
				_ = a.record(transcript.StepEnd{Timestamp: time.Now()})
				return Turn{}, a.fail(err, turnUsage)
			}
			a.emit(
				ToolResult{
					CallID: r.CallID, Name: r.Name, Output: r.Output,
					IsError: r.IsError, Duration: r.Duration,
				},
			)
		}

		if err := a.record(transcript.StepEnd{Timestamp: time.Now()}); err != nil {
			return Turn{}, a.fail(err, turnUsage)
		}
	}

	if err := a.record(transcript.TurnEnd{Timestamp: time.Now()}); err != nil {
		return Turn{}, a.fail(err, turnUsage)
	}

	a.totalUsage = a.totalUsage.Add(turnUsage)
	a.emit(schema.TurnEnd{Turn: turn, Usage: turnUsage, TotalUsage: a.totalUsage})
	return turn, nil
}

// record persists one event and folds it into the request-history projection.
func (a *Agent) record(ev transcript.Event) error {
	if a.tsRecorder != nil {
		sequenced, err := a.tsRecorder.Append(ev)
		if err != nil {
			return fmt.Errorf("transcript append %s: %w", ev.Type(), err)
		}
		// Fold the sequenced copy: compaction ranges use those numbers, so live
		// and replay must agree on them.
		ev = sequenced
	}
	if err := a.history.Apply(ev); err != nil {
		return fmt.Errorf("apply transcript %s: %w", ev.Type(), err)
	}
	return nil
}

// loadHistory replays the persisted transcript once, before the first turn.
// The projection is only swapped in after a clean replay, so a failed load
// cannot leave a half-populated history behind.
func (a *Agent) loadHistory() error {
	if a.historyLoaded {
		return nil
	}
	if a.tsRecorder == nil {
		a.historyLoaded = true
		return nil
	}

	events, err := a.tsRecorder.ReadAll()
	if err != nil {
		return fmt.Errorf("read transcript: %w", err)
	}
	h := newHistoryProjection()
	for _, ev := range events {
		if err := h.Apply(ev); err != nil {
			return fmt.Errorf("replay transcript: %w", err)
		}
	}
	a.history = h
	a.historyLoaded = true
	return nil
}

// recordStep writes the reasoning, reasoning summary, and assistant output
// produced by one model response before that response joins the in-memory
// history.
func (a *Agent) recordStep(step Turn) error {
	if reasoning := step.Reasoning(); reasoning != "" {
		if err := a.record(transcript.AssistantReasoningMessage{Content: reasoning}); err != nil {
			return err
		}
	}
	if summary := step.Summary(); summary != "" {
		if err := a.record(transcript.AssistantReasoningSummary{Content: summary}); err != nil {
			return err
		}
	}

	calls := step.ToolCalls()
	toolCalls := make([]transcript.ToolCall, len(calls))
	for i, c := range calls {
		toolCalls[i] = transcript.ToolCall{
			CallID: c.ID, Name: c.Name, Arguments: c.Arguments,
		}
	}

	content := step.Output()
	if content == "" && len(toolCalls) == 0 {
		return nil
	}
	return a.record(transcript.AssistantOutputMessage{
		Content:   content,
		ToolCalls: toolCalls,
	})
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

	// Consume the chunk groups obtained from the stream iteration
	// and generate corresponding events
	for chunk := range ch {
		switch c := chunk.(type) {
		case llm.ReasoningDelta:
			a.emit(schema.ReasoningDelta{ItemID: c.ItemID, Text: c.Text})
		case llm.ReasoningSummaryDelta:
			a.emit(schema.ReasoningSummaryDelta{ItemID: c.ItemID, Text: c.Text})
		case llm.OutputDelta:
			a.emit(schema.OutputDelta{ItemID: c.ItemID, Text: c.Text})
		case llm.RefusalDelta:
			a.emit(schema.OutputDelta{ItemID: c.ItemID, Text: c.Text})
		case llm.ToolCallStart:
			a.emit(schema.ToolCallStart{CallID: c.CallID, Name: c.Name})
		case llm.ToolCallArgsDelta:
			a.emit(schema.ToolCallArgsDelta{CallID: c.CallID, Delta: c.Delta})
		case llm.ToolCallEnd:
			a.emit(schema.ToolCallEnd{CallID: c.CallID, Arguments: c.Arguments})
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

// fail records the accumulated usage, closes the transcript turn, and emits
// TurnFailed before returning err.
func (a *Agent) fail(err error, usage llm.TokenUsage) error {
	a.totalUsage = a.totalUsage.Add(usage)
	_ = a.record(transcript.TurnFailed{Timestamp: time.Now(), Error: err.Error()})
	a.emit(schema.TurnFailed{Err: err, Usage: usage, TotalUsage: a.totalUsage})
	return err
}

// emit sends one event to the events channel, blocking until a consumer reads
// it. Callers must keep a reader draining the channel during every turn.
func (a *Agent) emit(e schema.Event) {
	a.events <- e
}

func (a *Agent) Events() <-chan schema.Event {
	return a.events
}

// History returns the request history projected from the transcript.
func (a *Agent) History() []llm.Message {
	return a.history.Messages()
}
