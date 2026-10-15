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
