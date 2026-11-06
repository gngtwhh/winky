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
				m.history[i].IsError = e.Aborted
			}

		case transcript.TurnAborted:
			m.history = append(m.history, card{
				Kind: cardNotice,
				Text: "request aborted",
			})

		case transcript.TurnFailed:
			m.history = append(m.history, card{Kind: cardError, Text: e.Error})

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
			m.history = append(m.history, card{
				Kind: cardNotice,
				Text: "context compacted · " + formatTokens(e.TokensBefore) + " → " + formatTokens(e.TokensAfter),
			})
		}
	}
}
