// Command tui runs the winky terminal UI: a Bubble Tea front end over the same
// agent loop as cmd/agent.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	tea "charm.land/bubbletea/v2"

	"winky/internal/agent"
	"winky/internal/config"
	"winky/internal/llm"
	"winky/internal/llm/api"
	"winky/internal/llm/model"
	"winky/internal/session"
	"winky/internal/tool"
	"winky/internal/transcript"
	"winky/internal/transcript/jsonl"
	"winky/internal/tui"
)

func main() {
	configPath := flag.String("config", "", "path to config.json (default: defaults + .env)")
	catalogPath := flag.String("catalog", "catalog.json", "path to model catalog.json")
	promptPath := flag.String("prompt", "", "path to a system prompt file (default: none)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	if err := model.LoadCatalog(*catalogPath); err != nil {
		log.Fatalf("load model catalog: %v", err)
	}

	m := &model.Model{
		Provider: cfg.Provider.Name,
		BaseURL:  cfg.Provider.BaseURL,
		Name:     cfg.Provider.Model,
	}
	if err := model.Fix(m); err != nil {
		log.Fatalf("resolve model %q: %v", cfg.Provider.Model, err)
	}
	// An explicit config value is the generic override for models whose
	// window is not in the catalog.
	if cfg.Provider.ContextWindow > 0 {
		m.ContextWindow = cfg.Provider.ContextWindow
	}

	streamer, err := api.New(m, api.Config{ApiKey: cfg.Provider.APIKey})
	if err != nil {
		log.Fatalf("create llm streamer: %v", err)
	}

	policy := tool.NewPolicy(tool.WorkspaceWrite)
	reg, err := tool.NewDefault(cfg.Cwd, policy)
	if err != nil {
		log.Fatalf("create tools: %v", err)
	}

	sess, err := session.New(cfg.Agent.AgentDir, session.Options{
		Cwd:      cfg.Cwd,
		Provider: cfg.Provider.Name,
		Model:    cfg.Provider.Model,
	})
	if err != nil {
		log.Fatalf("create session: %v", err)
	}
	defer func() {
		if err := sess.Close(); err != nil {
			log.Printf("close session: %v", err)
		}
	}()

	rec, err := jsonl.Open(sess.TranscriptPath())
	if err != nil {
		log.Fatalf("open transcript: %v", err)
	}
	defer func() {
		if err := rec.Close(); err != nil {
			log.Printf("close transcript: %v", err)
		}
	}()

	a, err := newAgent(streamer, cfg, *promptPath, reg, rec)
	if err != nil {
		log.Fatalf("create agent: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The TUI is the single consumer of a.Events(); do not add another drainer
	// here, or the two readers will steal each other's events.
	label := m.Provider + "/" + m.Name
	t := tui.New(a,
		tui.WithModelLabel(label),
		tui.WithWorkDir(cfg.Cwd),
		tui.WithPolicy(policy),
		tui.WithContextWindow(m.ContextWindow),
	)
	if err := t.Run(ctx); err != nil && !errors.Is(err, tea.ErrProgramKilled) {
		log.Fatalf("tui: %v", err)
	}
}

// newAgent builds the conversational agent, loading the optional system prompt
// file named on the command line; an empty or blank file means no prompt.
//
// The prompt is sent as model instructions on every turn. As the one stable
// prefix the conversation always repeats, it is also what makes provider-side
// prompt caching pay off: a short chat never reaches the minimum cacheable
// prefix, and its cache rate then stays at "—" for the whole session.
func newAgent(streamer llm.Streamer, cfg *config.Config, promptPath string, tools agent.Toolset, rec transcript.Recorder) (*agent.Agent, error) {
	maxOutputTokens := cfg.Provider.MaxOutputTokens
	if maxOutputTokens <= 0 {
		maxOutputTokens = config.DefaultMaxOutputTokens
	}

	opts := []agent.Option{
		agent.WithModel(cfg.Provider.Model),
		agent.WithTemperature(cfg.Agent.Temperature),
		agent.WithMaxOutputTokens(maxOutputTokens),
	}
	if tools != nil {
		opts = append(opts, agent.WithTools(tools))
	}
	if rec != nil {
		opts = append(opts, agent.WithTranscriptRecorder(rec))
	}

	if promptPath != "" {
		prompt, err := readSystemPrompt(promptPath)
		if err != nil {
			return nil, err
		}
		if prompt != "" {
			opts = append(opts, agent.WithSystemPrompt(prompt))
		}
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
