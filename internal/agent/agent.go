// Package agent implements a minimal multi-turn conversational agent loop.
//
// It owns the conversation history and drives the llm.Streamer: every Turn
// appends the user message, streams one model reply, and appends it back so
// the next turn carries full context.
package agent

import (
	"context"
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

	history []llm.Message
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
	return a
}

// Turn sends one user message and returns the assistant's full reply,
// maintaining history across calls so consecutive turns share context.
func (a *Agent) Turn(ctx context.Context, input string) (string, error) {
	a.history = append(a.history, llm.Message{
		Role:      llm.RoleUser,
		Content:   input,
		Timestamp: time.Now(),
	})

	req := llm.Request{
		Model:        a.model,
		Messages:     a.history,
		SystemPrompt: a.systemPrompt,
		Temperature:  a.temperature,
		MaxTokens:    a.maxTokens,
	}

	ch, err := a.streamer.Stream(ctx, req)
	if err != nil {
		return "", err
	}

	// Accumulate text deltas into a full reply. Reasoning and tool-call chunks
	// are ignored for now; a stream error aborts the turn.
	var sb strings.Builder
	for chunk := range ch {
		switch c := chunk.(type) {
		case llm.TextDelta:
			sb.WriteString(c.Text)
		case llm.ErrorChunk:
			return "", c.Err
		}
	}

	reply := sb.String()
	a.history = append(a.history, llm.Message{
		Role:      llm.RoleAssistant,
		Content:   reply,
		Timestamp: time.Now(),
	})

	return reply, nil
}

// History returns a copy of the conversation so far.
func (a *Agent) History() []llm.Message {
	out := make([]llm.Message, len(a.history))
	copy(out, a.history)
	return out
}
