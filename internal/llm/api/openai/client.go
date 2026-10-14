package openai

import (
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func newClient(cfg Config) *openai.Client {
	opts := []option.RequestOption{option.WithAPIKey(cfg.ApiKey)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	client := openai.NewClient(opts...)
	return &client
}
