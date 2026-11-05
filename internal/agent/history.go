package agent

import (
	"fmt"

	"winky/internal/llm"
	"winky/internal/transcript"
)

// historyEntry pairs one request message with the sequence of the event that
// produced it; compaction ranges address those sequences.
type historyEntry struct {
	seq int64
	msg llm.Message
}

// historyProjection materializes the request history from transcript events,
// the source of truth. Entries stay ordered by sequence: a compaction summary
// takes over the sequence of the first entry it replaces.
type historyProjection struct {
	entries []historyEntry
	// turns and steps hold the sequences compaction may cut before: a user
	// message starts a turn, a step start starts a step.
	turns   []int64
	steps   []int64
	pending map[string]string // tool call ID -> tool name
	lastSeq int64
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
		h.append(ev, llm.Message{
			Role:    llm.RoleUser,
			Content: e.Content,
		})
		if seq := ev.Seq(); seq > 0 {
			h.turns = append(h.turns, seq)
		}

	case transcript.StepStart:
		h.pending = make(map[string]string)
		if seq := ev.Seq(); seq > 0 {
			h.steps = append(h.steps, seq)
		}

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
		h.append(ev, msg)

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
		h.append(ev, llm.Message{
			Role:       llm.RoleToolOutput,
			Name:       name,
			ToolCallID: e.CallID,
			Content:    e.Output,
		})
		delete(h.pending, e.CallID)

	case transcript.StepEnd:
		// Any call still pending here has no result; Messages answers it.
		h.pending = make(map[string]string)

	case transcript.TurnEnd, transcript.TurnFailed:
		h.pending = make(map[string]string)

	case transcript.CompactionSummary:
		h.applyCompaction(e)

	default:
		return fmt.Errorf("history: unsupported transcript event %q", ev.Type())
	}
	return nil
}

// append records one message under the sequence of the event that produced it.
func (h *historyProjection) append(ev transcript.Event, msg llm.Message) {
	h.entries = append(h.entries, historyEntry{seq: ev.Seq(), msg: msg})
}

// applyCompaction replaces the entries addressed by the event with its summary.
// planCompaction always starts a range at the first entry and cuts on a turn or
// step boundary, so a range is a prefix that cannot split a tool-call group; a
// range matching no entry is ignored.
func (h *historyProjection) applyCompaction(e transcript.CompactionSummary) {
	start, end := e.ReplacedStartSeq, e.ReplacedEndSeq

	kept := 0
	for kept < len(h.entries) && h.entries[kept].seq <= end {
		kept++
	}
	if kept == 0 {
		return
	}

	next := make([]historyEntry, 0, len(h.entries)-kept+1)
	next = append(next, historyEntry{
		seq: start,
		msg: llm.Message{Role: llm.RoleSystem, Content: e.Summary},
	})
	next = append(next, h.entries[kept:]...)
	h.entries = next
	h.trimUnits(end)
}

// trimUnits drops the boundaries that the replaced range covered.
func (h *historyProjection) trimUnits(end int64) {
	h.turns = trimSeqs(h.turns, end)
	h.steps = trimSeqs(h.steps, end)
}

func trimSeqs(seqs []int64, end int64) []int64 {
	i := 0
	for i < len(seqs) && seqs[i] <= end {
		i++
	}
	if i == 0 {
		return seqs
	}
	return seqs[i:]
}

// snapshot exposes the projection for planning; the slices are read-only.
func (h *historyProjection) snapshot() (entries []historyEntry, turns, steps []int64) {
	return h.entries, h.turns, h.steps
}

// Messages returns the request-safe view of the projection. It answers tool
// calls whose result is missing and drops orphan tool results, so a request can
// never carry an unanswered function call, whatever the transcript holds.
func (h *historyProjection) Messages() []llm.Message {
	return messagesOf(h.entries)
}

func messagesOf(entries []historyEntry) []llm.Message {
	out := make([]llm.Message, 0, len(entries))
	for i := 0; i < len(entries); {
		msg := entries[i].msg
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
		for j < len(entries) && entries[j].msg.Role == llm.RoleToolOutput {
			result := entries[j].msg
			if expected[result.ToolCallID] {
				have[result.ToolCallID] = true
				group = append(group, result)
			}
			j++
		}

		// Keep the group even when a result is missing: dropping it would also
		// discard the results that did arrive.
		out = append(out, msg)
		out = append(out, group...)
		if len(have) < len(expected) {
			for _, tc := range msg.ToolCalls {
				if have[tc.ID] {
					continue
				}
				out = append(
					out, llm.Message{
						Role:       llm.RoleToolOutput,
						Name:       tc.Name,
						ToolCallID: tc.ID,
						Content:    transcript.MissingToolResult,
					},
				)
			}
		}
		i = j
	}
	return out
}
