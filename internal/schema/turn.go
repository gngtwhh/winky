package schema

import (
	"strings"
	"time"
)

// BlockKind discriminates the kinds of blocks in a Turn.
type BlockKind string

const (
	// BlockReasoning is a span of the model's reasoning (chain of thought).
	BlockReasoning BlockKind = "reasoning"
	// BlockReasoningSummary is the concise summary of reasoning (summary_text).
	BlockReasoningSummary BlockKind = "reasoning_summary"
	// BlockMessage is an assistant message (answer, commentary, or refusal).
	BlockMessage BlockKind = "message"
	// BlockToolCall is a function tool call the model requested.
	BlockToolCall BlockKind = "tool_call"
)

// Block is one ordered fragment of a turn: a flattened llm.OutputItem with the
// protocol-only fields dropped.
type Block struct {
	Kind BlockKind
	// ID locates the block: item_id for reasoning/summary/message blocks,
	// call_id for tool-call blocks. The TUI uses it to group live deltas.
	ID string

	// Text holds reasoning, summary, or message text.
	Text string

	// Phase labels a message block: "", "commentary", or "final_answer".
	Phase string

	Name      string
	Arguments string
}

// Turn is the assembled result of one user turn: the model blocks across all
// requests plus the tool executions between them, in generation order.
type Turn struct {
	Blocks      []Block
	ToolResults []ToolResult
}

// ToolResult is the outcome of executing one tool call.
type ToolResult struct {
	CallID   string
	Name     string
	Output   string
	IsError  bool
	Duration time.Duration
}

// Reasoning returns the concatenated reasoning text.
func (t Turn) Reasoning() string {
	var b strings.Builder
	for _, blk := range t.Blocks {
		if blk.Kind == BlockReasoning {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// Summary returns the concatenated reasoning-summary text.
func (t Turn) Summary() string {
	var b strings.Builder
	for _, blk := range t.Blocks {
		if blk.Kind == BlockReasoningSummary {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// Output returns the concatenated message text.
func (t Turn) Output() string {
	var b strings.Builder
	for _, blk := range t.Blocks {
		if blk.Kind == BlockMessage {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// ToolCalls returns the tool-call blocks in generation order.
func (t Turn) ToolCalls() []Block {
	var out []Block
	for _, blk := range t.Blocks {
		if blk.Kind == BlockToolCall {
			out = append(out, blk)
		}
	}
	return out
}
