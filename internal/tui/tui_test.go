package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"winky/internal/agent"
)

// fakeAgent is a scripted Agent: Turn only records calls, while Events returns a
// channel the test drives directly.
type fakeAgent struct {
	ch chan agent.Event

	mu    sync.Mutex
	turns []string
	err   error
}

func newFakeAgent() *fakeAgent {
	return &fakeAgent{ch: make(chan agent.Event, 16)}
}

func (f *fakeAgent) Turn(_ context.Context, input string) (agent.TurnResult, error) {
	f.mu.Lock()
	f.turns = append(f.turns, input)
	f.mu.Unlock()
	return agent.TurnResult{}, f.err
}

func (f *fakeAgent) Events() <-chan agent.Event { return f.ch }

func (f *fakeAgent) turnCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.turns)
}

func newTestModel(t *testing.T, ag Agent) model {
	t.Helper()
	m := newTuiModel(ag, false)
	m.width, m.height = 80, 24
	m.ready = true
	m.layout()
	return m
}

func newTuiModel(ag Agent, expand bool) model {
	t := &Tui{ag: ag, altScreen: true, evCh: make(chan AgentEvent, 64)}
	t.m = newModel(ag, newStyles(), "test/model", expand)
	t.m.eventChan = t.evCh
	t.m.altScreen = t.altScreen
	return t.m
}

func feed(t *testing.T, m model, msgs ...tea.Msg) model {
	t.Helper()
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		got, ok := next.(model)
		if !ok {
			t.Fatalf("Update returned %T, want model", next)
		}
		m = got
	}
	return m
}

// turnRound streams the deltas and end event of a turn that has already been
// started by a TurnStart event.
func turnRound(t *testing.T, m model) model {
	t.Helper()
	return feed(t, m,
		AgentEvent{Event: agent.ReasoningDelta{Text: "think 1\n"}},
		AgentEvent{Event: agent.ReasoningDelta{Text: "think 2\n"}},
		AgentEvent{Event: agent.OutputDelta{Text: "hello "}},
		AgentEvent{Event: agent.OutputDelta{Text: "world"}},
		AgentEvent{Event: agent.TurnEnd{Reasoning: "think 1\nthink 2", Output: "hello world"}},
	)
}

func cardAt(t *testing.T, m model, kind cardKind) card {
	t.Helper()
	for _, c := range m.history {
		if c.Kind == kind {
			return c
		}
	}
	t.Fatalf("no card of kind %v in %d cards", kind, len(m.history))
	return card{}
}

// 1. Every consumed AgentEvent must re-arm exactly one receiver, otherwise the
// agent's blocking emit would stall once the buffer drains.
func TestUpdateRearmsEventReceiver(t *testing.T) {
	ag := newFakeAgent()
	m := newTestModel(t, ag)

	next, cmd := m.Update(AgentEvent{Event: agent.TurnStart{Timestamp: time.Now()}})
	m = next.(model)
	if cmd == nil {
		t.Fatal("Update(AgentEvent) returned no command; the receiver was not re-armed")
	}

	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected a tea.BatchMsg, got %T", cmd())
	}

	// Closing the event channel lets the armed receiver resolve immediately:
	// a blocked receive is exactly what we are asserting the shape of.
	close(m.eventChan)

	receivers, settled := 0, 0
	for _, c := range batch {
		if c == nil {
			continue
		}
		switch c().(type) {
		case tea.QuitMsg: // waitEvent observed the closed channel
			receivers++
		case AgentEvent:
			settled++
		}
	}
	if receivers != 1 || settled != 0 {
		t.Fatalf("got %d receivers and %d undelivered events, want 1 and 0", receivers, settled)
	}
}

