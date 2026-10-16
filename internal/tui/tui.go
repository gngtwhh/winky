package tui

import (
	"context"
	"io"
	"os"

	tea "charm.land/bubbletea/v2"

	"winky/internal/agent"
)

// Agent is the subset of *agent.Agent the TUI needs. Keeping it an interface
// lets tests inject a scripted event channel without a real LLM.
type Agent interface {
	Turn(ctx context.Context, input string) (agent.TurnResult, error)
	Events() <-chan agent.Event
}

// Tui owns the Bubble Tea program and the single reader of Agent.Events().
type Tui struct {
	ag Agent

	in   io.Reader
	out  io.Writer
	opts []tea.ProgramOption

	modelLabel      string
	altScreen       bool
	expandReasoning bool

	evCh chan AgentEvent
	m    model
}

// Option configures a Tui.
type Option func(*Tui)

// WithInput overrides the program input (defaults to os.Stdin).
func WithInput(r io.Reader) Option { return func(t *Tui) { t.in = r } }

// WithOutput overrides the program output (defaults to os.Stdout).
func WithOutput(w io.Writer) Option { return func(t *Tui) { t.out = w } }

// WithProgramOptions appends raw Bubble Tea program options.
func WithProgramOptions(opts ...tea.ProgramOption) Option {
	return func(t *Tui) { t.opts = append(t.opts, opts...) }
}

// WithModelLabel sets the label shown in the header, e.g. "deepseek/deepseek-chat".
func WithModelLabel(label string) Option { return func(t *Tui) { t.modelLabel = label } }

// WithAltScreen toggles the alternate screen buffer (default true).
func WithAltScreen(on bool) Option { return func(t *Tui) { t.altScreen = on } }

// WithReasoningExpanded controls whether a finished reasoning block stays
// expanded. When false (the default), reasoning streams inline and folds away
// as soon as the answer starts.
func WithReasoningExpanded(on bool) Option {
	return func(t *Tui) { t.expandReasoning = on }
}

// New builds a TUI around an Agent. The agent may be nil, in which case sends
// are rejected with a notice instead of panicking.
func New(ag Agent, opts ...Option) *Tui {
	t := &Tui{
		ag:        ag,
		in:        os.Stdin,
		out:       os.Stdout,
		altScreen: true,
		evCh:      make(chan AgentEvent, 64),
	}
	for _, opt := range opts {
		opt(t)
	}
	t.m = newModel(ag, newStyles(), t.modelLabel, t.expandReasoning)
	t.m.eventChan = t.evCh
	t.m.altScreen = t.altScreen
	return t
}

// Run starts the event pump and blocks until the user quits or ctx is
// cancelled. A context passed through tea.WithContext is the parent of the one
// used here, so cancelling it also unblocks any in-flight turn.
func (t *Tui) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The agent's emit is a blocking send on a shared channel: this pump must
	// be the only reader, and it must be running before the first Turn.
	pumpDone := make(chan struct{})
	startPump(ctx, t.ag, t.evCh, pumpDone)
	defer func() {
		cancel()
		<-pumpDone
	}()

	t.m.ctx = ctx
	t.m.eventChan = t.evCh
	t.m.altScreen = t.altScreen

	opts := append([]tea.ProgramOption{
		tea.WithContext(ctx),
		tea.WithInput(t.in),
		tea.WithOutput(t.out),
	}, t.opts...)

	_, err := tea.NewProgram(t.m, opts...).Run()
	return err
}
