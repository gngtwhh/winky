package cli

import (
	"context"
	"errors"
	"fmt"
	"log"

	tea "charm.land/bubbletea/v2"

	"winky/internal/config"
	"winky/internal/llm/api"
	"winky/internal/llm/model"
	"winky/internal/session"
	"winky/internal/tool"
	defaulttool "winky/internal/tool/default_tool"
	"winky/internal/transcript/jsonl"
	"winky/internal/tui"
)

// runTUI loads the configuration and starts a TUI session. Errors are returned
// to the command layer, which reports them once and exits non-zero.
func runTUI(ctx context.Context, opts *options) error {
	cfg, err := config.Load(opts.configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if err := model.LoadCatalog(opts.catalogPath); err != nil {
		return fmt.Errorf("load model catalog: %w", err)
	}

	m := &model.Model{
		Provider: cfg.Provider.Name,
		BaseURL:  cfg.Provider.BaseURL,
		Name:     cfg.Provider.Model,
	}
	if err := model.Fix(m); err != nil {
		return fmt.Errorf("resolve model %q: %w", cfg.Provider.Model, err)
	}
	// An explicit config value is the generic override for models whose
	// window is not in the catalog.
	if cfg.Provider.ContextWindow > 0 {
		m.ContextWindow = cfg.Provider.ContextWindow
	}

	streamer, err := api.New(m, api.Config{ApiKey: cfg.Provider.APIKey})
	if err != nil {
		return fmt.Errorf("create llm streamer: %w", err)
	}

	policy := tool.NewPolicy(tool.WorkspaceWrite)
	reg, err := defaulttool.NewDefault(cfg.Cwd, policy)
	if err != nil {
		return fmt.Errorf("create tools: %w", err)
	}

	sess, err := openSession(ctx, cfg, opts.resume)
	if errors.Is(err, tui.ErrSelectionAborted) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() {
		if err := sess.Close(); err != nil {
			log.Printf("close session: %v", err)
		}
	}()

	rec, err := jsonl.Open(sess.TranscriptPath())
	if err != nil {
		return fmt.Errorf("open transcript: %w", err)
	}
	for _, p := range rec.Problems() {
		log.Printf("transcript: seq %d: %s", p.Seq, p.Message)
	}
	defer func() {
		if err := rec.Close(); err != nil {
			log.Printf("close transcript: %v", err)
		}
	}()

	// Replay the stored transcript into the scrollback; a fresh session has
	// none. The agent folds the same events into its view on the first turn.
	replay, err := rec.ReadAll()
	if err != nil {
		return fmt.Errorf("read transcript: %w", err)
	}

	if cfg.Agent.Compaction.Enabled && m.ContextWindow <= 0 {
		log.Printf(
			"context compaction will not run: the context window is unknown; " +
				"set provider.contextWindow in config.json or add contextWindow to the catalog",
		)
	}

	a, err := newAgent(streamer, cfg, opts.promptPath, reg, rec, m.ContextWindow)
	if err != nil {
		return fmt.Errorf("create agent: %w", err)
	}

	// The TUI is the single consumer of a.Events(); do not add another drainer
	// here, or the two readers will steal each other's events.
	label := m.Provider + "/" + m.Name
	t := tui.New(
		a,
		tui.WithModelLabel(label),
		tui.WithWorkDir(cfg.Cwd),
		tui.WithPolicy(policy),
		tui.WithContextWindow(m.ContextWindow),
		tui.WithReplay(replay),
	)
	if err := t.Run(ctx); err != nil && !errors.Is(err, tea.ErrProgramKilled) {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}

// openSession resumes a stored session when requested, and otherwise creates a
// fresh one. An aborted picker is not an error.
func openSession(ctx context.Context, cfg *config.Config, resume bool) (*session.Session, error) {
	if resume {
		sess, err := tui.ChooseSession(ctx, cfg.Agent.AgentDir, cfg.Cwd)
		if errors.Is(err, tui.ErrSelectionAborted) {
			return nil, err
		}
		if err != nil {
			return nil, fmt.Errorf("choose session: %w", err)
		}
		if sess != nil {
			log.Printf("resumed session %s", sess.ID)
			return sess, nil
		}
	}

	sess, err := session.New(
		cfg.Agent.AgentDir, session.Options{
			Cwd:      cfg.Cwd,
			Provider: cfg.Provider.Name,
			Model:    cfg.Provider.Model,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return sess, nil
}
