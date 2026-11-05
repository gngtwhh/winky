package tui

import (
	"winky/internal/transcript"
)

// replayCards rebuilds the scrollback of a resumed session. It mirrors
// applyTurn: one card per user message, reasoning or summary block, assistant
// output, and tool call, with tool results attached to their call card.
func (m *model) replayCards(events []transcript.Event) {
	calls := make(map[string]int)
	for _, ev := range events {
		switch e := ev.(type) {
		case transcript.UserMessage:
			m.history = append(m.history, card{Kind: cardUser, Text: e.Content})

		case transcript.AssistantReasoningMessage:
			m.history = append(m.history, card{
				Kind: cardReasoning, Text: e.Content, Expanded: m.expandReasoning,
			})

		case transcript.AssistantReasoningSummary:
			m.history = append(m.history, card{
				Kind: cardSummary, Text: e.Content, Expanded: m.expandReasoning,
			})

		case transcript.AssistantOutputMessage:
			if e.Content != "" {
				m.history = append(m.history, card{Kind: cardAssistant, Text: e.Content})
			}
			for _, tc := range e.ToolCalls {
				m.history = append(m.history, card{
					Kind: cardTool, ID: tc.CallID, Title: tc.Name, Text: tc.Arguments,
				})
				calls[tc.CallID] = len(m.history) - 1
			}

		case transcript.ToolResult:
			if i, ok := calls[e.CallID]; ok {
				m.history[i].Output = e.Output
			}

		case transcript.CompactionSummary:
			m.history = append(m.history, card{
				Kind: cardNotice,
				Text: "context compacted · " + formatTokens(e.TokensBefore) + " → " + formatTokens(e.TokensAfter),
			})
		}
	}
}
