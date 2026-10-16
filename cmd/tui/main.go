// Command tui runs the winky terminal UI: a Bubble Tea front end over the same
// agent loop as cmd/agent.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	tea "charm.land/bubbletea/v2"

	"winky/internal/agent"
	"winky/internal/config"
	"winky/internal/llm/api"
	"winky/internal/llm/model"
	"winky/internal/tui"
)

func main() {
	configPath := flag.String("config", "", "path to config.json (default: defaults + .env)")
	catalogPath := flag.String("catalog", "catalog.json", "path to model catalog.json")
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

	streamer, err := api.New(m, api.Config{ApiKey: cfg.Provider.APIKey})
	if err != nil {
		log.Fatalf("create llm streamer: %v", err)
	}

	maxTokens := cfg.Provider.MaxTokens
	if maxTokens <= 0 {
		maxTokens = config.DefaultMaxTokens
	}

	a := agent.New(
		streamer,
		agent.WithModel(cfg.Provider.Model),
		agent.WithTemperature(cfg.Agent.Temperature),
		agent.WithMaxTokens(maxTokens),
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The TUI is the single consumer of a.Events(); do not add another drainer
	// here, or the two readers will steal each other's events.
	label := m.Provider + "/" + m.Name
	if err := tui.New(a, tui.WithModelLabel(label)).Run(ctx); err != nil &&
		!errors.Is(err, tea.ErrProgramKilled) {
		log.Fatalf("tui: %v", err)
	}
}
