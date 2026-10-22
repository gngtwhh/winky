package config

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/joho/godotenv"
)

type Config struct {
	Provider ProviderConfig `json:"provider"`
	Agent    AgentConfig    `json:"agent"`
	LogLevel string         `json:"log_level"`
	Cwd      string         `json:"cwd"`
}

type ProviderConfig struct {
	Name            string `json:"name"` // "openai" | "anthropic" | "deepseek" ...
	APIKey          string `json:"apiKey"`
	BaseURL         string `json:"baseUrl"`
	Model           string `json:"model"`
	MaxOutputTokens int    `json:"maxOutputTokens,omitempty"`
	// ContextWindow is the model's context window in tokens. Zero means
	// unknown, which hides the TUI's context-usage gauge.
	ContextWindow int `json:"contextWindow,omitempty"`
}

type AgentConfig struct {
	AgentDir    string           `json:"agentDir"`
	Temperature float64          `json:"temperature,omitempty"`
	Compaction  CompactionConfig `json:"compaction"`
}

// CompactionConfig tunes automatic context compaction; zero fields fall back to
// the defaults in DefaultConfig.
type CompactionConfig struct {
	Enabled          bool   `json:"enabled"`
	TriggerPercent   int    `json:"triggerPercent,omitempty"`
	HardPercent      int    `json:"hardPercent,omitempty"`
	KeepPercent      int    `json:"keepPercent,omitempty"`
	MaxPromptTokens  int    `json:"maxPromptTokens,omitempty"`
	MaxSummaryTokens int    `json:"maxSummaryTokens,omitempty"`
	Model            string `json:"model,omitempty"`
}

// Load loads config.json and .env files
// .env contains secret data such as API_KEY
func Load(configPath string) (*Config, error) {
	cfg := DefaultConfig()

	if configPath != "" {
		if path.Ext(configPath) != ".json" {
			return nil, fmt.Errorf("config file must end with .json")
		}
		if err := loadJSON(configPath, cfg); err != nil {
			return nil, fmt.Errorf("loading config %s: %w", configPath, err)
		}
	}

	dotenvPath := filepath.Join(cfg.Cwd, ".env")
	_ = godotenv.Load(dotenvPath)

	apiKey := os.Getenv("API_KEY")
	cfg.Provider.APIKey = apiKey

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}
	return cfg, nil
}

func loadJSON(configPath string, cfg *Config) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal(
		data, cfg,
		json.RejectUnknownMembers(true),
	)
}