// 2. Reasoning streams inline while it arrives and folds away on the first
// output delta.
func TestReasoningCollapsesOnFirstOutputDelta(t *testing.T) {
	ag := newFakeAgent()
	m := newTestModel(t, ag)

	m = feed(t, m, AgentEvent{Event: agent.TurnStart{Timestamp: time.Now()}})
	if c := cardAt(t, m, cardReasoning); !c.Live || !c.Expanded {
		t.Fatalf("reasoning card = %+v, want live and expanded while streaming", c)
	}

	m = feed(t, m, AgentEvent{Event: agent.ReasoningDelta{Text: "a\nb"}})
	if c := cardAt(t, m, cardReasoning); !c.Live || !c.Expanded || c.Text != "a\nb" {
		t.Fatalf("reasoning card = %+v, want live text a\\nb", c)
	}

	m = feed(t, m, AgentEvent{Event: agent.OutputDelta{Text: "answer"}})
	if c := cardAt(t, m, cardReasoning); c.Live || c.Expanded {
		t.Fatalf("reasoning card = %+v, want folded once output started", c)
	}

	m = turnRound(t, m)
	if c := cardAt(t, m, cardReasoning); c.Live || c.Expanded {
		t.Fatalf("reasoning card after TurnEnd = %+v, want folded", c)
	}
	if got := cardAt(t, m, cardAssistant).Text; got != "hello world" {
		t.Fatalf("assistant text = %q, want %q", got, "hello world")
	}
	if m.streaming {
		t.Fatal("model still streaming after TurnEnd")
	}
}

// A click on the reasoning label toggles it, and only that card.
func TestClickTogglesReasoningCard(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	m = feed(t, m, AgentEvent{Event: agent.TurnStart{Timestamp: time.Now()}})
	m = turnRound(t, m)

	if len(m.hints) != 1 {
		t.Fatalf("got %d clickable hints, want 1", len(m.hints))
	}
	line := m.hints[0].line

	next, _ := m.Update(tea.MouseClickMsg(tea.Mouse{X: 3, Y: line + headerRows, Button: tea.MouseLeft}))
	m = next.(model)
	if c := cardAt(t, m, cardReasoning); !c.Expanded {
		t.Fatalf("card = %+v, want expanded after click", c)
	}

	next, _ = m.Update(tea.MouseClickMsg(tea.Mouse{X: 3, Y: line + headerRows, Button: tea.MouseLeft}))
	m = next.(model)
	if c := cardAt(t, m, cardReasoning); c.Expanded {
		t.Fatalf("card = %+v, want folded after second click", c)
	}
}

// 3. A cancelled turn must discard late events and its late result.
func TestCancelDiscardsStaleEvents(t *testing.T) {
	ag := newFakeAgent()
	m := newTestModel(t, ag)

	m.input.SetValue("hello")
	next, _ := m.Update(keyPress(tea.KeyEnter, 0))
	m = next.(model)
	if !m.streaming {
		t.Fatal("enter did not start a turn")
	}
	staleTurn := m.turn

	// esc with an empty (already reset) input cancels the turn.
	next, _ = m.Update(keyPress(tea.KeyEsc, 0))
	m = next.(model)
	if m.turnCanc != nil {
		t.Fatal("esc did not clear the turn cancel function")
	}

	before := len(m.history)
	m = feed(t, m, AgentEvent{Event: agent.OutputDelta{Text: "late"}})

	next, _ = m.Update(turnResult{TurnID: staleTurn, Result: agent.TurnResult{Output: "late"}, Err: errors.New("boom")})
	m = next.(model)

	if len(m.history) != before {
		t.Fatalf("history grew by %d cards after cancel, want 0", len(m.history)-before)
	}
	if m.err != nil {
		t.Fatalf("error = %v, want nil for a discarded result", m.err)
	}
}

// 4. enter sends; ctrl+j inserts a newline without sending.
func TestEnterSendsAndCtrlJInsertsNewline(t *testing.T) {
	ag := newFakeAgent()
	m := newTestModel(t, ag)
	m.input.SetValue("hi")

	next, cmd := m.Update(keyPress(tea.KeyEnter, 0))
	m = next.(model)
	if cmd == nil {
		t.Fatal("enter produced no command")
	}
	if !m.streaming {
		t.Fatal("enter did not put the model into the streaming state")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("input = %q, want empty after send", got)
	}
	if got := cardAt(t, m, cardUser).Text; got != "hi" {
		t.Fatalf("user card = %q, want %q", got, "hi")
	}

	// The command must carry this turn's id so a stale result can be dropped.
	// Submit returns a batch (turn + spinner kickoff); find the turn command.
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("turn command returned %T, want tea.BatchMsg", cmd())
	}
	var res turnResult
	found := false
	for _, c := range batch {
		if c == nil {
			continue
		}
		if tr, ok := c().(turnResult); ok {
			res = tr
			found = true
			break
		}
	}
	if !found {
		t.Fatal("batch did not contain the turn command")
	}
	if res.TurnID != m.turn {
		t.Fatalf("turn result id = %d, want %d", res.TurnID, m.turn)
	}
	if ag.turnCount() == 0 {
		t.Fatal("the turn command did not reach the agent")
	}

	// ctrl+j must reach the textarea and insert a line break.
	m = newTestModel(t, ag)
	m.input.SetValue("line")
	m = feed(t, m, keyPress('j', tea.ModCtrl))
	if got := m.input.Value(); !strings.Contains(got, "\n") {
		t.Fatalf("input = %q, want an inserted newline", got)
	}
	if m.streaming {
		t.Fatal("ctrl+j started a turn; it must only insert a newline")
	}
}

