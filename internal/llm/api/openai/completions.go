package openai

import (
	"context"

	"winky/internal/llm"

	"github.com/openai/openai-go/v3"
)

// CompletionsProtocol holds an OpenAI API client and implements the llm.Streamer interface
type CompletionsProtocol struct {
	client *openai.Client
}

func NewCompletions(cfg Config) *CompletionsProtocol {
	return &CompletionsProtocol{
		client: newClient(cfg),
	}
}

func (p *CompletionsProtocol) Stream(
	ctx context.Context, req llm.Request,
) (<-chan llm.StreamChunk, error) {
	panic("not implemented")
}

func (p *CompletionsProtocol) Name() string {
	return "openai-completions"
}
