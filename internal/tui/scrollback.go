package tui

import "strings"

// hint is one clickable viewport line bound to a card.
type hint struct {
	line int
	card int
}

// rebuild re-renders every card into the viewport and records which content line
// carries a clickable collapsible label.
func (m *model) rebuild() {
	var (
		lines   []string
		hintIdx []hint
	)
	for i, c := range m.cards {
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

	if m.follow {
		m.vp.GotoBottom()
	}
}

func (m *model) appendCard(c card) {
	m.cards = append(m.cards, c)
	m.rebuild()
}

// hasRunningTool reports whether a tool of the current turn is still running,
// which is when its stopwatch has to keep refreshing.
func (m *model) hasRunningTool() bool {
	for i := m.turnStartIdx; i < len(m.cards); i++ {
		if m.cards[i].Kind == cardTool && m.cards[i].Live {
			return true
		}
	}
	return false
}

// lastCard returns the trailing card of the given kind, live or not.
func (m *model) lastCard(k cardKind) *card {
	for i := len(m.cards) - 1; i >= 0; i-- {
		if m.cards[i].Kind == k {
			return &m.cards[i]
		}
	}
	return nil
}

// freezeLiveCards closes out any card still marked live, which happens when a
// turn ends without a TurnEnd or TurnFailed event.
func (m *model) freezeLiveCards() {
	for i := range m.cards {
		if !m.cards[i].Live {
			continue
		}
		m.cards[i].Live = false
		if m.cards[i].Kind.collapsible() {
			m.cards[i].Expanded = m.expandReasoning
		}
	}
}

// toggleHint flips the collapsible card owning the given content line.
func (m *model) toggleHint(line int) bool {
	for _, h := range m.hints {
		if h.line != line {
			continue
		}
		if h.card < 0 || h.card >= len(m.cards) {
			return false
		}
		if !m.cards[h.card].Kind.collapsible() {
			return false
		}
		m.cards[h.card].Expanded = !m.cards[h.card].Expanded
		return true
	}
	return false
}

// toggleAllReasoning expands or collapses every collapsible card, reporting the
// state after the toggle.
func (m *model) toggleAllReasoning() bool {
	expand := false
	for _, c := range m.cards {
		if c.Kind.collapsible() && !c.Expanded {
			expand = true
			break
		}
	}
	for i := range m.cards {
		if m.cards[i].Kind.collapsible() {
			m.cards[i].Expanded = expand
		}
	}
	return expand
}