// 5. A tiny window must still leave a usable viewport.
func TestTinyWindowKeepsViewportUsable(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	m = feed(t, m, tea.WindowSizeMsg{Width: 10, Height: 5})

	if m.vp.Height() < minViewportHeight {
		t.Fatalf("viewport height = %d, want >= %d", m.vp.Height(), minViewportHeight)
	}
	if m.vp.Width() < 1 {
		t.Fatalf("viewport width = %d, want >= 1", m.vp.Width())
	}
	if used := headerRows + statusRows + chromeGap + m.input.Height() + m.vp.Height(); used > m.height {
		t.Fatalf("chrome uses %d rows of %d", used, m.height)
	}
}

// The status line must never exceed the terminal width.
func TestViewFitsWidth(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	m = feed(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	if got := displayWidth(m.statusView()); got > 40 {
		t.Fatalf("status width = %d, want <= 40", got)
	}
	if got := displayWidth(m.headerView()); got > 40 {
		t.Fatalf("header width = %d, want <= 40", got)
	}
}

func keyPress(code rune, mod tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Mod: mod})
}

// The placeholder hint must not use the default near-invisible foreground,
// which gets swallowed by the cursor-line background under empty input.
func TestInputPlaceholderIsReadable(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	got := m.input.Styles().Focused.Placeholder.GetForeground()
	want := lipgloss.Color("245")
	if got != want {
		t.Fatalf("placeholder foreground = %v, want %v", got, want)
	}
}

// The spinner must actually rotate: submit starts it, and each spinner.TickMsg
// advances the rendered frame and re-arms the timer.
func TestSpinnerRotates(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	m.input.SetValue("hi")

	next, cmd := m.Update(keyPress(tea.KeyEnter, 0))
	m = next.(model)
	if !m.streaming {
		t.Fatal("submit did not enter the streaming state")
	}

	// Submit must kick off the spinner with a command that resolves to a
	// spinner.TickMsg.
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("submit command = %T, want tea.BatchMsg", cmd())
	}
	sawTick := false
	for _, c := range batch {
		if c == nil {
			continue
		}
		if _, ok := c().(spinner.TickMsg); ok {
			sawTick = true
		}
	}
	if !sawTick {
		t.Fatal("submit did not start the spinner")
	}

	// A spinner tick must advance the rendered frame and re-arm the timer.
	before := m.sp.View()
	next, cmd = m.Update(spinner.TickMsg{ID: m.sp.ID()})
	m = next.(model)
	if m.sp.View() == before {
		t.Fatalf("spinner frame did not advance (still %q)", before)
	}
	if cmd == nil {
		t.Fatal("spinner tick did not re-arm")
	}
}

// The assistant card must show the turn's wall-clock duration once it settles,
// even though the duration only arrives with the command result after TurnEnd.
func TestAssistantCardShowsTurnDuration(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	m = feed(t, m, AgentEvent{Event: agent.TurnStart{Timestamp: time.Now()}})
	m = feed(t, m, AgentEvent{Event: agent.TurnEnd{Reasoning: "r", Output: "hi"}})

	next, _ := m.Update(turnResult{
		TurnID:   m.turn,
		Result:   agent.TurnResult{Reasoning: "r", Output: "hi"},
		Duration: 1500 * time.Millisecond,
	})
	m = next.(model)

	c := cardAt(t, m, cardAssistant)
	if c.Duration != 1500*time.Millisecond {
		t.Fatalf("assistant duration = %v, want 1.5s", c.Duration)
	}
	if label := c.labelText(); !strings.Contains(label, "1.5s") {
		t.Fatalf("assistant label = %q, want it to contain %q", label, "1.5s")
	}
}
