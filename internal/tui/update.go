package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"winky/internal/agent"
	"winky/internal/llm"
	"winky/internal/tool"
)

// errAgentNil guards against running without a configured agent.
var errAgentNil = errors.New("tui: no agent configured")

// Update reduces every message. Exactly one waitEvent is in flight at any time,
// armed in Init and re-armed on every AgentEvent. The model is a value type, so
// the helpers below mutate the addressable local copy.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.layout()
		m.rebuild()
		m.relayoutIfNeeded()
		return m, nil

	case AgentEvent:
		// handleEvent needs a pointer: Update's receiver is a copy.
		eventCmd := m.handleEvent(msg.Event)
		// Re-arm the receiver in the same step that consumed an event.
		return m, tea.Batch(eventCmd, waitEvent(m.eventChan))

	case turnResult:
		next, cmd := m.handleTurnResult(msg)
		return next, cmd

	case tickMsg:
		// The rebuild tick only flushes pending deltas; the spinner advances
		// through its own spinner.TickMsg below.
		if m.dirty {
			m.rebuild()
		}
		if m.streaming {
			return m, tickCmd(msg.ID)
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		if !m.streaming {
			// The turn settled while a tick was in flight; do not re-arm.
			return m, nil
		}
		return m, cmd

	case tea.KeyPressMsg:
		if m.perm != nil {
			return m.handlePermKey(msg)
		}
		return m.handleKey(msg)

	case permRequest:
		m.perm = &msg
		return m, nil

	case tea.MouseClickMsg, tea.MouseWheelMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg:
		return m.handleMouse(msg)

	case tea.PasteMsg:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.relayoutIfNeeded()
		return m, cmd
	}

	// Everything else belongs to the widgets.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.relayoutIfNeeded()
	return m, cmd
}

func (m model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.Key()

	switch {
	case key.Matches(msg, m.keys.Quit):
		if m.turnCanc != nil {
			m.turnCanc()
		}
		return m, tea.Quit

	case key.Matches(msg, m.keys.Cancel):
		if m.input.Value() != "" {
			// esc clears a draft first; only an empty input cancels the turn.
			m.input.Reset()
			m.relayoutIfNeeded()
			return m, nil
		}
		if m.turnCanc != nil {
			m.turnCanc()
			m.turnCanc = nil
			m.markCancelled()
			m.status = "Turn cancelled"
		}
		return m, nil

	case key.Matches(msg, m.keys.Send) && k.Mod&tea.ModAlt == 0:
		// Plain enter sends; alt+enter falls through to the textarea.
		return m.submit()

	case key.Matches(msg, m.keys.ToggleAll):
		if m.toggleAllReasoning() {
			m.status = "unfolded all reasoning"
		} else {
			m.status = "folded all reasoning"
		}
		m.rebuild()
		return m, nil

	case key.Matches(msg, m.keys.ScrollUp):
		m.vp.PageUp()
		m.follow = false
		return m, nil

	case key.Matches(msg, m.keys.ScrollDown):
		m.vp.PageDown()
		m.follow = m.streaming && m.atBottom()
		return m, nil
	}

	// Everything else belongs to the textarea, which keeps the cursor visible.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.relayoutIfNeeded()
	return m, cmd
}

// handlePermKey answers the open permission prompt: y allows, n/esc denies.
func (m model) handlePermKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	allow := key.Matches(msg, key.NewBinding(key.WithKeys("y", "Y")))
	deny := key.Matches(msg, key.NewBinding(key.WithKeys("n", "N", "esc")))
	if !allow && !deny {
		return m, nil
	}
	m.perm.reply <- allow
	m.perm = nil
	m.status = ""
	return m, nil
}

// handleMouse routes clicks to clickable reasoning labels and everything else to
// the viewport (wheel scrolling, drag selection).
func (m model) handleMouse(msg tea.Msg) (tea.Model, tea.Cmd) {
	if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft {
		contentY := click.Y - headerRows + m.vp.YOffset()
		if m.toggleHint(contentY) {
			m.rebuild()
		}
	}

	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	m.follow = m.streaming && m.atBottom()
	return m, cmd
}

