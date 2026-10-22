package transcript

import "time"

// Event is an event generated during an agent's conversation.
// It can be the start of the conversation, the end of the conversation,
// text output, tool invocation, result return, and so on.
type Event interface {
	Seq() int64
	Type() string
}

type baseEvent struct {
	seq int64
}

func (b baseEvent) Seq() int64 { return b.seq }

type UserMessage struct {
	baseEvent
	Content string `json:"content"`
}

func (e UserMessage) Type() string { return "user_message" }

type AssistantOutputMessage struct {
	baseEvent
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

func (e AssistantOutputMessage) Type() string { return "assistant_output_msg" }

type AssistantReasoningMessage struct {
	baseEvent
	Content string `json:"content"`
}

func (e AssistantReasoningMessage) Type() string { return "assistant_reasoning_msg" }

// AssistantReasoningSummary is the concise summary of reasoning
// (summary_text), distinct from the full reasoning text.
type AssistantReasoningSummary struct {
	baseEvent
	Content string `json:"content"`
}

func (e AssistantReasoningSummary) Type() string { return "assistant_reasoning_summary" }

type ToolCall struct {
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolResult struct {
	baseEvent
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

func (e ToolResult) Type() string { return "tool_result" }

// CompactionSummary replaces the history prefix whose event sequences fall in
// [ReplacedStartSeq, ReplacedEndSeq] with Summary. The summary takes over
// ReplacedStartSeq, so entries stay ordered and a later range covers it again.
type CompactionSummary struct {
	baseEvent
	Summary          string    `json:"summary"`
	ReplacedStartSeq int64     `json:"replaced_start_seq"`
	ReplacedEndSeq   int64     `json:"replaced_end_seq"`
	Timestamp        time.Time `json:"timestamp"`
	Kind             string    `json:"kind,omitempty"` // auto_turn, auto_step
	Model            string    `json:"model,omitempty"`
	TokensBefore     int       `json:"tokens_before,omitempty"`
	TokensAfter      int       `json:"tokens_after,omitempty"`
}

func (e CompactionSummary) Type() string { return "compaction_summary" }

type TurnStart struct {
	baseEvent
	Timestamp time.Time `json:"timestamp"`
}

func (e TurnStart) Type() string { return "turn_start" }

type TurnEnd struct {
	baseEvent
	Timestamp time.Time `json:"timestamp"`
}

func (e TurnEnd) Type() string { return "turn_end" }

// TurnFailed marks a turn that ended with an error. It closes the turn in the
// transcript so a partial turn is not left open.
type TurnFailed struct {
	baseEvent
	Timestamp time.Time `json:"timestamp"`
	Error     string    `json:"error"`
}

func (e TurnFailed) Type() string { return "turn_failed" }

type StepStart struct {
	baseEvent
	Timestamp time.Time `json:"timestamp"`
}

func (e StepStart) Type() string { return "step_start" }

type StepEnd struct {
	baseEvent
	Timestamp time.Time `json:"timestamp"`
}

func (e StepEnd) Type() string { return "step_end" }
