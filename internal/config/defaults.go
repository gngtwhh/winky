package config

import (
	"os"
	"path"
)

const (
	DefaultMaxOutputTokens int = 16384

	DefaultLogLevel string = "info"

	DefaultAgentDirBase string  = ".winky"
	DefaultTemperature  float64 = 0.7
)

func DefaultConfig() *Config {
	home, _ := os.UserHomeDir()

	return &Config{
		Provider: ProviderConfig{
			MaxOutputTokens: DefaultMaxOutputTokens,
		},
		Agent: AgentConfig{
			AgentDir:    path.Join(home, DefaultAgentDirBase),
			Temperature: DefaultTemperature,
			Compaction: CompactionConfig{
				Enabled:          true,
				TriggerPercent:   80,
				HardPercent:      92,
				KeepPercent:      30,
				MaxSummaryTokens: 1024,
			},
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
