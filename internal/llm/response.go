package llm

// ItemType is the type discriminator of an output item in a model response.
type ItemType string

// Output item types understood by the Responses protocol. Other item types are
// preserved as-is in OutputItem.Type and their raw JSON in OutputItem.Raw.
const (
	ItemMessage      ItemType = "message"
	ItemReasoning    ItemType = "reasoning"
	ItemFunctionCall ItemType = "function_call"
)

// PartType is the type discriminator of a content part within a message or
// reasoning item.
type PartType string

// Content part types.
const (
	PartOutputText       PartType = "output_text"
	PartRefusal          PartType = "refusal"
	PartReasoningText    PartType = "reasoning_text"
	PartReasoningSummary PartType = "summary_text"
)

// ContentPart is one content part of a message or reasoning item.
type ContentPart struct {
	Type PartType
	Text string
}

// OutputItem is one output item of a response. Fields are populated according
// to Type; Raw carries the original JSON for fields not yet modelled.
//
// ID is the item_id, shared with the ItemID on live text deltas. CallID is the
// call_id, set only for function_call items, and is what the provider expects
// back in a function_call_output item.
type OutputItem struct {
	Type   ItemType
	ID     string
	Status string

	// message
	Phase string // "", "commentary", "final_answer"

	// reasoning
	EncryptedContent string

	// function_call
	CallID    string
	Name      string
	Arguments string

	// Content holds the item's parts in provider order.
	Content []ContentPart
	// Summary holds a reasoning item's summary parts, in provider order.
	Summary []ContentPart

	Raw string
}

// Response is the fully accumulated result of one model response.
//
// Output is heterogeneous and in generation order: reasoning, message and
// function_call items may interleave, so consumers must not assume a fixed
// shape.
type Response struct {
	ID     string
	Output []OutputItem
	Usage  *TokenUsage
}
