package tui

import (
	"path/filepath"
	"testing"

	"winky/internal/llm"
)

func TestFormatTokens(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.0k"},
		{1234, "1.2k"},
		{1000000, "1.0M"},
		{2500000, "2.5M"},
	}
	for _, tc := range cases {
		if got := formatTokens(tc.n); got != tc.want {
			t.Fatalf("formatTokens(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestFormatUsage(t *testing.T) {
	cases := []struct {
		name string
		u    llm.TokenUsage
		want string
	}{
		{"none", llm.TokenUsage{}, "—"},
		{"full", llm.TokenUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500, CachedTokens: 500}, "↑1.0k ↓500 · Cache 50%"},
		{"no input", llm.TokenUsage{PromptTokens: 0, CompletionTokens: 10, TotalTokens: 10}, "↑0 ↓10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatUsage(tc.u); got != tc.want {
				t.Fatalf("formatUsage(%+v) = %q, want %q", tc.u, got, tc.want)
			}
		})
	}
}

func TestFitToWidth(t *testing.T) {
	cands := []string{"a long candidate", "short", "x"}
	if got := fitToWidth(cands, 5); got != "short" {
		t.Fatalf("fitToWidth(5) = %q, want %q", got, "short")
	}
	if got := fitToWidth(cands, 1); got != "x" {
		t.Fatalf("fitToWidth(1) = %q, want %q", got, "x")
	}
	if got := fitToWidth(cands, 0); got != "" {
		t.Fatalf("fitToWidth(0) = %q, want empty", got)
	}
	if got := fitToWidth(nil, 10); got != "" {
		t.Fatalf("fitToWidth(nil) = %q, want empty", got)
	}
}

func TestAbbrevPath(t *testing.T) {
	sep := string(filepath.Separator)
	home := "C:" + sep + "Users" + sep + "me"
	cases := []struct {
		path, want string
	}{
		{home, "~"},
		{home + sep + "code", "~" + sep + "code"},
		{home + "x", home + "x"}, // not a path boundary: left untouched
		{"D:" + sep + "other", "D:" + sep + "other"},
	}
	for _, tc := range cases {
		if got := abbrevPath(tc.path, home); got != tc.want {
			t.Fatalf("abbrevPath(%q, %q) = %q, want %q", tc.path, home, got, tc.want)
		}
	}
}

func TestTailPath(t *testing.T) {
	sep := string(filepath.Separator)
	if got := tailPath("a" + sep + "b" + sep + "c"); got != "…c" {
		t.Fatalf("tailPath = %q, want %q", got, "…c")
	}
}
