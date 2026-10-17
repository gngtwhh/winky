package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"winky/internal/llm"
)

// formatTokens renders a token count with a compact suffix.
func formatTokens(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	}
}

// formatUsage renders "↑in ↓out · Cache N%", or "—" when there is no data.
func formatUsage(u llm.TokenUsage) string {
	if u.TotalTokens == 0 {
		return "—"
	}
	out := fmt.Sprintf("↑%s ↓%s", formatTokens(u.PromptTokens), formatTokens(u.CompletionTokens))
	if u.PromptTokens > 0 {
		out += fmt.Sprintf(" · Cache %d%%", int(u.CacheHitRate()*100+0.5))
	}
	return out
}

// fitToWidth returns the first candidate that fits within width columns, or
// the last candidate truncated with an ellipsis.
func fitToWidth(candidates []string, width int) string {
	if len(candidates) == 0 {
		return ""
	}
	for _, c := range candidates {
		if displayWidth(c) <= width {
			return c
		}
	}
	return truncate(candidates[len(candidates)-1], width, "…")
}

// abbrevPath replaces the home directory prefix with "~".
func abbrevPath(path, home string) string {
	if home == "" {
		return path
	}
	sep := string(filepath.Separator)
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+sep) {
		return "~" + sep + strings.TrimPrefix(path, home+sep)
	}
	return path
}

// tailPath returns an ellipsis plus the last path component.
func tailPath(path string) string {
	return "…" + filepath.Base(path)
}
