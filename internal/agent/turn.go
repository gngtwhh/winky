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

// turnDraft accumulates streamed fragments so an interrupted response can be
// persisted. The terminal Response stays authoritative on success.
type turnDraft struct {
	blocks []draftBlock
	index  map[draftKey]int
}

type draftBlock struct {
	block    Block
	complete bool
}

type draftKey struct {
	kind BlockKind
	id   string
}

func newTurnDraft() *turnDraft {
	return &turnDraft{index: make(map[draftKey]int)}
}

// text appends a text delta to the block named by kind and id.
func (d *turnDraft) text(kind BlockKind, id, text string) {
	if text == "" {
		return
	}
	key := draftKey{kind: kind, id: id}
	if i, ok := d.index[key]; ok {
		d.blocks[i].block.Text += text
		return
	}
	d.index[key] = len(d.blocks)
	d.blocks = append(d.blocks, draftBlock{
		block:    Block{Kind: kind, ID: id, Text: text},
		complete: true,
	})
}

// startTool begins a tool call; it stays incomplete until endTool so truncated
// arguments never reach the request.
func (d *turnDraft) startTool(id, name string) {
	if id == "" {
		return
	}
	key := draftKey{kind: BlockToolCall, id: id}
	if _, ok := d.index[key]; ok {
		return
	}
	d.index[key] = len(d.blocks)
	d.blocks = append(d.blocks, draftBlock{
		block: Block{Kind: BlockToolCall, ID: id, Name: name},
	})
}

func (d *turnDraft) toolArgs(id, delta string) {
	if i, ok := d.index[draftKey{kind: BlockToolCall, id: id}]; ok {
		d.blocks[i].block.Arguments += delta
	}
}

func (d *turnDraft) endTool(id, args string) {
	if id == "" {
		return
	}
	key := draftKey{kind: BlockToolCall, id: id}
	i, ok := d.index[key]
	if !ok {
		d.index[key] = len(d.blocks)
		d.blocks = append(d.blocks, draftBlock{
			block: Block{Kind: BlockToolCall, ID: id},
		})
		i = len(d.blocks) - 1
	}
	if args != "" {
		d.blocks[i].block.Arguments = args
	}
	d.blocks[i].complete = true
}

// Turn returns the blocks a cancelled or failed stream produced, dropping
// incomplete tool calls whose arguments are not valid JSON.
func (d *turnDraft) Turn() Turn {
	out := make([]Block, 0, len(d.blocks))
	for _, b := range d.blocks {
		if b.block.Kind == BlockToolCall {
			if !b.complete {
				continue
			}
		} else if b.block.Text == "" {
			continue
		}
		out = append(out, b.block)
	}
	return Turn{Blocks: out}
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
