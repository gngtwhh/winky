package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"winky/internal/agent"
)

func TestWrapText(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		width int
		want  []string
	}{
		{name: "short line unchanged", in: "hello", width: 10, want: []string{"hello"}},
		{name: "wraps at spaces", in: "hello world this is a test", width: 10, want: []string{"hello", "world this", "is a test"}},
		{name: "long word hard breaks", in: "abcdefghij", width: 5, want: []string{"abcde", "fghij"}},
		{name: "preserves hard newlines", in: "one\ntwo three", width: 5, want: []string{"one", "two", "three"}},
		{name: "preserves blank lines", in: "a\n\nb", width: 5, want: []string{"a", "", "b"}},
		{name: "trailing newline trimmed", in: "a\n", width: 5, want: []string{"a"}},
		{name: "preserves leading indentation", in: "    indented", width: 20, want: []string{"    indented"}},
		{name: "cjk wide runes wrap by width", in: "你好世界", width: 4, want: []string{"你好", "世界"}},
		{name: "empty input", in: "", width: 5, want: nil},
		{name: "exact fit", in: "abcde", width: 5, want: []string{"abcde"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapText(tc.in, tc.width)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("wrapText(%q, %d) = %q, want %q", tc.in, tc.width, got, tc.want)
			}
			for _, l := range got {
				if w := displayWidth(l); w > tc.width {
					t.Fatalf("line %q width %d exceeds %d", l, w, tc.width)
				}
			}
		})
	}
}

// A long single-line assistant reply must be wrapped to the viewport width so
// nothing is clipped and the user never has to scroll horizontally.
func TestLongOutputWrapsToViewportWidth(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	long := strings.Repeat("word ", 50) // ~250 columns, no newline

	m = feed(t, m,
		AgentEvent{Event: agent.TurnStart{Timestamp: time.Now()}},
		AgentEvent{Event: agent.OutputDelta{Text: long}},
		AgentEvent{Event: agent.TurnEnd{Reasoning: "", Output: long}},
	)

	width := m.vp.Width()
	content := m.vp.GetContent()
	lines := strings.Split(content, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected the long line to wrap into several lines, got %d: %q", len(lines), content)
	}
	for i, line := range lines {
		if w := displayWidth(line); w > width {
			t.Fatalf("line %d width %d exceeds viewport width %d: %q", i, w, width, line)
		}
	}
}

// startPump must not panic when the TUI is run with a nil agent.
func TestStartPumpNilAgent(t *testing.T) {
	done := make(chan struct{})
	ch := make(chan AgentEvent)
	startPump(t.Context(), nil, ch, done)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pump did not stop for a nil agent")
	}
}
