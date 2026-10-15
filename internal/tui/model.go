package tui

type model struct {
	// text content
	reasoningContent []string
	outputContent    []string
	userContent      string
	// status
	selectLlmModel string
}
