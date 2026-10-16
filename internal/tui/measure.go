package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/rivo/uniseg"
)

// displayWidth returns the number of terminal columns a styled string occupies.
func displayWidth(s string) int { return lipgloss.Width(s) }

// runeWidth returns the number of columns a single rune occupies.
func runeWidth(r rune) int {
	if r == 0 {
		return 0
	}
	return uniseg.StringWidth(string(r))
}

// truncate shortens an ANSI-styled string to at most width columns, appending
// tail when something was cut. Escape sequences are preserved and any style
// still open at the cut point is reset.
func truncate(s string, width int, tail string) string {
	if width <= 0 {
		return ""
	}
	if displayWidth(s) <= width {
		return s
	}

	budget := width - displayWidth(tail)
	if budget < 0 {
		budget = 0
	}

	var (
		b    strings.Builder
		used int
		in   bool
	)
	for _, r := range s {
		if r == 0x1b {
			in = true
		}
		if in {
			b.WriteRune(r)
			if r == 'm' {
				in = false
			}
			continue
		}
		w := runeWidth(r)
		if used+w > budget {
			break
		}
		b.WriteRune(r)
		used += w
	}

	b.WriteString(tail)
	if used < budget {
		b.WriteString(strings.Repeat(" ", budget-used))
	}
	b.WriteString("\x1b[0m")
	return b.String()
}
