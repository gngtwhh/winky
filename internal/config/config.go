package config

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Provider ProviderConfig `json:"provider"`
	Agent    AgentConfig    `json:"agent"`
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
		if filepath.Ext(configPath) != ".json" {
			return nil, fmt.Errorf("config file must end with .json")
		}
		if err := loadJSON(configPath, cfg); err != nil {
			return nil, fmt.Errorf("loading config %s: %w", configPath, err)
		}
	}

	agentDir, err := expandHome(cfg.Agent.AgentDir)
	if err != nil {
		return nil, fmt.Errorf("loading config: agent.agentDir: %w", err)
	}
	cfg.Agent.AgentDir = agentDir

	dotenvPath := filepath.Join(cfg.Cwd, ".env")
	_ = godotenv.Load(dotenvPath)

	apiKey := os.Getenv("API_KEY")
	cfg.Provider.APIKey = apiKey

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}
	return cfg, nil
}

// expandHome replaces a leading ~ with the user's home directory. Every other
// path is returned unchanged, so an absolute or relative directory keeps
// working.
func expandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, p[2:]), nil
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
