package agent

import (
	"strings"

	"winky/internal/llm"
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

// Turn is the assembled result of one model response, in generation order.
// Blocks may interleave reasoning, summary, message and tool-call blocks;
// consumers must not assume a fixed shape.
type Turn struct {
	Blocks []Block
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

// turnFromResponse converts the terminal response into a Turn; nil yields an
// empty Turn.
func turnFromResponse(resp *llm.Response) Turn {
	if resp == nil {
		return Turn{}
	}
	var blocks []Block
	for _, item := range resp.Output {
		blocks = append(blocks, blocksFromItem(item)...)
	}
	return Turn{Blocks: blocks}
}

// blocksFromItem flattens one output item into blocks. A reasoning item yields
// both a reasoning and a summary block when both are present.
func blocksFromItem(item llm.OutputItem) []Block {
	switch item.Type {
	case llm.ItemReasoning:
		var out []Block
		if text := joinParts(item.Content); text != "" {
			out = append(out, Block{Kind: BlockReasoning, ID: item.ID, Text: text})
		}
		if text := joinParts(item.Summary); text != "" {
			out = append(out, Block{Kind: BlockReasoningSummary, ID: item.ID, Text: text})
		}
		return out

	case llm.ItemMessage:
		return []Block{{
			Kind:  BlockMessage,
			ID:    item.ID,
			Phase: item.Phase,
			Text:  joinParts(item.Content),
		}}

	case llm.ItemFunctionCall:
		return []Block{{
			Kind:      BlockToolCall,
			ID:        item.CallID,
			Name:      item.Name,
			Arguments: item.Arguments,
		}}
	}

	// Unknown item type: surface its text parts as a message block.
	return []Block{{
		Kind: BlockMessage,
		ID:   item.ID,
		Text: joinParts(item.Content),
	}}
}

func joinParts(parts []llm.ContentPart) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}
