package agent

import (
	"fmt"

	"winky/internal/llm"
	"winky/internal/prompt"
	"winky/internal/transcript"
)

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

type entry struct {
	seq int64
	msg llm.Message
}

// view is the model-facing state of a session, folded out of the transcript.
// Entries stay ordered by sequence: a compaction summary takes over the sequence
// of the first entry it replaces.
type view struct {
	entries []entry
	turns   []int64
	steps   []int64
	pending map[string]string // tool call ID -> tool name
	lastSeq int64

	// prompt produces no request message, so compaction never touches it.
	prompt prompt.State

	turnOpen         bool
	turnUserSeq      int64
	turnHasAssistant bool
}

func newView() *view {
	return &view{pending: make(map[string]string)}
}

// project is the only producer of a view; a second producer would be a second
// source of truth.
func project(events []transcript.Event) (*view, error) {
	v := newView()
	for _, ev := range events {
		if err := v.apply(ev); err != nil {
			return nil, err
		}
	}
	return v, nil
}

func (v *view) apply(ev transcript.Event) error {
	if ev == nil {
		return fmt.Errorf("view: nil transcript event")
	}
	if seq := ev.Seq(); seq > 0 {
		if seq <= v.lastSeq {
			return fmt.Errorf("view: non-monotonic transcript sequence %d after %d", seq, v.lastSeq)
		}
		if v.lastSeq > 0 && seq != v.lastSeq+1 {
			return fmt.Errorf("view: transcript sequence gap: got %d after %d", seq, v.lastSeq)
		}
		v.lastSeq = seq
	}

	switch e := ev.(type) {
	case transcript.TurnStart:
		// An open turn here was interrupted.
		v.repairOpenTurn(ev.Seq())
		v.pending = make(map[string]string)

	case transcript.UserMessage:
		v.repairOpenTurn(ev.Seq())
		v.append(ev.Seq(), llm.Message{
			Role:    llm.RoleUser,
			Content: e.Content,
		})
		if seq := ev.Seq(); seq > 0 {
			v.turns = append(v.turns, seq)
		}
		v.turnOpen = true
		v.turnUserSeq = ev.Seq()
		v.turnHasAssistant = false

	case transcript.StepStart:
		v.pending = make(map[string]string)
		if seq := ev.Seq(); seq > 0 {
			v.steps = append(v.steps, seq)
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
		v.appendAssistant(ev.Seq(), msg)
		v.turnHasAssistant = true

	case transcript.AssistantReasoningMessage, transcript.AssistantReasoningSummary:
		// Persisted for auditing, never sent.

	case transcript.ToolResult:
		name, ok := v.pending[e.CallID]
		if !ok {
			return nil
		}
		v.append(ev.Seq(), llm.Message{
			Role:       llm.RoleToolOutput,
			Name:       name,
			ToolCallID: e.CallID,
			Content:    e.Output,
		})
		delete(v.pending, e.CallID)

	case transcript.StepEnd:
		// Calls still pending here have no result; messagesOf answers them.
		v.pending = make(map[string]string)

	case transcript.TurnEnd:
		if v.turnOpen && !v.turnHasAssistant {
			v.appendAssistant(ev.Seq(), llm.Message{Role: llm.RoleAssistant, Content: noOutputMarker})
		}
		v.pending = make(map[string]string)
		v.closeTurn()

	case transcript.TurnFailed:
		if v.turnOpen {
			v.appendAssistant(ev.Seq(), llm.Message{
				Role:    llm.RoleAssistant,
				Content: failedMarker(e.Error),
			})
		}
		v.pending = make(map[string]string)
		v.closeTurn()

	case transcript.TurnAborted:
		if v.turnOpen {
			v.appendAssistant(ev.Seq(), llm.Message{
				Role:    llm.RoleAssistant,
				Content: abortedMarker,
			})
		}
		v.pending = make(map[string]string)
		v.closeTurn()

	case transcript.CompactionSummary:
		v.applyCompaction(e)

	case transcript.PromptChanged:
		if err := v.applyPromptChange(e); err != nil {
			return fmt.Errorf("view: prompt change at seq %d: %w", ev.Seq(), err)
		}

	default:
		return fmt.Errorf("view: unsupported transcript event %q", ev.Type())
	}
	return nil
}

func (v *view) applyPromptChange(e transcript.PromptChanged) error {
	slot := prompt.Slot(e.Slot)
	if e.Remove {
		return v.prompt.Remove(slot, e.Name)
	}
	return v.prompt.Set(slot, e.Name, e.Text)
}

func (v *view) systemPrompt() string { return v.prompt.Render() }

// repairOpenTurn closes a turn that never received a terminal event. The marker
// takes the triggering event's seq, so it stays ordered just before it.
func (v *view) repairOpenTurn(seq int64) {
	if !v.turnOpen {
		return
	}
	v.appendAssistant(seq, llm.Message{
		Role:    llm.RoleAssistant,
		Content: interruptedMarker,
	})
	v.pending = make(map[string]string)
	v.closeTurn()
}

// closeTurn clears turn state only: the tool group may still be live after a
// compacted prefix, so pending is left alone.
func (v *view) closeTurn() {
	v.turnOpen = false
	v.turnUserSeq = 0
	v.turnHasAssistant = false
}

func (v *view) append(seq int64, msg llm.Message) {
	v.entries = append(v.entries, entry{seq: seq, msg: msg})
}

func (v *view) appendAssistant(seq int64, msg llm.Message) {
	for _, tc := range msg.ToolCalls {
		v.pending[tc.ID] = tc.Name
	}
	if n := len(v.entries); n > 0 {
		last := &v.entries[n-1]
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
	v.append(seq, msg)
}

// applyCompaction replaces the entries addressed by the event with its summary.
// planCompaction always starts at the first entry and cuts on a turn or step
// boundary, so the range is a prefix that cannot split a tool-call group. A range
// matching no entry is ignored.
func (v *view) applyCompaction(e transcript.CompactionSummary) {
	start, end := e.ReplacedStartSeq, e.ReplacedEndSeq

	kept := 0
	for kept < len(v.entries) && v.entries[kept].seq <= end {
		kept++
	}
	if kept == 0 {
		return
	}

	next := make([]entry, 0, len(v.entries)-kept+1)
	next = append(next, entry{
		seq: start,
		msg: llm.Message{Role: llm.RoleSystem, Content: e.Summary},
	})
	next = append(next, v.entries[kept:]...)
	v.entries = next
	v.trimUnits(end)

	// The summary swallowed the open turn's user message: nothing left to close.
	if v.turnOpen && v.turnUserSeq > 0 && v.turnUserSeq <= end {
		v.closeTurn()
	}
}

func (v *view) trimUnits(end int64) {
	v.turns = trimSeqs(v.turns, end)
	v.steps = trimSeqs(v.steps, end)
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

func (v *view) parts() (entries []entry, turns, steps []int64) {
	return v.entries, v.turns, v.steps
}

func (v *view) messages() []llm.Message {
	return messagesOf(v.entries)
}

func messagesOf(entries []entry) []llm.Message {
	out := make([]llm.Message, 0, len(entries))
	for i := 0; i < len(entries); {
		msg := entries[i].msg
		if msg.Role == llm.RoleToolOutput {
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

// mergeAdjacentAssistants is the last compatibility gate: a tool-call group is
// followed by its results, so only text-only assistants can be adjacent here.
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
