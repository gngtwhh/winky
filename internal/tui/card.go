package tui

import (
	"fmt"
	"strings"
	"time"
)

type cardKind uint8

const (
	cardUser cardKind = iota
	cardReasoning
	cardSummary
	cardAssistant
	cardTool
	cardError
	cardNotice
)

// card is one history entry. Cards are appended once and never reordered.
type card struct {
	Kind     cardKind
	ID       string // block id: item_id (text blocks) or call_id (tool blocks)
	Title    string // tool name, for cardTool
	Text     string
	Output   string // tool result output, for cardTool
	IsError  bool   // tool result was an error, for cardTool
	At       time.Time
	Duration time.Duration // assistant card: turn wall time; tool card: execution time
	Live     bool          // still being streamed into
	Expanded bool          // collapsible cards only (reasoning / summary)
}

// cardLine is one rendered line; label marks the card's label line.
type cardLine struct {
	text  string
	label bool
}

// hint is one clickable viewport line bound to a history card.
type hint struct {
	line int
	card int
}

// collapsible reports whether the user can fold the card.
func (k cardKind) collapsible() bool {
	return k == cardReasoning || k == cardSummary
}

func (c card) labelText() string {
	head := c.Kind.label()
	switch c.Kind {
	case cardReasoning, cardSummary:
		marker := "▸"
		if c.Live || c.Expanded {
			marker = "▾"
		}
		head = fmt.Sprintf("%s %s", marker, head)
	case cardUser:
		if !c.At.IsZero() {
			head = fmt.Sprintf("%s · %s", head, c.At.Format("15:04:05"))
		}
	case cardAssistant:
		if d := c.Duration.Round(time.Millisecond); d > 0 {
			head = fmt.Sprintf("%s · %s", head, d)
		}
	case cardTool:
		if c.Title != "" {
			head = fmt.Sprintf("%s · %s", head, c.Title)
		}
		if c.Output != "" {
			if c.IsError {
				head += " ✗"
			} else {
				head += " ✓"
			}
		}
		if d := c.Duration.Round(time.Millisecond); d > 0 {
			head = fmt.Sprintf("%s · %s", head, d)
		}
	}
	return head
}

func (k cardKind) label() string {
	switch k {
	case cardUser:
		return "you"
	case cardReasoning:
		return "reasoning"
	case cardSummary:
		return "summary"
	case cardAssistant:
		return "winky"
	case cardTool:
		return "tool"
	case cardError:
		return "error"
	case cardNotice:
		return "notice"
	default:
		return "?"
	}
}

// lines renders the card into labelled, width-wrapped lines. Styling is applied
// by the caller, so a card never emits a half-open escape sequence. The last
// element is a blank separator that rebuild drops.
func (c card) lines(width int) []cardLine {
	out := make([]cardLine, 0, 8)
	out = append(out, cardLine{text: c.labelText(), label: true})

	body := c.Text
	if c.Kind.collapsible() && !c.Live && !c.Expanded {
		body = collapseHint(c.Kind, c.Text)
	}
	// The body is indented by two columns, so wrap to width-2.
	bodyWidth := width - 2
	if bodyWidth < 1 {
		bodyWidth = 1
	}
	bodyLines := wrapText(body, bodyWidth)
	if len(bodyLines) == 0 {
		bodyLines = []string{""}
	}
	for _, l := range bodyLines {
		out = append(out, cardLine{text: "  " + l})
	}

	if c.Kind == cardTool && c.Output != "" {
		out = append(out, cardLine{text: "  ── output ──"})
		for _, l := range wrapText(c.Output, bodyWidth) {
			out = append(out, cardLine{text: "  " + l})
		}
	}

	return append(out, cardLine{text: ""})
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

func collapseHint(k cardKind, text string) string {
	rows := 0
	for _, l := range splitLines(text) {
		if strings.TrimSpace(l) != "" {
			rows++
		}
	}
	label := k.label()
	if rows == 0 {
		return fmt.Sprintf("(%s folded)", label)
	}
	return fmt.Sprintf("(%s folded · %d lines · Click to expand)", label, rows)
}

// rebuild re-renders every card into the viewport and records which content line
// carries a clickable collapsible label.
func (m *model) rebuild() {
	var (
		lines   []string
		hintIdx []hint
	)
	for i, c := range m.history {
		if i > 0 && len(lines) > 0 {
			lines = append(lines, "") // one blank line between cards
		}
		for _, l := range c.lines(m.vp.Width()) {
			if l.label {
				lines = append(lines, m.style.head(c.Kind).Render(l.text))
				if c.Kind.collapsible() {
					hintIdx = append(hintIdx, hint{line: len(lines) - 1, card: i})
				}
			} else {
				lines = append(lines, m.style.body(c.Kind).Render(l.text))
			}
		}
		lines = lines[:len(lines)-1] // drop the trailing separator
	}

	m.hints = hintIdx
	m.vp.SetContent(strings.Join(lines, "\n"))
	m.dirty = false

	if m.streaming && m.follow {
		m.vp.GotoBottom()
	}
}

func (m *model) appendCard(c card) {
	m.history = append(m.history, c)
	m.rebuild()
}

// lastCard returns the trailing card of the given kind, live or not.
func (m *model) lastCard(k cardKind) *card {
	for i := len(m.history) - 1; i >= 0; i-- {
		if m.history[i].Kind == k {
			return &m.history[i]
		}
	}
	return nil
}

// markCancelled abandons the current turn: its late events and its command
// result are both discarded, and the view returns to idle immediately.
func (m *model) markCancelled() {
	if m.cancelled == nil {
		m.cancelled = make(map[int]bool)
	}
	m.cancelled[m.turn] = true
	m.streaming = false
	m.dirty = false
}

// freezeLiveCards closes out any card still marked live, which happens when a
// turn ends without a TurnEnd or TurnFailed event.
func (m *model) freezeLiveCards() {
	for i := range m.history {
		if !m.history[i].Live {
			continue
		}
		m.history[i].Live = false
		if m.history[i].Kind.collapsible() {
			m.history[i].Expanded = m.expandReasoning
		}
	}
}

// toggleHint flips the collapsible card owning the given content line.
func (m *model) toggleHint(line int) bool {
	for _, h := range m.hints {
		if h.line != line {
			continue
		}
		if h.card < 0 || h.card >= len(m.history) {
			return false
		}
		if !m.history[h.card].Kind.collapsible() {
			return false
		}
		m.history[h.card].Expanded = !m.history[h.card].Expanded
		return true
	}
	return false
}

// toggleAllReasoning expands or collapses every collapsible card, reporting the
// state after the toggle.
func (m *model) toggleAllReasoning() bool {
	expand := false
	for _, c := range m.history {
		if c.Kind.collapsible() && !c.Expanded {
			expand = true
			break
		}
	}
	for i := range m.history {
		if m.history[i].Kind.collapsible() {
			m.history[i].Expanded = expand
		}
	}
	return expand
}
