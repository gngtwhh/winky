package openai

const defaultBaseURL = "https://api.openai.com/v1"

type Config struct {
	ApiKey  string
	Model   string
	BaseURL string
}
