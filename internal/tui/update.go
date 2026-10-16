package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"winky/internal/agent"
)

// errAgentNil guards against running without a configured agent.
var errAgentNil = errors.New("tui: no agent configured")

// Update reduces every message. Exactly one waitEvent is in flight at any time:
// it is armed in Init and re-armed on every AgentEvent.
//
// Update returns the model by value, so every mutation helper below runs on the
// addressable local copy and the updated copy is returned.
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
		// handleEvent mutates through a pointer: Update's receiver is a copy,
		// so a value receiver there would silently discard every change.
		eventCmd := m.handleEvent(msg.Event)
		// Re-arm the receiver in the same step that consumed an event.
		return m, tea.Batch(eventCmd, waitEvent(m.eventChan))

	case turnResult:
		next, cmd := m.handleTurnResult(msg)
		return next, cmd

	case tickMsg:
		// The spinner re-arms itself through its own TickMsg, so a tick only
		// has to flush pending deltas and advance the spinner.
		var cmds []tea.Cmd
		if m.streaming {
			var cmd tea.Cmd
			m.sp, cmd = m.sp.Update(msg)
			cmds = append(cmds, cmd, tickCmd(msg.ID))
		}
		if m.dirty {
			m.rebuild()
		}
		return m, tea.Batch(cmds...)

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.MouseClickMsg, tea.MouseWheelMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg:
		return m.handleMouse(msg)

	case tea.PasteMsg:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.relayoutIfNeeded()
		return m, cmd
	}

	// Spinner frames and anything else the widgets care about.
	var cmds []tea.Cmd
	if m.streaming {
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		cmds = append(cmds, cmd)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	m.relayoutIfNeeded()
	return m, tea.Batch(cmds...)
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
			// Remember the abandoned turn so its late events and its command
			// result are both discarded.
			m.markCancelled()
			m.status = "本轮已取消"
		}
		return m, nil

	case key.Matches(msg, m.keys.Send) && k.Mod&tea.ModAlt == 0:
		// Plain enter sends; alt+enter falls through to the textarea.
		return m.submit()

	case key.Matches(msg, m.keys.ToggleAll):
		if m.toggleAllReasoning() {
			m.status = "已展开全部推理"
		} else {
			m.status = "已折叠全部推理"
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

	// Everything else (ctrl+j, ctrl+enter, arrows, home/end, paste, selection)
	// belongs to the textarea, which keeps the cursor visible on its own.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.relayoutIfNeeded()
	return m, cmd
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

// handleEvent mutates the model, so it runs on a pointer to the local copy held
// by Update (see the note there).
//
// Every branch except TurnStart is gated on m.streaming, which is cleared when a
// turn is cancelled or settled. That gate is what stops a cancelled turn's late
// deltas from appending cards or resurrecting the streaming state.
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
		m.reasoning = ""
		m.output = ""
		// Cards still marked live mean the previous turn never delivered a
		// TurnEnd or TurnFailed; fold them so the view cannot show two live
		// blocks at once.
		m.freezeLiveCards()
		m.appendCard(card{
			Kind:     cardReasoning,
			At:       m.started,
			Live:     true,
			Expanded: true, // streamed inline; collapses on the first output delta
		})
		m.appendCard(card{Kind: cardAssistant, At: m.started, Live: true})
		return tickCmd(m.turn)

	case agent.ReasoningDelta:
		if !m.streaming || m.cancelled[m.turn] {
			return nil
		}
		m.reasoning += ev.Text
		if c := m.liveCard(cardReasoning); c != nil {
			c.Text = m.reasoning
		}
		m.dirty = true
		return nil

	case agent.OutputDelta:
		if !m.streaming || m.cancelled[m.turn] {
			return nil
		}
		// First output delta: fold the reasoning away and start streaming the
		// answer. `Live` guarantees this happens exactly once per turn.
		if c := m.liveCard(cardReasoning); c != nil && c.Live {
			c.Live = false
			c.Expanded = m.expandReasoning
		}
		m.output += ev.Text
		if c := m.liveCard(cardAssistant); c != nil {
			c.Text = m.output
		}
		m.dirty = true
		return nil

	case agent.TurnEnd:
		if !m.streaming {
			return nil
		}
		// TurnEnd carries the authoritative full text; the deltas were only a
		// live preview.
		if c := m.liveCard(cardReasoning); c != nil {
			c.Text = ev.Reasoning
			c.Live = false
			c.Expanded = m.expandReasoning
		}
		if c := m.liveCard(cardAssistant); c != nil {
			c.Text = ev.Output
			c.Live = false
		}
		m.finishTurn()
		m.rebuild()
		return nil

	case agent.TurnFailed:
		if !m.streaming {
			return nil
		}
		m.finishTurn()
		m.err = ev.Err
		m.status = ""
		m.appendCard(card{Kind: cardError, Text: ev.Err.Error(), At: time.Now()})
		return nil
	}
	return nil
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
	if !m.streaming {
		// TurnEnd already settled this turn. Errors normally arrive through
		// TurnFailed, so only reconcile when the stream said nothing.
		if res.Err != nil && m.err == nil {
			m.err = res.Err
			m.appendCard(card{Kind: cardError, Text: res.Err.Error(), At: time.Now()})
		}
		return m, nil
	}

	m.finishTurn()
	if res.Err != nil {
		m.err = res.Err
		m.appendCard(card{Kind: cardError, Text: res.Err.Error(), At: time.Now()})
	} else {
		if c := m.liveCard(cardReasoning); c != nil {
			c.Text = res.Result.Reasoning
			c.Live = false
			c.Expanded = m.expandReasoning
		}
		if c := m.liveCard(cardAssistant); c != nil {
			c.Text = res.Result.Output
			c.Live = false
		}
	}
	m.rebuild()
	return m, nil
}

func (m model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil
	}
	if m.ag == nil {
		m.err = errAgentNil
		m.status = "未配置 agent"
		m.appendCard(card{Kind: cardNotice, Text: errAgentNil.Error(), At: time.Now()})
		return m, nil
	}
	if m.streaming {
		m.status = "正在生成中，请等待本轮结束"
		return m, nil
	}

	// The agent appends the user message to its own history, so the TUI owns
	// the user side of the view: echo it before clearing the input.
	m.appendCard(card{Kind: cardUser, Text: text, At: time.Now()})
	m.input.Reset()
	m.reasoning = ""
	m.output = ""

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

	return m, doTurn(turnCtx, m.ag, m.turn, text)
}
