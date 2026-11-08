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

// card is one scrollback entry. Cards are appended once and never reordered.
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
