package config

import "errors"

// Validate checks that the config holds the minimum required values to run
// an agent. It is called by Load before returning.
func (c *Config) Validate() error {
	if c == nil {
		return errors.New("config: nil config")
	}
	if c.Provider.APIKey == "" {
		return errors.New("config: provider.apiKey is required (set API_KEY in .env)")
	}
	if c.Provider.Model == "" {
		return errors.New("config: provider.model is required")
	}
	c.Agent.Compaction.normalize()
	return nil
}

// normalize fills omitted compaction fields and keeps the hard threshold above
// the soft one.
func (c *CompactionConfig) normalize() {
	if c.TriggerPercent <= 0 {
		c.TriggerPercent = 80
	}
	if c.TriggerPercent > 95 {
		c.TriggerPercent = 95
	}
	if c.HardPercent <= c.TriggerPercent {
		c.HardPercent = c.TriggerPercent + 8
	}
	if c.HardPercent > 100 {
		c.HardPercent = 100
	}
	if c.KeepPercent <= 0 {
		c.KeepPercent = 30
	}
	if c.KeepPercent > 90 {
		c.KeepPercent = 90
	}
	if c.MaxSummaryTokens <= 0 {
		c.MaxSummaryTokens = 1024
	}
	if c.MaxPromptTokens < 0 {
		c.MaxPromptTokens = 0
	}
}
