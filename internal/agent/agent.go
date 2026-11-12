// Package agent implements a multi-turn conversational agent loop: it records
// the transcript log, projects the model-facing view from it, and drives the
// llm.Streamer so consecutive turns share context.
package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"winky/internal/llm"
	"winky/internal/prompt"
	"winky/internal/schema"
	"winky/internal/tool"
	"winky/internal/transcript"
)

// maxSteps caps how many model requests one turn may make before giving up, so a
// model that keeps asking for tools without answering cannot spin forever.
const maxSteps = 100

// ErrBusy reports a Turn or Compact that overlapped another one. Both fold
// events into the same projection and append to the same transcript.
var ErrBusy = errors.New("agent: a turn or compaction is already running")

// ErrAborted reports a turn the user cancelled. The partial Turn is still
// returned and the transcript is closed with TurnAborted.
var ErrAborted = errors.New("agent: turn aborted")

const (
	// maxStreamRetries bounds how often one model request is retried.
	maxStreamRetries = 3
	// retryBaseDelay doubles after each failed attempt.
	retryBaseDelay = 500 * time.Millisecond
)

// Agent keeps an LLM streamer, the event log its view is projected from, and
// the tools it may run.
type Agent struct {
	streamer        llm.Streamer
	model           string
	initialPrompt   string
	temperature     float64
	maxOutputTokens int
	tools           Toolset
	events          chan schema.Event

	// log is the append-only copy of the persisted event log. It is only ever
	// appended to, never derived, so it cannot disagree with the transcript; the
	// view is projected from it on demand.
	log []transcript.Event
	// logLoaded is false until the persisted transcript has been read.
	// A nil recorder means there is nothing to load.
	logLoaded bool

	tsRecorder transcript.Recorder

	// busy serializes Turn and Compact; the TUI also waits, but the guarantee
	// belongs to the agent.
	busy sync.Mutex

	// Context compaction. Everything the next request needs is folded out of the
	// log by derive; only the per-turn compaction budget survives across calls.
	compact             CompactionOptions
	contextWindow       int
	toolTokens          int
	compactionsThisTurn int
	// calls breaks a per-turn loop of identical tool calls.
	calls callGuard
}

// Option configures an Agent.
type Option func(*Agent)

// WithModel sets the model name sent with every request.
func WithModel(name string) Option {
	return func(a *Agent) { a.model = name }
}

