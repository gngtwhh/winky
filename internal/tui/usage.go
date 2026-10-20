package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"winky/internal/llm"
)

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
//
// An uncached turn is the normal case (cold cache, or too short a prompt), so a
// zero cache count renders as "Cache —" rather than the misleading "Cache 0%".
// Any real hit is floored at 1% for the same reason.
func formatUsage(u llm.TokenUsage) string {
	if u.TotalTokens == 0 {
		return "—"
	}
	out := fmt.Sprintf("↑%s ↓%s", formatTokens(u.PromptTokens), formatTokens(u.CompletionTokens))
	if u.PromptTokens > 0 {
		if u.CachedTokens <= 0 {
			out += " · Cache —"
		} else {
			pct := int(u.CacheHitRate()*100 + 0.5)
			if pct < 1 {
				pct = 1
			}
			out += fmt.Sprintf(" · Cache %d%%", pct)
		}
	}
	return out
}

// formatContextUsage renders "ctx 40%" from the most recent request's prompt
// tokens and the model's context window. It returns "" when either value is
// unknown, so the usage row keeps its previous form.
func formatContextUsage(tokens, window int) string {
	if tokens <= 0 || window <= 0 {
		return ""
	}
	pct := (tokens*100 + window/2) / window
	if pct < 1 {
		pct = 1
	}
	if pct > 100 {
		pct = 100
	}
	return fmt.Sprintf("ctx %d%%", pct)
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

func tailPath(path string) string {
	return "…" + filepath.Base(path)
}
