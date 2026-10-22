package agent

import (
	"fmt"

	"winky/internal/llm"
	"winky/internal/transcript"
)

// historyProjection materializes the request history from transcript events.
// The transcript is the source of truth; this type is only an in-memory view
// used to build llm.Request.Messages.
type historyProjection struct {
	messages []llm.Message
	pending  map[string]string // tool call ID -> tool name
	lastSeq  int64
}

func newHistoryProjection() *historyProjection {
	return &historyProjection{pending: make(map[string]string)}
}

// Apply folds one transcript event into the projection. Reasoning and turn/step
// marker events do not become request messages; they exist for audit and
// grouping.
func (h *historyProjection) Apply(ev transcript.Event) error {
	if ev == nil {
		return fmt.Errorf("history: nil transcript event")
	}
	if seq := ev.Seq(); seq > 0 {
		if seq <= h.lastSeq {
			return fmt.Errorf("history: non-monotonic transcript sequence %d after %d", seq, h.lastSeq)
		}
		if h.lastSeq > 0 && seq != h.lastSeq+1 {
			return fmt.Errorf("history: transcript sequence gap: got %d after %d", seq, h.lastSeq)
		}
		h.lastSeq = seq
	}

	switch e := ev.(type) {
	case transcript.TurnStart:
		h.pending = make(map[string]string)

	case transcript.UserMessage:
		h.messages = append(h.messages, llm.Message{
			Role:    llm.RoleUser,
			Content: e.Content,
		})

	case transcript.StepStart:
		h.pending = make(map[string]string)

	case transcript.AssistantOutputMessage:
		if e.Content == "" && len(e.ToolCalls) == 0 {
			return nil
		}
		msg := llm.Message{Role: llm.RoleAssistant, Content: e.Content}
		for _, tc := range e.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{
				ID:        tc.CallID,
				Name:      tc.Name,
				Arguments: tc.Arguments,
			})
			h.pending[tc.CallID] = tc.Name
		}
		h.messages = append(h.messages, msg)

	case transcript.AssistantReasoningMessage, transcript.AssistantReasoningSummary:
		// Reasoning is persisted for auditing but is not part of the
		// provider-facing request history.

	case transcript.ToolResult:
		name, ok := h.pending[e.CallID]
		if !ok {
			// An orphan result cannot be paired with a call, so it must not
			// enter the request history.
			return nil
		}
		h.messages = append(h.messages, llm.Message{
			Role:       llm.RoleToolOutput,
			Name:       name,
			ToolCallID: e.CallID,
			Content:    e.Output,
		})
		delete(h.pending, e.CallID)

	case transcript.StepEnd:
		// Any call left pending here is incomplete; Messages drops it.
		h.pending = make(map[string]string)

	case transcript.TurnEnd, transcript.TurnFailed:
		h.pending = make(map[string]string)

	case transcript.CompactionSummary:
		// Compaction projection is intentionally deferred until the compaction
		// feature writes these events.

	default:
		return fmt.Errorf("history: unsupported transcript event %q", ev.Type())
	}
	return nil
}

// Messages returns the request-safe view of the projection. It drops incomplete
// trailing tool-call groups and orphan tool results, so a failed turn can never
// leave the next request with an unanswered function call.
func (h *historyProjection) Messages() []llm.Message {
	out := make([]llm.Message, 0, len(h.messages))
	for i := 0; i < len(h.messages); {
		msg := h.messages[i]
		if msg.Role == llm.RoleToolOutput {
			// Orphan tool output; valid groups are consumed below.
			i++
			continue
		}
		if msg.Role != llm.RoleAssistant || len(msg.ToolCalls) == 0 {
			out = append(out, msg)
			i++
			continue
		}

		expected := make(map[string]bool, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			expected[tc.ID] = true
		}

		have := make(map[string]bool, len(msg.ToolCalls))
		group := make([]llm.Message, 0, len(msg.ToolCalls))
		j := i + 1
		for j < len(h.messages) && h.messages[j].Role == llm.RoleToolOutput {
			result := h.messages[j]
			if expected[result.ToolCallID] {
				have[result.ToolCallID] = true
				group = append(group, result)
			}
			j++
		}

		if len(have) == len(expected) {
			out = append(out, msg)
			out = append(out, group...)
		}
		i = j
	}
	return out
}