// WithInitialSystemPrompt seeds the system prompt of a new session. A session
// that already records one keeps it, so resuming ignores this.
func WithInitialSystemPrompt(text string) Option {
	return func(a *Agent) { a.initialPrompt = text }
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
// recorder disables transcript writes and leaves the in-memory log as the only
// record.
func WithTranscriptRecorder(r transcript.Recorder) Option {
	return func(a *Agent) { a.tsRecorder = r }
}

// New builds an Agent around the given streamer.
func New(streamer llm.Streamer, opts ...Option) *Agent {
	a := &Agent{streamer: streamer}
	for _, opt := range opts {
		opt(a)
	}
	a.normalizeCompaction()
	if a.tools != nil {
		a.toolTokens = estimateToolTokens(a.tools)
	}
	a.events = make(chan schema.Event, 32)
	if a.tsRecorder == nil {
		a.logLoaded = true
	}
	return a
}

// Turn sends one user message and loops until the model answers without calling
// a tool: each response that asks for tools is executed and fed back, then the
// model is asked again.
//
// A cancelled turn is kept: its user message, partial output and tool calls are
// closed by TurnAborted, so "continue" can resume the partial work.
func (a *Agent) Turn(ctx context.Context, input string) (Turn, error) {
	if !a.busy.TryLock() {
		return Turn{}, ErrBusy
	}
	defer a.busy.Unlock()

	startTime := time.Now()

	var (
		turn      Turn
		turnUsage llm.TokenUsage
	)

	if err := a.loadLog(); err != nil {
		return a.closeTurn(ctx, err, turnUsage, turn, false)
	}
	if err := a.record(transcript.TurnStart{Timestamp: startTime}); err != nil {
		return a.closeTurn(ctx, err, turnUsage, turn, false)
	}
	a.emit(schema.TurnStart{Timestamp: startTime})

	// Record before anything cancellable, so an abort always has a request to
	// close.
	if err := a.record(transcript.UserMessage{Content: input}); err != nil {
		return a.closeTurn(ctx, err, turnUsage, turn, false)
	}

	a.compactionsThisTurn = 0
	a.calls = callGuard{}
	if err := a.maybeCompact(ctx, false); err != nil {
		return a.closeTurn(ctx, err, turnUsage, turn, false)
	}

	for step := 0; ; step++ {
		if step >= maxSteps {
			return a.closeTurn(
				ctx, fmt.Errorf("turn exceeded %d model calls", maxSteps),
				turnUsage, turn, false,
			)
		}

		// The previous step is closed, so the view still holds whole tool-call
		// groups here.
		if step > 0 {
			if err := a.maybeCompact(ctx, true); err != nil {
				return a.closeTurn(ctx, err, turnUsage, turn, false)
			}
		}
		if ctx.Err() != nil {
			return a.closeTurn(ctx, ctx.Err(), turnUsage, turn, false)
		}

		if err := a.record(transcript.StepStart{Timestamp: time.Now()}); err != nil {
			return a.closeTurn(ctx, err, turnUsage, turn, false)
		}

		v, err := a.project()
		if err != nil {
			return a.closeTurn(ctx, err, turnUsage, turn, false)
		}

		req := llm.Request{
			Model:           a.model,
			Messages:        v.messages(),
			SystemPrompt:    v.systemPrompt(),
			Temperature:     a.temperature,
			MaxOutputTokens: a.maxOutputTokens,
		}
		if a.tools != nil {
			req.Tools = a.tools.Specs()
		}

		resp, usage, draft, err := a.stream(ctx, req)
		turnUsage = turnUsage.Add(usage)
		if usage.PromptTokens > 0 {
			// The prompt-token count is the exact size of the request just
			// sent, so it is the best available measure of context usage. It
			// also anchors the next estimate, through the StepEnd below.
			a.emit(schema.ContextUsage{Tokens: usage.PromptTokens})
		}
		if err != nil {
			// Persist the partial step and pair its completed tool calls.
			if recErr := a.recordStep(draft); recErr != nil {
				err = recErr
			}
			turn.Blocks = append(turn.Blocks, draft.Blocks...)
			a.appendUnresultedToolCalls(&turn, draft.ToolCalls(), ctx.Err() != nil)
			return a.closeTurn(ctx, err, turnUsage, turn, true)
		}

		stepTurn := turnFromResponse(resp)
		if err := a.recordStep(stepTurn); err != nil {
			return a.closeTurn(ctx, err, turnUsage, turn, true)
		}

		turn.Blocks = append(turn.Blocks, stepTurn.Blocks...)

		calls := stepTurn.ToolCalls()
		if len(calls) == 0 {
			if err := a.record(transcript.StepEnd{Timestamp: time.Now(), Usage: usagePtr(usage)}); err != nil {
				return a.closeTurn(ctx, err, turnUsage, turn, false)
			}
			break
		}

		tcalls := make([]tool.Call, len(calls))
		for i, b := range calls {
			tcalls[i] = tool.Call{ID: b.ID, Name: b.Name, Arguments: b.Arguments}
		}

		// Record and report each call as it settles: a fast tool must not wait
		// for a slow sibling to appear in the transcript or on screen.
		var recordErr error
		a.runTools(ctx, tcalls, func(r ToolResult) {
			turn.ToolResults = append(turn.ToolResults, r)
			if recordErr != nil {
				return
			}
			if err := a.record(transcript.ToolResult{
				CallID: r.CallID, Output: r.Output, Aborted: r.Aborted,
			}); err != nil {
				recordErr = err
				return
			}
			a.emit(
				ToolResult{
					CallID: r.CallID, Name: r.Name, Output: r.Output,
					IsError: r.IsError, Aborted: r.Aborted,
					Duration: r.Duration, Budget: r.Budget,
				},
			)
		})
		if recordErr != nil {
			return a.closeTurn(ctx, recordErr, turnUsage, turn, true)
		}

		if err := a.record(transcript.StepEnd{Timestamp: time.Now(), Usage: usagePtr(usage)}); err != nil {
			return a.closeTurn(ctx, err, turnUsage, turn, false)
		}
		// Cancelled during tools: results are paired, close before asking again.
		if ctx.Err() != nil {
			return a.closeTurn(ctx, ctx.Err(), turnUsage, turn, false)
		}
	}

	if err := a.record(transcript.TurnEnd{Timestamp: time.Now()}); err != nil {
		return a.closeTurn(ctx, err, turnUsage, turn, false)
	}

	a.emit(schema.TurnEnd{Turn: turn, Usage: turnUsage, TotalUsage: a.total()})
	return turn, nil
}

// appendUnresultedToolCalls pairs committed but unexecuted tool calls with a
// result.
func (a *Agent) appendUnresultedToolCalls(turn *Turn, calls []Block, aborted bool) {
	output := transcript.MissingToolResult
	if aborted {
		output = transcript.AbortedToolResult
	}
	for _, b := range calls {
		r := ToolResult{
			CallID: b.ID, Name: b.Name, Output: output,
			IsError: true, Aborted: aborted,
		}
		_ = a.record(transcript.ToolResult{CallID: r.CallID, Output: r.Output, Aborted: aborted})
		a.emit(schema.ToolResult{
			CallID: r.CallID, Name: r.Name, Output: r.Output,
			IsError: true, Aborted: aborted,
		})
		turn.ToolResults = append(turn.ToolResults, r)
	}
}

// closeTurn ends the turn: TurnAborted for a cancelled context, TurnFailed
// otherwise. stepOpen records the missing StepEnd.
func (a *Agent) closeTurn(
	ctx context.Context, err error, usage llm.TokenUsage, turn Turn, stepOpen bool,
) (Turn, error) {
	if stepOpen {
		_ = a.record(transcript.StepEnd{Timestamp: time.Now(), Usage: usagePtr(usage)})
	}
	total := a.total()

	if ctx.Err() != nil {
		reason := ctx.Err().Error()
		_ = a.record(transcript.TurnAborted{Timestamp: time.Now(), Reason: reason})
		a.emit(schema.TurnAborted{
			Turn: turn, Usage: usage, TotalUsage: total, Reason: reason,
		})
		return turn, fmt.Errorf("%w: %v", ErrAborted, reason)
	}

	if err == nil {
		err = errors.New("agent: turn failed without an error")
	}
	_ = a.record(transcript.TurnFailed{Timestamp: time.Now(), Error: err.Error()})
	a.emit(schema.TurnFailed{
		Err: err, Turn: turn, Usage: usage, TotalUsage: total,
	})
	return turn, err
}

// record appends one event to the transcript and to the in-memory copy of the
// log. Because it never derives anything, the copy cannot drift from the file:
// the view is projected from it when it is needed.
func (a *Agent) record(ev transcript.Event) error {
	if a.tsRecorder != nil {
		sequenced, err := a.tsRecorder.Append(ev)
		if err != nil {
			return fmt.Errorf("transcript append %s: %w", ev.Type(), err)
		}
		// Keep the sequenced copy: compaction ranges use those numbers, so live
		// and replay must agree on them.
		ev = sequenced
	}
	a.log = append(a.log, ev)
	return nil
}

// project folds the event log into the view the next request is built from.
func (a *Agent) project() (*view, error) {
	v, _, err := a.derive()
	if err != nil {
		return nil, fmt.Errorf("project transcript: %w", err)
	}
	return v, nil
}

// loadLog reads the persisted transcript once, before the first turn. The log is
// only published after it projects cleanly, so a malformed transcript cannot
// leave half of itself behind.
func (a *Agent) loadLog() error {
	if a.logLoaded {
		return nil
	}
	if a.tsRecorder == nil {
		a.logLoaded = true
		return a.seedPrompt(newView())
	}

	events, err := a.tsRecorder.ReadAll()
	if err != nil {
		return fmt.Errorf("read transcript: %w", err)
	}
	v, err := project(events)
	if err != nil {
		return fmt.Errorf("replay transcript: %w", err)
	}
	a.log = events
	a.logLoaded = true
	return a.seedPrompt(v)
}

// seedPrompt records the initial prompt as the base section of a session that
// has none.
func (a *Agent) seedPrompt(v *view) error {
	if a.initialPrompt == "" || v.prompt.Has(prompt.SlotBase) {
		return nil
	}
	return a.record(transcript.PromptChanged{
		Source: "startup",
		Slot:   string(prompt.SlotBase),
		Name:   prompt.BaseName,
		Text:   a.initialPrompt,
	})
}

// recordStep writes the reasoning, reasoning summary, and assistant output
// produced by one model response before that response joins the log.
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
	return a.record(
		transcript.AssistantOutputMessage{
			Content:   content,
			ToolCalls: toolCalls,
		},
	)
}

