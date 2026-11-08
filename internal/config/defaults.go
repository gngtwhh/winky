package config

import (
	"os"
	"path/filepath"
)

const (
	DefaultMaxOutputTokens int = 16384

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
			AgentDir:    filepath.Join(home, DefaultAgentDirBase),
			Temperature: DefaultTemperature,
			Compaction: CompactionConfig{
				Enabled:          true,
				TriggerPercent:   80,
				HardPercent:      92,
				KeepPercent:      30,
				MaxSummaryTokens: 1024,
			},
		},
		Cwd: mustGetWd(),
	}
}

func mustGetWd() string {
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	return wd
}