// handleEvent mutates the model, so it runs on a pointer to Update's local copy.
//
// Delta events are gated on m.streaming so a cancelled turn's late deltas cannot
// resurrect the streaming state; terminal events still record usage after the
// turn settled, but are dropped for a cancelled turn.
func (m *model) handleEvent(e agent.Event) tea.Cmd {
	switch ev := e.(type) {
	case agent.TurnStart:
		if m.cancelled[m.turn] {
			// A cancelled turn that still raced a TurnStart through: ignore it
			// rather than flickering back into the streaming state.
			return nil
		}
		m.streaming = true
		m.err = nil
		m.status = ""
		m.started = ev.Timestamp
		if m.started.IsZero() {
			m.started = time.Now()
		}
		m.usage = llm.TokenUsage{}
		// Live cards left over mean the previous turn never delivered a
		// TurnEnd/TurnFailed; fold them so two live blocks cannot coexist.
		m.freezeLiveCards()
		m.liveBlocks = make(map[blockKey]int)
		m.turnStartIdx = len(m.history)
		return tickCmd(m.turn)

	case agent.ReasoningDelta:
		if !m.streaming || m.cancelled[m.turn] {
			return nil
		}
		m.appendDelta(blockKey{kind: cardReasoning, id: ev.ItemID}, ev.Text)

	case agent.ReasoningSummaryDelta:
		if !m.streaming || m.cancelled[m.turn] {
			return nil
		}
		m.appendDelta(blockKey{kind: cardSummary, id: ev.ItemID}, ev.Text)

	case agent.OutputDelta:
		if !m.streaming || m.cancelled[m.turn] {
			return nil
		}
		m.appendDelta(blockKey{kind: cardAssistant, id: ev.ItemID}, ev.Text)

	case agent.ToolCallStart:
		if !m.streaming || m.cancelled[m.turn] {
			return nil
		}
		idx := m.ensureBlock(blockKey{kind: cardTool, id: ev.CallID})
		m.history[idx].Title = ev.Name
		m.dirty = true

	case agent.ToolCallArgsDelta:
		if !m.streaming || m.cancelled[m.turn] {
			return nil
		}
		m.appendDelta(blockKey{kind: cardTool, id: ev.CallID}, ev.Delta)

	case agent.ToolCallEnd:
		if !m.streaming || m.cancelled[m.turn] {
			return nil
		}
		idx := m.ensureBlock(blockKey{kind: cardTool, id: ev.CallID})
		m.history[idx].Text = ev.Arguments
		m.dirty = true

	case agent.ToolResult:
		if !m.streaming || m.cancelled[m.turn] {
			return nil
		}
		idx := m.ensureBlock(blockKey{kind: cardTool, id: ev.CallID})
		m.history[idx].Output = ev.Output
		m.history[idx].IsError = ev.IsError
		m.history[idx].Duration = ev.Duration
		m.dirty = true

	case agent.TurnEnd:
		if m.cancelled[m.turn] {
			return nil
		}
		// Usage rides the terminal event; record it even if the command result
		// already settled this turn.
		m.usage = ev.Usage
		m.totalUsage = ev.TotalUsage
		if !m.streaming {
			m.rebuild()
			return nil
		}
		m.applyTurn(ev.Turn)
		m.finishTurn()
		m.rebuild()
		return nil

	case agent.TurnFailed:
		if m.cancelled[m.turn] {
			return nil
		}
		m.usage = ev.Usage
		m.totalUsage = ev.TotalUsage
		if !m.streaming {
			m.rebuild()
			return nil
		}
		// Drop any partial live blocks from the failed turn.
		m.history = m.history[:m.turnStartIdx]
		m.liveBlocks = nil
		m.finishTurn()
		m.err = ev.Err
		m.status = ""
		m.appendCard(card{Kind: cardError, Text: ev.Err.Error(), At: time.Now()})
		return nil
	}
	return nil
}

func (m *model) appendDelta(key blockKey, text string) {
	idx := m.ensureBlock(key)
	m.history[idx].Text += text
	m.dirty = true
}

func (m *model) ensureBlock(key blockKey) int {
	if idx, ok := m.liveBlocks[key]; ok {
		return idx
	}
	c := card{Kind: key.kind, ID: key.id, At: m.started, Live: true}
	if key.kind.collapsible() {
		c.Expanded = true // stream inline
	}
	m.history = append(m.history, c)
	m.liveBlocks[key] = len(m.history) - 1
	m.dirty = true
	return len(m.history) - 1
}

func (m *model) applyTurn(turn agent.Turn) {
	m.history = m.history[:m.turnStartIdx]
	results := make(map[string]agent.ToolResult, len(turn.ToolResults))
	for _, r := range turn.ToolResults {
		results[r.CallID] = r
	}
	for _, blk := range turn.Blocks {
		c := m.cardFromBlock(blk)
		if blk.Kind == agent.BlockToolCall {
			if r, ok := results[blk.ID]; ok {
				c.Output = r.Output
				c.IsError = r.IsError
				c.Duration = r.Duration
			}
		}
		m.history = append(m.history, c)
	}
	m.liveBlocks = nil
}

