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
	return nil
}
