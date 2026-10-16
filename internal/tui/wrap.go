package tui

import "strings"

// wrapText wraps plain text into lines of at most width terminal columns.
// Hard newlines are preserved, words are broken at spaces, and words longer
// than the width are hard-broken mid-word. Leading indentation of a hard line
// is preserved; wrapped continuations are not re-indented.
func wrapText(s string, width int) []string {
	if width <= 0 {
		return splitLines(s)
	}
	trimmed := strings.TrimRight(s, "\n")
	if trimmed == "" {
		return nil
	}
	var out []string
	for _, h := range strings.Split(trimmed, "\n") {
		out = append(out, wrapLine(h, width)...)
	}
	return out
}

// wrapLine wraps one hard line (no '\n') to at most width columns.
func wrapLine(s string, width int) []string {
	if s == "" || width <= 0 {
		return []string{s}
	}

	var (
		lines   []string
		cur     []rune
		curW    int
		breakAt = -1 // cur[:breakAt] is the current line; the rest starts the next
		hasWord bool // cur contains a non-space rune
	)

	flush := func() {
		for len(cur) > 0 && cur[len(cur)-1] == ' ' {
			cur = cur[:len(cur)-1]
		}
		if len(cur) > 0 || len(lines) == 0 {
			lines = append(lines, string(cur))
		}
		cur = nil
		curW = 0
		breakAt = -1
		hasWord = false
	}

	for _, r := range s {
		rw := runeWidth(r)
		if r == ' ' {
			if !hasWord {
				// Leading indentation: keep it, but it is not a break point.
				cur = append(cur, r)
				curW += rw
				continue
			}
			if curW+rw <= width {
				cur = append(cur, r)
				curW += rw
				breakAt = len(cur) - 1 // break at this space
			} else {
				breakAt = len(cur) // the line is full; break before the next word
			}
			continue
		}

		if curW+rw <= width {
			cur = append(cur, r)
			curW += rw
			hasWord = true
			continue
		}

		// r overflows the current line.
		if breakAt >= 0 {
			line := strings.TrimRight(string(cur[:breakAt]), " ")
			if line != "" {
				lines = append(lines, line)
			}
			rest := cur[breakAt:]
			for len(rest) > 0 && rest[0] == ' ' {
				rest = rest[1:]
			}
			cur = rest
			curW = runeWidths(cur)
			hasWord = len(cur) > 0
			breakAt = -1
			if curW+rw > width {
				// The remainder is itself wider than the line: hard-break it.
				if len(cur) > 0 {
					lines = append(lines, string(cur))
				}
				cur = nil
				curW = 0
				hasWord = false
			}
			cur = append(cur, r)
			curW += rw
			hasWord = true
			continue
		}

		// No break point: hard-break the current line.
		if len(cur) > 0 {
			lines = append(lines, string(cur))
		}
		cur = nil
		curW = 0
		hasWord = false
		cur = append(cur, r)
		curW += rw
		hasWord = true
	}

	flush()
	return lines
}

// runeWidths returns the total number of columns a rune slice occupies.
func runeWidths(rs []rune) int {
	w := 0
	for _, r := range rs {
		w += runeWidth(r)
	}
	return w
}
