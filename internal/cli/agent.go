package cli

import (
	"fmt"
	"os"
	"strings"

	"winky/internal/agent"
	"winky/internal/config"
	"winky/internal/llm"
	"winky/internal/prompt"
	"winky/internal/transcript"
)

// newAgent builds the conversational agent. The system prompt comes from the
// -prompt file when one is given, and from the built-in default otherwise; an
// empty or blank file means no prompt. The prompt seeds a new session: a resumed
// session sends the one it recorded.
func newAgent(streamer llm.Streamer, cfg *config.Config, promptPath string, tools agent.Toolset, rec transcript.Recorder, contextWindow int) (*agent.Agent, error) {
	maxOutputTokens := cfg.Provider.MaxOutputTokens
	if maxOutputTokens <= 0 {
		maxOutputTokens = config.DefaultMaxOutputTokens
	}

	comp := cfg.Agent.Compaction
	opts := []agent.Option{
		agent.WithModel(cfg.Provider.Model),
		agent.WithTemperature(cfg.Agent.Temperature),
		agent.WithMaxOutputTokens(maxOutputTokens),
		agent.WithContextWindow(contextWindow),
		agent.WithCompaction(agent.CompactionOptions{
			Enabled:          comp.Enabled,
			TriggerPercent:   comp.TriggerPercent,
			HardPercent:      comp.HardPercent,
			KeepPercent:      comp.KeepPercent,
			MaxPromptTokens:  comp.MaxPromptTokens,
			MaxSummaryTokens: comp.MaxSummaryTokens,
			Model:            comp.Model,
		}),
	}
	if tools != nil {
		opts = append(opts, agent.WithTools(tools))
	}
	if rec != nil {
		opts = append(opts, agent.WithTranscriptRecorder(rec))
	}

	initial := prompt.DefaultSystemPrompt
	if promptPath != "" {
		text, err := readSystemPrompt(promptPath)
		if err != nil {
			return nil, err
		}
		initial = text
	}
	if initial != "" {
		opts = append(opts, agent.WithInitialSystemPrompt(initial))
	}

	return agent.New(streamer, opts...), nil
}

// readSystemPrompt loads a system prompt file. Surrounding whitespace is
// trimmed, so an empty or blank file means "no system prompt".
func readSystemPrompt(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return strings.TrimSpace(string(data)), nil
}
