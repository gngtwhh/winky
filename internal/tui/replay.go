package tui

import (
	"winky/internal/llm"
	"winky/internal/transcript"
)

// llmUsage converts persisted usage into the type the status bar prints.
func llmUsage(u transcript.Usage) llm.TokenUsage {
	return llm.TokenUsage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CachedTokens:     u.CachedTokens,
	}
}

// replayCards rebuilds the scrollback of a resumed session. It mirrors
// applyTurn: one card per user message, reasoning or summary block, assistant
// output, and tool call, with tool results attached to their call card. It also
// restores the token totals the transcript recorded.
func (m *model) replayCards(events []transcript.Event) {
	calls := make(map[string]int)
	for _, ev := range events {
		switch e := ev.(type) {
		case transcript.UserMessage:
			m.cards = append(m.cards, card{Kind: cardUser, Text: e.Content})

		case transcript.AssistantReasoningMessage:
			m.cards = append(m.cards, card{
				Kind: cardReasoning, Text: e.Content, Expanded: m.expandReasoning,
			})

		case transcript.AssistantReasoningSummary:
			m.cards = append(m.cards, card{
				Kind: cardSummary, Text: e.Content, Expanded: m.expandReasoning,
			})

		case transcript.AssistantOutputMessage:
			if e.Content != "" {
				m.cards = append(m.cards, card{Kind: cardAssistant, Text: e.Content})
			}
			for _, tc := range e.ToolCalls {
				m.cards = append(m.cards, card{
					Kind: cardTool, ID: tc.CallID, Title: tc.Name, Text: tc.Arguments,
				})
				calls[tc.CallID] = len(m.cards) - 1
			}

		case transcript.ToolResult:
			if i, ok := calls[e.CallID]; ok {
				m.cards[i].Output = e.Output
				m.cards[i].IsError = e.Aborted
			}

		case transcript.TurnAborted:
			m.cards = append(m.cards, card{
				Kind: cardNotice,
				Text: "request aborted",
			})

		case transcript.TurnFailed:
			m.cards = append(m.cards, card{Kind: cardError, Text: e.Error})

		case transcript.StepEnd:
			// Usage is persisted per request, so a resumed session shows what it
			// already spent instead of zero.
			if e.Usage != nil {
				m.totalUsage = m.totalUsage.Add(llmUsage(*e.Usage))
				if e.Usage.PromptTokens > 0 {
					m.contextTokens = e.Usage.PromptTokens
				}
			}

		case transcript.CompactionSummary:
			if e.Usage != nil {
				m.totalUsage = m.totalUsage.Add(llmUsage(*e.Usage))
			}
			m.cards = append(m.cards, card{
				Kind: cardNotice,
				Text: "context compacted · " + formatTokens(e.TokensBefore) + " → " + formatTokens(e.TokensAfter),
			})
		}
	}
}
