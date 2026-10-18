package tool

import (
	"fmt"
	"unicode/utf8"
)

// DefaultMaxOutputBytes caps what a single tool call may hand back to the model.
// Tool output becomes conversation state: it is re-sent on every later request,
// so an unbounded file dump or directory listing would burn the context window
// for the rest of the session.
const DefaultMaxOutputBytes = 32 << 10

// Truncate shortens s to at most max bytes, keeping the head and the tail and
// marking the elided middle. max <= 0 disables truncation. It never splits a
// multi-byte rune, so the result is always valid UTF-8.
//
// Head and tail are kept rather than just the head: compilers, test output and
// file dumps usually put the conclusion (the error, the exit status, the closing
// bracket) at the end.
func Truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}

	// Budget the marker with the widest byte count the elision could report, so
	// the result can never exceed max.
	markerLen := len(fmt.Sprintf("\n... [%d bytes elided] ...\n", len(s)))
	budget := max - markerLen
	if budget <= 0 {
		// Pathologically small limit: a bare head cut is the only option.
		return cutAtRune(s, max)
	}

	head := budget * 2 / 3
	tail := budget - head
	headEnd := runeFloor(s, head)
	tailStart := runeCeil(s, len(s)-tail)
	if tailStart < headEnd {
		tailStart = headEnd
	}

	elided := tailStart - headEnd
	return s[:headEnd] + fmt.Sprintf("\n... [%d bytes elided] ...\n", elided) + s[tailStart:]
}

// cutAtRune returns the longest prefix of s that fits in max bytes without
// splitting a rune.
func cutAtRune(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	return s[:runeFloor(s, max)]
}

// runeFloor returns the largest index <= i that starts a rune.
func runeFloor(s string, i int) int {
	if i > len(s) {
		i = len(s)
	}
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// runeCeil returns the smallest index >= i that starts a rune.
func runeCeil(s string, i int) int {
	if i < 0 {
		i = 0
	}
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return i
}