func (m *model) cardFromBlock(blk agent.Block) card {
	c := card{
		Kind: cardKindFor(blk.Kind),
		ID:   blk.ID,
		Text: blk.Text,
		At:   m.started,
	}
	switch blk.Kind {
	case agent.BlockToolCall:
		c.Text = blk.Arguments
		c.Title = blk.Name
	case agent.BlockReasoning, agent.BlockReasoningSummary:
		c.Expanded = m.expandReasoning
	}
	return c
}

func cardKindFor(k agent.BlockKind) cardKind {
	switch k {
	case agent.BlockReasoning:
		return cardReasoning
	case agent.BlockReasoningSummary:
		return cardSummary
	case agent.BlockToolCall:
		return cardTool
	default:
		return cardAssistant
	}
}

// applyCommand handles a "/" input: /readonly, /workspace (or /write), /full.
func (m model) applyCommand(text string) (tea.Model, tea.Cmd) {
	m.input.Reset()
	perm, ok := parsePerm(text)
	if !ok {
		m.status = "unknown command: " + text
		return m, nil
	}
	if m.policy == nil {
		m.status = "no tool policy attached"
		return m, nil
	}
	m.policy.Set(perm)
	m.status = "permission: " + perm.String()
	return m, nil
}

func parsePerm(s string) (tool.Permission, bool) {
	switch strings.TrimSpace(s) {
	case "/readonly":
		return tool.ReadOnly, true
	case "/workspace", "/write":
		return tool.WorkspaceWrite, true
	case "/full":
		return tool.FullAccess, true
	}
	return tool.WorkspaceWrite, false
}

// handleTurnResult reconciles the command's return value with the event stream.
// It takes and returns the model by value: Update must hand back the mutated
// copy, so a pointer receiver here would silently drop the change.
func (m model) handleTurnResult(res turnResult) (tea.Model, tea.Cmd) {
	if m.cancelled[res.TurnID] {
		// The user abandoned this turn: forget it and drop the result.
		delete(m.cancelled, res.TurnID)
		return m, nil
	}
	if res.TurnID != m.turn {
		// A superseded turn: drop it entirely.
		return m, nil
	}

	m.turnCanc = nil
	// The wall-clock duration is only known once the turn command returns, and
	// that can happen after TurnEnd has already settled the cards.
	if c := m.lastCard(cardAssistant); c != nil {
		c.Duration = res.Duration
	}

	if !m.streaming {
		// TurnEnd already settled this turn. Errors normally arrive through
		// TurnFailed, so only reconcile when the stream said nothing.
		if res.Err != nil && m.err == nil {
			m.err = res.Err
			m.appendCard(card{Kind: cardError, Text: res.Err.Error(), At: time.Now()})
			return m, nil // appendCard rebuilt the view
		}
		m.rebuild() // reflect the duration recorded above
		return m, nil
	}

	m.finishTurn()
	if res.Err != nil {
		m.err = res.Err
		m.appendCard(card{Kind: cardError, Text: res.Err.Error(), At: time.Now()})
	} else {
		m.applyTurn(res.Result)
	}
	m.rebuild()
	return m, nil
}

func (m model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil
	}
	if strings.HasPrefix(text, "/") {
		return m.applyCommand(text)
	}
	if m.ag == nil {
		m.err = errAgentNil
		m.status = "agent is not configured"
		m.appendCard(card{Kind: cardNotice, Text: errAgentNil.Error(), At: time.Now()})
		return m, nil
	}
	if m.streaming {
		m.status = "Generating...Please wait"
		return m, nil
	}

	// The agent appends the user message to its own history, so the TUI owns
	// the user side of the view: echo it before clearing the input.
	m.appendCard(card{Kind: cardUser, Text: text, At: time.Now()})
	m.input.Reset()
	m.liveBlocks = nil
	m.usage = llm.TokenUsage{}

	m.turn++
	m.streaming = true
	m.follow = true
	m.dirty = false
	m.err = nil
	m.status = ""
	m.started = time.Now()

	turnCtx, cancel := context.WithCancel(m.tuiContext())
	m.turnCanc = cancel

	m.layout()
	m.rebuild()
	m.relayoutIfNeeded()

	return m, tea.Batch(doTurn(turnCtx, m.ag, m.turn, text), m.sp.Tick)
}
