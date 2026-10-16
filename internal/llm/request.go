package llm

import (
	"context"
	"time"
)

const (
	RoleSystem     Role = "system"
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleToolOutput Role = "tools"
)

type Role string

type Message struct {
	Role       Role
	Name       string
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	Usage      *TokenUsage
	Timestamp  time.Time
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments string // json string TODO: improvement
}

type ToolParamUnit struct {
}

// ToolSpec defines the info of a llm layer tools
// it is needless to contain tools exec info
type ToolSpec struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON Schema
	Strict      bool
}

type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int
}

// Add returns the component-wise sum of two usage totals.
func (u TokenUsage) Add(o TokenUsage) TokenUsage {
	return TokenUsage{
		PromptTokens:     u.PromptTokens + o.PromptTokens,
		CompletionTokens: u.CompletionTokens + o.CompletionTokens,
		TotalTokens:      u.TotalTokens + o.TotalTokens,
		CachedTokens:     u.CachedTokens + o.CachedTokens,
	}
}

// CacheHitRate returns the fraction of input tokens served from cache, clamped
// to [0,1]. It returns 0 when there are no input tokens.
func (u TokenUsage) CacheHitRate() float64 {
	if u.PromptTokens <= 0 {
		return 0
	}
	rate := float64(u.CachedTokens) / float64(u.PromptTokens)
	if rate > 1 {
		return 1
	}
	if rate < 0 {
		return 0
	}
	return rate
}

// Request is a complete request record for an LLM call,
// and it is independent of the model provider and the api
type Request struct {
	Model        string
	Messages     []Message
	Tools        []ToolSpec
	SystemPrompt string
	Temperature  float64
	MaxTokens    int
}

type Streamer interface {
	Stream(ctx context.Context, req Request) (<-chan Chunk, error)
	Name() string
}
