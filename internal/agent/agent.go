// Package agent implements a minimal multi-turn conversational agent loop.
//
// It owns the conversation history and drives the llm.Streamer: every Turn
// appends the user message, streams one model reply, and appends it back so
// the next turn carries full context.
package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"winky/internal/llm"
)

// Agent keeps an LLM streamer and the accumulated conversation history.
type Agent struct {
	streamer     llm.Streamer
	model        string
	systemPrompt string
	temperature  float64
	maxTokens    int
	events       chan Event

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

// WithMaxTokens sets the maximum number of output tokens per turn.
func WithMaxTokens(n int) Option {
	return func(a *Agent) { a.maxTokens = n }
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

// TurnResult is the outcome of one Turn.
type TurnResult struct {
	Reasoning string
	Output    string
}

// Turn sends one user message and returns the model's reasoning and output,
// maintaining history across calls so consecutive turns share context.
func (a *Agent) Turn(ctx context.Context, input string) (TurnResult, error) {
	a.history = append(
		a.history, llm.Message{
			Role:      llm.RoleUser,
			Content:   input,
			Timestamp: time.Now(),
		},
	)

	req := llm.Request{
		Model:        a.model,
		Messages:     a.history,
		SystemPrompt: a.systemPrompt,
		Temperature:  a.temperature,
		MaxTokens:    a.maxTokens,
	}

	ch, err := a.streamer.Stream(ctx, req)
	if err != nil {
		a.emit(TurnFailed{Err: err})
		return TurnResult{}, err
	}

	// Accumulate the reasoning and output text deltas, forwarding each delta
	// to the events channel as it arrives.
	var reasoning strings.Builder
	var output strings.Builder
	stopReason := llm.StopReasonUnknown
	var streamErr error
	var turnUsage llm.TokenUsage
	var hasUsage bool

	var uc []string

	for chunk := range ch {
		switch c := chunk.(type) {
		case llm.AgentStart:
			a.emit(TurnStart{Timestamp: c.Timestamp})

		case llm.ReasoningContentDelta:
			reasoning.WriteString(c.Delta)
			a.emit(ReasoningDelta{Text: c.Delta})

		case llm.OutputContentDelta:
			output.WriteString(c.Delta)
			a.emit(OutputDelta{Text: c.Delta})

		case llm.AgentEnd:
			stopReason = c.StopReason
			streamErr = c.Err
			if c.Usage != nil {
				turnUsage = *c.Usage
				hasUsage = true
			}

		case llm.UnknownChunk:
			uc = append(uc, c.Raw)
		}
	}

	// just for debug
	// fmt.Println("{\"all\":[" + strings.Join(uc, ",") + "]}")

	a.totalUsage = a.totalUsage.Add(turnUsage)

	result := TurnResult{
		Reasoning: reasoning.String(),
		Output:    output.String(),
	}

	if stopReason != llm.StopReasonDone {
		if streamErr == nil {
			streamErr = fmt.Errorf("stream stopped: %s", stopReason)
		}
		a.emit(TurnFailed{Err: streamErr, Usage: turnUsage, TotalUsage: a.totalUsage})
		return TurnResult{}, streamErr
	}

	var msgUsage *llm.TokenUsage
	if hasUsage {
		msgUsage = &turnUsage
	}
	a.history = append(
		a.history, llm.Message{
			Role:      llm.RoleAssistant,
			Content:   result.Output,
			Usage:     msgUsage,
			Timestamp: time.Now(),
		},
	)

	a.emit(
		TurnEnd{
			Reasoning:  result.Reasoning,
			Output:     result.Output,
			Usage:      turnUsage,
			TotalUsage: a.totalUsage,
		},
	)

	return result, nil
}

// emit sends one event to the events channel. It blocks until a consumer
// reads it, so a consumer (currently a placeholder in cmd/agent) must drain
// the channel during every turn.
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
