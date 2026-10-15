package api

import (
	"fmt"
	"winky/internal/llm"
	"winky/internal/llm/api/openai"
	"winky/internal/llm/model"
)

type Config struct {
	ApiKey string
}

func New(m *model.Model, cfg Config) (llm.Streamer, error) {
	if m.Name == "" {
		return nil, fmt.Errorf("model name is required")
	}
	if m.Protocol == "" {
		if err := model.Fix(m); err != nil {
			return nil, fmt.Errorf("api: resolve protocol for model %q: %w", m.Name, err)
		}
	}

	switch m.Protocol {
	case "openai-responses":
		return openai.NewResponses(
			openai.Config{
				ApiKey:  cfg.ApiKey,
				Model:   m.Name,
				BaseURL: m.BaseURL,
			},
		), nil

		// case "openai-completions":
		// 	return openai.NewCompletions(
		// 		openai.Config{
		// 			ApiKey:  cfg.ApiKey,
		// 			Model:   m.Name,
		// 			BaseURL: m.BaseURL,
		// 		},
		// 	), nil

		// case "anthropic-messages":
		// 	return anthropic.NewMessages(
		// 		anthropic.Config{
		// 			APIKey:  cfg.APIKey,
		// 			BaseURL: m.BaseURL,
		// 		},
		// 	), nil
	}
	return nil, fmt.Errorf("unsupported protocol: %q for model %q", m.Protocol, m.Name)
}
