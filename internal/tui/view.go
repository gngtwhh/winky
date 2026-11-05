package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// View composes the chrome around the scrollback viewport. Alt screen and mouse
// mode are per-view in Bubble Tea v2, not program options.
func (m model) View() tea.View {
	if !m.ready {
		return tea.NewView("initializing the terminal...")
	}

	body := lipgloss.JoinVertical(
		lipgloss.Left,
		m.headerView(),
		m.vp.View(),
		m.statusView(),
		m.usageView(),
		m.input.View(),
	)

	v := tea.NewView(body)
	v.AltScreen = m.altScreen
	v.MouseMode = tea.MouseModeCellMotion
	v.KeyboardEnhancements = tea.KeyboardEnhancements{ReportEventTypes: true}
	// The textarea draws its own virtual cursor; only forward a real terminal
	// cursor when the component asks for one.
	if c := m.input.Cursor(); c != nil {
		v.Cursor = c
	}
	return v
}

func (m model) headerView() string {
	label := m.label
	if label == "" {
		label = "Unconfigured model"
	}
	width := max(minViewportWidth, m.width)

	base := "winky · " + label
	var candidates []string
	if m.workDir != "" {
		candidates = []string{
			base + " · " + abbrevPath(m.workDir, m.home),
			base + " · " + tailPath(m.workDir),
			base,
		}
	} else {
		candidates = []string{base}
	}
	return m.style.Header.Render(fitToWidth(candidates, width))
}

func (m model) statusView() string {
	left := "Ready"
	hint := "enter Send · ctrl+j Newline · esc Abort · ctrl+t Think · ctrl+c Quit"

	switch {
	case m.perm != nil:
		left = fmt.Sprintf("Allow %s to %s %s? [y/n]", m.perm.req.Tool, m.perm.req.Access, m.perm.req.Target)
		hint = "y Allow · n Deny"
	case m.streaming:
		left = fmt.Sprintf("%s generating · %s", m.sp.View(), time.Since(m.started).Round(time.Second))
		hint = "esc Abort · Press a Thinking title to unfold"
	case m.err != nil:
		left = "Error"
	case m.status != "":
		left = m.status
	default:
		if m.policy != nil {
			left = "Ready · " + m.policy.Get().String()
		}
	}
	if m.turn > 0 {
		left = fmt.Sprintf("%s · Turn %d", left, m.turn)
	}
	if !m.atBottom() {
		// The user has scrolled up; there is more history below the fold.
		left = "↑ " + left
	}

	return m.style.Status.Render(pair(left, hint, max(minViewportWidth, m.width)))
}

func (m model) usageView() string {
	left := "This turn " + formatUsage(m.usage)
	if ctx := formatContextUsage(m.contextTokens, m.contextWindow); ctx != "" {
		left = ctx + " · " + left
	}
	right := "Total " + formatUsage(m.totalUsage)
	return m.style.Status.Render(pair(left, right, max(minViewportWidth, m.width)))
}

func pair(left, right string, width int) string {
	left = truncate(left, width, "…")
	lw := displayWidth(left)

	// Truncate the right side instead of dropping it, so the key hints stay
	// visible on a narrow terminal.
	if space := width - lw - 1; right != "" && space > 0 {
		right = truncate(right, space, "…")
	} else {
		right = ""
	}
	rw := displayWidth(right)

	gap := width - lw - rw
	if gap < 0 {
		gap = 0
	}
	return left + strings.Repeat(" ", gap) + right
}