// stream runs one model request, retrying transient failures with backoff. An
// attempt that already emitted output is never retried: the same step would be
// asked twice and the user already saw the partial answer.
//
// The usage of a discarded attempt is dropped with it. It never reaches the
// transcript, so counting it live would make a resumed session report a
// different total than the one it showed before it exited.
func (a *Agent) stream(ctx context.Context, req llm.Request) (*llm.Response, llm.TokenUsage, Turn, error) {
	for attempt := 0; ; attempt++ {
		resp, usage, draft, produced, err := a.streamOnce(ctx, req)
		if err == nil {
			return resp, usage, Turn{}, nil
		}
		if produced || attempt >= maxStreamRetries || !llm.Retryable(err) || ctx.Err() != nil {
			return nil, usage, draft, err
		}

		delay := retryBaseDelay << attempt
		a.emit(schema.Retrying{Attempt: attempt + 1, Max: maxStreamRetries, Delay: delay, Err: err})
		select {
		case <-ctx.Done():
			return nil, usage, draft, err
		case <-time.After(delay):
		}
	}
}

// streamOnce runs one attempt, forwarding and accumulating fragments. On cancel
// it stops forwarding but keeps draining so the partial step can be persisted.
func (a *Agent) streamOnce(
	ctx context.Context, req llm.Request,
) (*llm.Response, llm.TokenUsage, Turn, bool, error) {
	ch, err := a.streamer.Stream(ctx, req)
	if err != nil {
		return nil, llm.TokenUsage{}, Turn{}, false, err
	}

	draft := newTurnDraft()
	stopReason := llm.StopReasonUnknown
	var streamErr error
	var finalResp *llm.Response
	var usage llm.TokenUsage
	produced := false

	// Consume the chunk groups obtained from the stream iteration
	// and generate corresponding events
	for chunk := range ch {
		// Always consume the terminal event: it carries the stop reason and usage.
		if end, ok := chunk.(llm.ResponseEnd); ok {
			stopReason = end.StopReason
			streamErr = end.Err
			finalResp = end.Response
			if end.Response != nil && end.Response.Usage != nil {
				usage = *end.Response.Usage
			}
			continue
		}
		if ctx.Err() != nil {
			continue
		}

		switch c := chunk.(type) {
		case llm.ReasoningDelta:
			draft.text(BlockReasoning, c.ItemID, c.Text)
			a.emit(schema.ReasoningDelta{ItemID: c.ItemID, Text: c.Text})
			produced = true
		case llm.ReasoningSummaryDelta:
			draft.text(BlockReasoningSummary, c.ItemID, c.Text)
			a.emit(schema.ReasoningSummaryDelta{ItemID: c.ItemID, Text: c.Text})
			produced = true
		case llm.OutputDelta:
			draft.text(BlockMessage, c.ItemID, c.Text)
			a.emit(schema.OutputDelta{ItemID: c.ItemID, Text: c.Text})
			produced = true
		case llm.RefusalDelta:
			draft.text(BlockMessage, c.ItemID, c.Text)
			a.emit(schema.OutputDelta{ItemID: c.ItemID, Text: c.Text})
			produced = true
		case llm.ToolCallStart:
			draft.startTool(c.CallID, c.Name)
			a.emit(schema.ToolCallStart{CallID: c.CallID, Name: c.Name})
			produced = true
		case llm.ToolCallArgsDelta:
			draft.toolArgs(c.CallID, c.Delta)
			a.emit(schema.ToolCallArgsDelta{CallID: c.CallID, Delta: c.Delta})
			produced = true
		case llm.ToolCallEnd:
			draft.endTool(c.CallID, c.Arguments)
			a.emit(schema.ToolCallEnd{CallID: c.CallID, Arguments: c.Arguments})
			produced = true
		}
	}

	// A completed response wins over a cancellation that raced its last chunks.
	if stopReason == llm.StopReasonDone {
		return finalResp, usage, Turn{}, produced, nil
	}
	if ctx.Err() != nil {
		return nil, usage, draft.Turn(), produced, ctx.Err()
	}
	if streamErr == nil {
		streamErr = fmt.Errorf("stream stopped: %s", stopReason)
	}
	return nil, usage, draft.Turn(), produced, streamErr
}

// usagePtr converts provider usage into the stored form, or nil when the
// provider reported none, so a zero value never reaches the transcript.
func usagePtr(u llm.TokenUsage) *transcript.Usage {
	if u.TotalTokens == 0 && u.PromptTokens == 0 && u.CompletionTokens == 0 {
		return nil
	}
	return &transcript.Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CachedTokens:     u.CachedTokens,
	}
}

// llmUsage converts stored usage back into the provider-facing type.
func llmUsage(u transcript.Usage) llm.TokenUsage {
	return llm.TokenUsage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CachedTokens:     u.CachedTokens,
	}
}

// emit sends one event to the events channel, blocking until a consumer reads
// it. Callers must keep a reader draining the channel during every turn.
func (a *Agent) emit(e schema.Event) {
	a.events <- e
}

func (a *Agent) Events() <-chan schema.Event {
	return a.events
}
