package openai

import (
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func newClient(cfg Config) *openai.Client {
	client := openai.NewClient(option.WithAPIKey(cfg.ApiKey))
	return &client
}
