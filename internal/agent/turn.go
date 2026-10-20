package agent

import (
	"strings"

	"winky/internal/llm"
	"winky/internal/schema"
)

type (
	BlockKind  = schema.BlockKind
	Block      = schema.Block
	Turn       = schema.Turn
	ToolResult = schema.ToolResult
)

const (
	BlockReasoning        = schema.BlockReasoning
	BlockReasoningSummary = schema.BlockReasoningSummary
	BlockMessage          = schema.BlockMessage
	BlockToolCall         = schema.BlockToolCall
)

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
