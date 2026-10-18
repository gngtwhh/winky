// Package agent implements a minimal multi-turn conversational agent loop: it
// owns the history and drives the llm.Streamer so consecutive turns share
// context.
package agent

import (
	"context"
	"fmt"
	"time"

	"winky/internal/llm"
)

// Agent keeps an LLM streamer and the accumulated conversation history.
type Agent struct {
	streamer        llm.Streamer
	model           string
	systemPrompt    string
	temperature     float64
	maxOutputTokens int
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

// WithMaxOutputTokens sets the maximum number of output tokens per turn.
func WithMaxOutputTokens(n int) Option {
	return func(a *Agent) { a.maxOutputTokens = n }
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

// Turn sends one user message, streams one model reply and appends both to the
// history.
//
// Live fragments are forwarded to the event channel; the authoritative Turn is
// built from the terminal response and delivered on TurnEnd.
func (a *Agent) Turn(ctx context.Context, input string) (Turn, error) {
	a.history = append(
		a.history, llm.Message{
			Role:      llm.RoleUser,
			Content:   input,
			Timestamp: time.Now(),
		},
	)

	req := llm.Request{
		Model:           a.model,
		Messages:        a.history,
		SystemPrompt:    a.systemPrompt,
		Temperature:     a.temperature,
		MaxOutputTokens: a.maxOutputTokens,
	}

	ch, err := a.streamer.Stream(ctx, req)
	if err != nil {
		a.emit(TurnFailed{Err: err})
		return Turn{}, err
	}

	stopReason := llm.StopReasonUnknown
	var streamErr error
	var finalResp *llm.Response
	var turnUsage llm.TokenUsage
	var hasUsage bool

	for chunk := range ch {
		switch c := chunk.(type) {
		case llm.ResponseStart:
			a.emit(TurnStart{Timestamp: c.Timestamp})

		case llm.ReasoningDelta:
			a.emit(ReasoningDelta{ItemID: c.ItemID, Text: c.Text})

		case llm.ReasoningSummaryDelta:
			a.emit(ReasoningSummaryDelta{ItemID: c.ItemID, Text: c.Text})

		case llm.OutputDelta:
			a.emit(OutputDelta{ItemID: c.ItemID, Text: c.Text})

		case llm.RefusalDelta:
			// Fold a refusal into the output stream; the authoritative Turn
			// still carries it via the message block.
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
				turnUsage = *c.Response.Usage
				hasUsage = true
			}
		}
	}

	a.totalUsage = a.totalUsage.Add(turnUsage)

	turn := turnFromResponse(finalResp)

	if stopReason != llm.StopReasonDone {
		if streamErr == nil {
			streamErr = fmt.Errorf("stream stopped: %s", stopReason)
		}
		a.emit(TurnFailed{Err: streamErr, Usage: turnUsage, TotalUsage: a.totalUsage})
		return Turn{}, streamErr
	}

	var msgUsage *llm.TokenUsage
	if hasUsage {
		msgUsage = &turnUsage
	}
	a.history = append(
		a.history, llm.Message{
			Role:      llm.RoleAssistant,
			Content:   turn.Output(),
			Usage:     msgUsage,
			Timestamp: time.Now(),
		},
	)

	a.emit(
		TurnEnd{
			Turn:       turn,
			Usage:      turnUsage,
			TotalUsage: a.totalUsage,
		},
	)

	return turn, nil
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
