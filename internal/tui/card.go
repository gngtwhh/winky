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
	Budget   time.Duration // tool card: how long the call was allowed to run
	Live     bool          // still in progress: streaming into, or a running tool
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
	}
	return head
}

// timing is the right-aligned suffix of a tool card's label: how long the call
// took, or has been running, and how long it was allowed to take. A call that
// never ran has neither.
func (c card) timing() string {
	if c.Kind != cardTool {
		return ""
	}
	elapsed := c.Duration
	if c.Live && !c.At.IsZero() {
		// Still running: the viewport is re-rendered on every tick, so this
		// reads as a stopwatch without any stored state.
		elapsed = time.Since(c.At)
	}
	if elapsed <= 0 && c.Budget <= 0 {
		return ""
	}
	limit := "-"
	if c.Budget > 0 {
		limit = shortDuration(c.Budget)
	}
	return shortDuration(elapsed) + " / " + limit
}

// shortDuration formats a duration for a single-line label: "312µs", "847ms",
// "1.2s", "1m30s", "10m", "1.0h".
func shortDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "0s"
	case d < time.Millisecond:
		return d.Round(time.Microsecond).String()
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	case d < time.Hour:
		minutes := int(d / time.Minute)
		if seconds := int(d/time.Second) % 60; seconds != 0 {
			return fmt.Sprintf("%dm%02ds", minutes, seconds)
		}
		return fmt.Sprintf("%dm", minutes)
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%.1fh", d.Hours())
	}
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
	label := c.labelText()
	if timing := c.timing(); timing != "" {
		// Right-aligned against the card width, so the numbers line up down the
		// scrollback instead of trailing names of different lengths.
		label = pair(label, timing, width)
	}
	out = append(out, cardLine{text: label, label: true})

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
