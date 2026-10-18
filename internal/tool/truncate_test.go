package tool

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateLeavesShortTextAlone(t *testing.T) {
	const s = "short"
	if got := Truncate(s, 100); got != s {
		t.Fatalf("Truncate = %q, want %q", got, s)
	}
	if got := Truncate(s, 0); got != s {
		t.Fatalf("Truncate(max=0) = %q, want the input unchanged", got)
	}
	if got := Truncate(s, -1); got != s {
		t.Fatalf("Truncate(max=-1) = %q, want the input unchanged", got)
	}
}

// Head and tail both survive: the useful part of compiler or test output is
// usually the end, and the identity of the dump is at the start.
func TestTruncateKeepsHeadAndTail(t *testing.T) {
	const (
		max    = 80
		source = "HEAD" + "middle-middle-middle-middle-middle-middle-middle-" +
			"middle-middle-middle-middle-middle-middle-middle-" + "TAIL"
	)
	got := Truncate(source, max)

	if len(got) > max {
		t.Fatalf("len(Truncate) = %d, want <= %d", len(got), max)
	}
	if !strings.HasPrefix(got, "HEAD") {
		t.Fatalf("Truncate = %q, want the head preserved", got)
	}
	if !strings.HasSuffix(got, "TAIL") {
		t.Fatalf("Truncate = %q, want the tail preserved", got)
	}
	if !strings.Contains(got, "bytes elided") {
		t.Fatalf("Truncate = %q, want an elision marker", got)
	}
}

// Truncation must not split a rune: tool output goes back to the model as JSON
// and invalid UTF-8 would be mangled or rejected by the provider.
func TestTruncateIsRuneSafe(t *testing.T) {
	source := strings.Repeat("中", 100) // 3 bytes per rune
	for _, max := range []int{1, 2, 3, 4, 7, 31, 32, 33, 64, 100, 299} {
		got := Truncate(source, max)
		if !utf8.ValidString(got) {
			t.Fatalf("Truncate(max=%d) produced invalid UTF-8: %q", max, got)
		}
		if len(got) > max {
			t.Fatalf("Truncate(max=%d) = %d bytes, want <= %d", max, len(got), max)
		}
	}
}

func TestTruncateTinyLimit(t *testing.T) {
	got := Truncate("hello world", 5)
	if got != "hello" {
		t.Fatalf("Truncate(..., 5) = %q, want %q", got, "hello")
	}
}

func TestTruncateReportsElidedBytes(t *testing.T) {
	source := strings.Repeat("a", 1000)
	got := Truncate(source, 200)
	if !strings.Contains(got, "bytes elided") {
		t.Fatalf("Truncate = %q, want the elision count", got)
	}
	if removed := len(source) - len(got); removed <= 0 {
		t.Fatalf("Truncate removed %d bytes, want a positive count", removed)
	}
}
