package agent

import (
	"fmt"

	"winky/internal/llm"
	"winky/internal/transcript"
)

// Model-facing closing messages. Every turn with a user message ends with an
// assistant message, so user messages are never adjacent.
const (
	abortedMarker     = "[the previous request was aborted before completion; it is closed]"
	interruptedMarker = "[the previous request was interrupted before completion]"
	noOutputMarker    = "[the request completed without an assistant response]"
)

func failedMarker(err string) string {
	if err == "" {
		return "[the previous request failed before completion]"
	}
	return "[the previous request failed before completion: " + err + "]"
}

// historyEntry pairs one request message with the sequence of the event that
// produced it; compaction ranges address those sequences.
type historyEntry struct {
	seq int64
	msg llm.Message
}

// historyProjection materializes the request history from transcript events,
// the source of truth. Entries stay ordered by sequence: a compaction summary
// takes over the sequence of the first entry it replaces.
//
// A projection is produced only by Project: the live agent and a replay from
// disk therefore run the same fold over the same events and cannot diverge.
type historyProjection struct {
	entries []historyEntry
	// turns and steps hold the sequences compaction may cut before: a user
	// message starts a turn, a step start starts a step.
	turns   []int64
	steps   []int64
	pending map[string]string // tool call ID -> tool name
	lastSeq int64

	// turnOpen is set by a user message and cleared by a terminal event or an
	// interruption repair. turnHasAssistant tracks assistant output in the turn.
	turnOpen         bool
	turnUserSeq      int64
	turnHasAssistant bool
}

func newHistoryProjection() *historyProjection {
	return &historyProjection{pending: make(map[string]string)}
}

// Project folds the whole event log into its request view. It is the only
// producer of a projection; a projection handed out by anything else would be
// a second source of truth.
func Project(events []transcript.Event) (*historyProjection, error) {
	h := newHistoryProjection()
	for _, ev := range events {
		if err := h.Apply(ev); err != nil {
			return nil, err
		}
	}
	return h, nil
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
		// A new turn with the previous one still open means it was interrupted.
		h.repairOpenTurn(ev.Seq())
		h.pending = make(map[string]string)

	case transcript.UserMessage:
		h.repairOpenTurn(ev.Seq())
		h.append(ev, llm.Message{
			Role:    llm.RoleUser,
			Content: e.Content,
		})
		if seq := ev.Seq(); seq > 0 {
			h.turns = append(h.turns, seq)
		}
		h.turnOpen = true
		h.turnUserSeq = ev.Seq()
		h.turnHasAssistant = false

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
		}
		h.appendAssistant(ev.Seq(), msg)
		h.turnHasAssistant = true

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

	case transcript.TurnEnd:
		if h.turnOpen && !h.turnHasAssistant {
			h.appendAssistant(ev.Seq(), llm.Message{Role: llm.RoleAssistant, Content: noOutputMarker})
		}
		h.pending = make(map[string]string)
		h.closeTurn()

	case transcript.TurnFailed:
		if h.turnOpen {
			h.appendAssistant(ev.Seq(), llm.Message{
				Role:    llm.RoleAssistant,
				Content: failedMarker(e.Error),
			})
		}
		h.pending = make(map[string]string)
		h.closeTurn()

	case transcript.TurnAborted:
		if h.turnOpen {
			h.appendAssistant(ev.Seq(), llm.Message{
				Role:    llm.RoleAssistant,
				Content: abortedMarker,
			})
		}
		h.pending = make(map[string]string)
		h.closeTurn()

	case transcript.CompactionSummary:
		h.applyCompaction(e)

	default:
		return fmt.Errorf("history: unsupported transcript event %q", ev.Type())
	}
	return nil
}

// repairOpenTurn closes a turn that never received a terminal event. The marker
// takes the triggering event's seq, keeping it ordered immediately before it.
func (h *historyProjection) repairOpenTurn(seq int64) {
	if !h.turnOpen {
		return
	}
	h.appendAssistant(seq, llm.Message{
		Role:    llm.RoleAssistant,
		Content: interruptedMarker,
	})
	h.pending = make(map[string]string)
	h.closeTurn()
}

// closeTurn clears turn state only. applyCompaction uses it too, and the tool
// group may still be live after the replaced prefix, so pending is untouched.
func (h *historyProjection) closeTurn() {
	h.turnOpen = false
	h.turnUserSeq = 0
	h.turnHasAssistant = false
}

// append records one message under the sequence of the event that produced it.
func (h *historyProjection) append(ev transcript.Event, msg llm.Message) {
	h.appendSeq(ev.Seq(), msg)
}

func (h *historyProjection) appendSeq(seq int64, msg llm.Message) {
	h.entries = append(h.entries, historyEntry{seq: seq, msg: msg})
}

// appendAssistant appends an assistant message, merging into a preceding
// text-only assistant to avoid adjacency. Tool calls are always registered.
func (h *historyProjection) appendAssistant(seq int64, msg llm.Message) {
	for _, tc := range msg.ToolCalls {
		h.pending[tc.ID] = tc.Name
	}
	if n := len(h.entries); n > 0 {
		last := &h.entries[n-1]
		if last.msg.Role == llm.RoleAssistant && len(last.msg.ToolCalls) == 0 {
			if msg.Content != "" {
				if last.msg.Content != "" {
					last.msg.Content += "\n\n"
				}
				last.msg.Content += msg.Content
			}
			last.msg.ToolCalls = append(last.msg.ToolCalls, msg.ToolCalls...)
			return
		}
	}
	h.appendSeq(seq, msg)
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

	// The summary swallowed the open turn's user message: nothing left to close.
	if h.turnOpen && h.turnUserSeq > 0 && h.turnUserSeq <= end {
		h.closeTurn()
	}
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

// Messages returns the request-safe view: missing tool results answered, orphan
// results dropped, adjacent assistants merged.
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
	return mergeAdjacentAssistants(out)
}

// mergeAdjacentAssistants is the last compatibility gate. Tool-call groups are
// followed by results, so only text-only assistants can be adjacent here.
func mergeAdjacentAssistants(msgs []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(msgs))
	for _, msg := range msgs {
		if n := len(out); n > 0 {
			last := &out[n-1]
			if last.Role == llm.RoleAssistant && msg.Role == llm.RoleAssistant &&
				len(last.ToolCalls) == 0 {
				if msg.Content != "" {
					if last.Content != "" {
						last.Content += "\n\n"
					}
					last.Content += msg.Content
				}
				last.ToolCalls = append(last.ToolCalls, msg.ToolCalls...)
				continue
			}
		}
		out = append(out, msg)
	}
	return out
}
