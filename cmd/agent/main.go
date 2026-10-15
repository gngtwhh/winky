// Command agent runs a minimal interactive multi-turn conversation loop
// against a configured LLM, to verify the llm and agent packages end to end.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"winky/internal/agent"
	"winky/internal/config"
	"winky/internal/llm/api"
	"winky/internal/llm/model"
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

	streamer, err := api.New(m, api.Config{ApiKey: cfg.Provider.APIKey})
	if err != nil {
		log.Fatalf("create llm streamer: %v", err)
	}

	a := agent.New(
		streamer,
		agent.WithModel(cfg.Provider.Model),
		agent.WithTemperature(cfg.Agent.Temperature),
		agent.WithMaxTokens(cfg.Provider.MaxTokens),
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("winky ready (model=%s). type \"exit\" to quit.\n\n", cfg.Provider.Model)

	scanner := bufio.NewScanner(os.Stdin)
	fmt.Print("you> ")
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			fmt.Print("you> ")
			continue
		}
		if line == "exit" || line == "quit" {
			break
		}

		reply, unknownStr, err := a.Turn(ctx, line)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		} else {
			fmt.Printf("agent> %s\n\n", reply)
			fmt.Printf("unknown log> %s\n\n", unknownStr)
		}
		fmt.Print("you> ")
	}

	if err := scanner.Err(); err != nil {
		log.Fatalf("reading input: %v", err)
	}
}
