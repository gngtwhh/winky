package tui

import (
	"context"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Chrome around the scrollback viewport.
const (
	headerRows = 1
	statusRows = 1
	chromeGap  = 1

	minViewportHeight = 1
	minViewportWidth  = 20

	minInputHeight = 3
	maxInputHeight = 8
)

// keyMap holds the TUI-level bindings. Widget bindings (textarea, viewport)
// stay inside their own components.
type keyMap struct {
	Send       key.Binding
	Cancel     key.Binding
	Quit       key.Binding
	ScrollUp   key.Binding
	ScrollDown key.Binding
	ToggleAll  key.Binding
}

func defaultKeyMap() keyMap {
	return keyMap{
		Send:       key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "发送")),
		Cancel:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "取消/清空")),
		Quit:       key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "退出")),
		ScrollUp:   key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "上翻")),
		ScrollDown: key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "下翻")),
		ToggleAll:  key.NewBinding(key.WithKeys("ctrl+t"), key.WithHelp("ctrl+t", "展开/折叠全部推理")),
	}
}

type model struct {
	// Dependencies and lifecycle.
	ag        Agent
	ctx       context.Context
	eventChan chan AgentEvent
	turnCanc  context.CancelFunc

	// Layout.
	width, height   int
	ready           bool
	altScreen       bool
	lastInputHeight int

	// Components.
	input textarea.Model // multi-line user input
	vp    viewport.Model // scrollback of every card
	sp    spinner.Model  // shown while a turn is streaming

	// Session view state (in memory only for now).
	history         []card
	hints           []hint // clickable lines (toggling a reasoning card)
	follow          bool   // autoscroll while streaming
	dirty           bool   // deltas pending a coalesced rebuild
	turn            int
	streaming       bool
	cancelled       map[int]bool // turn ids cancelled by the user
	reasoning       string       // accumulated ReasoningDelta of the live turn
	output          string       // accumulated OutputDelta of the live turn
	expandReasoning bool

	// Status line.
	status  string
	err     error
	started time.Time

	style Styles
	keys  keyMap
	label string
}

func newModel(ag Agent, st Styles, label string, expandReasoning bool) model {
	ta := textarea.New()
	ta.Placeholder = "输入消息…  (enter 发送 · ctrl+j 换行 · esc 取消)"
	ta.Prompt = "> "
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.MinHeight = minInputHeight
	ta.MaxHeight = maxInputHeight
	ta.DynamicHeight = true

	// The default placeholder foreground (ANSI 240) is nearly invisible
	// against the cursor-line background the textarea paints under empty
	// input. Brighten it to a readable gray so the hint stays legible.
	tst := ta.Styles()
	tst.Focused.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	tst.Blurred.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	ta.SetStyles(tst)

	// enter must send the message, so drop it from the textarea's newline
	// bindings and keep the explicit ones (ctrl+j / ctrl+m / ctrl+enter).
	km := textarea.DefaultKeyMap()
	km.InsertNewline = key.NewBinding(
		key.WithKeys("ctrl+j", "ctrl+m", "ctrl+enter"),
		key.WithHelp("ctrl+j", "换行"),
	)
	ta.KeyMap = km
	ta.Focus()

	sp := spinner.New(spinner.WithSpinner(spinner.Dot))

	return model{
		ag:              ag,
		eventChan:       make(chan AgentEvent, 64),
		input:           ta,
		vp:              viewport.New(),
		sp:              sp,
		follow:          true,
		cancelled:       make(map[int]bool),
		expandReasoning: expandReasoning,
		style:           st,
		keys:            defaultKeyMap(),
		label:           label,
	}
}

// Init focuses the input and arms the single event receiver. The pump goroutine
// itself is started by Tui.Run, before the program starts.
func (m model) Init() tea.Cmd {
	return tea.Batch(
		m.input.Focus(),
		waitEvent(m.eventChan),
	)
}

// tuiContext never returns nil so commands always carry a cancellable context.
func (m model) tuiContext() context.Context {
	if m.ctx == nil {
		return context.Background()
	}
	return m.ctx
}

func (m *model) layout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}

	m.input.SetWidth(max(minViewportWidth, m.width-2))
	m.vp.SetWidth(max(minViewportWidth, m.width-2))

	// The input may not eat the whole window on a short terminal: shrink its
	// ceiling before measuring it.
	fixed := headerRows + statusRows + chromeGap
	maxInput := min(maxInputHeight, max(1, m.height-fixed-minViewportHeight))
	m.input.MaxHeight = maxInput
	m.input.MinHeight = min(minInputHeight, maxInput)
	m.input.SetHeight(m.input.Height())

	m.vp.SetHeight(max(minViewportHeight, m.height-fixed-m.input.Height()))
	m.lastInputHeight = m.input.Height()
}

// relayoutIfNeeded reclaims space when the textarea grew or shrank because of
// its dynamic height.
func (m *model) relayoutIfNeeded() {
	if m.input.Height() != m.lastInputHeight {
		m.layout()
	}
}

func (m *model) atBottom() bool {
	maxOffset := m.vp.TotalLineCount() - m.vp.Height()
	if maxOffset < 0 {
		maxOffset = 0
	}
	return m.vp.YOffset() >= maxOffset
}

// finishTurn clears the per-turn streaming state.
func (m *model) finishTurn() {
	m.streaming = false
	m.turnCanc = nil
	m.dirty = false
}
