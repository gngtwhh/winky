package tui

import (
	"charm.land/lipgloss/v2"
)

// Styles holds every lipgloss style used by the TUI.
type Styles struct {
	Header    lipgloss.Style
	Status    lipgloss.Style
	Err       lipgloss.Style
	User      lipgloss.Style
	Reasoning lipgloss.Style
	Summary   lipgloss.Style
	Tool      lipgloss.Style
	Hint      lipgloss.Style
	Assistant lipgloss.Style
}

func newStyles() Styles {
	return Styles{
		Header:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")),
		Status:    lipgloss.NewStyle().Foreground(lipgloss.BrightBlack),
		Err:       lipgloss.NewStyle().Foreground(lipgloss.Red),
		User:      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Cyan),
		Reasoning: lipgloss.NewStyle().Faint(true).Italic(true),
		Summary:   lipgloss.NewStyle().Faint(true).Italic(true).Foreground(lipgloss.Color("244")),
		Tool:      lipgloss.NewStyle().Foreground(lipgloss.Color("178")),
		Hint:      lipgloss.NewStyle().Faint(true).Foreground(lipgloss.BrightBlack),
		Assistant: lipgloss.NewStyle().Foreground(lipgloss.Color("252")),
	}
}

func (s Styles) head(k cardKind) lipgloss.Style {
	switch k {
	case cardUser:
		return s.User
	case cardReasoning:
		return s.Reasoning
	case cardSummary:
		return s.Summary
	case cardTool:
		return s.Tool
	case cardError:
		return s.Err
	case cardNotice:
		return s.Hint
	default:
		return s.Header
	}
}

func (s Styles) body(k cardKind) lipgloss.Style {
	switch k {
	case cardReasoning:
		return s.Reasoning
	case cardSummary:
		return s.Summary
	case cardTool:
		return s.Tool
	case cardError:
		return s.Err
	case cardNotice:
		return s.Hint
	default:
		return s.Assistant
	}
}
