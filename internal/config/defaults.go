package config

import (
	"os"
	"path"
)

const (
	DefaultMaxOutputTokens int = 16384

	DefaultLogLevel string = "info"

	DefaultAgentDirBase   string  = ".winky"
	DefaultMaxTurn        int     = 20
	DefaultMaxTimeMinutes         = 60
	DefaultTemperature    float64 = 0.7
)

func DefaultConfig() *Config {
	home, _ := os.UserHomeDir()

	return &Config{
		Provider: ProviderConfig{
			MaxOutputTokens: DefaultMaxOutputTokens,
		},
		Agent: AgentConfig{
			AgentDir:       path.Join(home, DefaultAgentDirBase),
			MaxTurns:       DefaultMaxTurn,
			MaxTimeMinutes: DefaultMaxTimeMinutes,
			Temperature:    DefaultTemperature,
		},
		LogLevel: DefaultLogLevel,
		Cwd:      mustGetWd(),
	}
}

func mustGetWd() string {
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	return wd
}
