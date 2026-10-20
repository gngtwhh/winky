package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"winky/internal/schema"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"winky/internal/agent"
	"winky/internal/llm"
)

// fakeAgent is a scripted Agent: Turn only records calls, while Events returns a
// channel the test drives directly.
type fakeAgent struct {
	ch chan schema.Event

	mu    sync.Mutex
	turns []string
	err   error
}

func newFakeAgent() *fakeAgent {
	return &fakeAgent{ch: make(chan schema.Event, 16)}
}

func (f *fakeAgent) Turn(_ context.Context, input string) (agent.Turn, error) {
	f.mu.Lock()
	f.turns = append(f.turns, input)
	f.mu.Unlock()
	return agent.Turn{}, f.err
}

func (f *fakeAgent) Events() <-chan schema.Event { return f.ch }

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

// turnOf builds an agent.Turn with an optional reasoning block then a message
// block, so tests can construct the authoritative result concisely.
func turnOf(reasoning, output string) agent.Turn {
	var blocks []agent.Block
	if reasoning != "" {
		blocks = append(blocks, agent.Block{Kind: agent.BlockReasoning, ID: "reasoning", Text: reasoning})
	}
	if output != "" {
		blocks = append(blocks, agent.Block{Kind: agent.BlockMessage, ID: "message", Text: output})
	}
	return agent.Turn{Blocks: blocks}
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
	return feed(
		t, m,
		AgentEvent{Event: schema.ReasoningDelta{Text: "think 1\n"}},
		AgentEvent{Event: schema.ReasoningDelta{Text: "think 2\n"}},
		AgentEvent{Event: schema.OutputDelta{Text: "hello "}},
		AgentEvent{Event: schema.OutputDelta{Text: "world"}},
		AgentEvent{Event: schema.TurnEnd{Turn: turnOf("think 1\nthink 2", "hello world")}},
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

	next, cmd := m.Update(AgentEvent{Event: schema.TurnStart{Timestamp: time.Now()}})
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

// 2. Blocks appear lazily as deltas arrive, grouped by ID; summary is its own
// collapsible block; TurnEnd reconciles with the authoritative Turn.
func TestBlocksStreamAndReconcile(t *testing.T) {
	ag := newFakeAgent()
	m := newTestModel(t, ag)

	m = feed(t, m, AgentEvent{Event: schema.TurnStart{Timestamp: time.Now()}})
	if len(m.history) != 0 {
		t.Fatalf("no cards should be created eagerly, got %d", len(m.history))
	}

	// Same-ID reasoning deltas append to one live block.
	m = feed(t, m, AgentEvent{Event: schema.ReasoningDelta{ItemID: "r1", Text: "think "}})
	m = feed(t, m, AgentEvent{Event: schema.ReasoningDelta{ItemID: "r1", Text: "hard"}})
	if c := cardAt(t, m, cardReasoning); !c.Live || !c.Expanded || c.Text != "think hard" {
		t.Fatalf("reasoning card = %+v, want live expanded text %q", c, "think hard")
	}

	// Summary is a distinct collapsible block.
	m = feed(t, m, AgentEvent{Event: schema.ReasoningSummaryDelta{ItemID: "r1", Text: "summary text"}})
	if c := cardAt(t, m, cardSummary); !c.Live || !c.Expanded || c.Text != "summary text" {
		t.Fatalf("summary card = %+v, want live expanded text %q", c, "summary text")
	}

	// Output is a separate message block.
	m = feed(t, m, AgentEvent{Event: schema.OutputDelta{ItemID: "m1", Text: "answer"}})
	if c := cardAt(t, m, cardAssistant); !c.Live || c.Text != "answer" {
		t.Fatalf("assistant card = %+v, want live text %q", c, "answer")
	}

	// TurnEnd reconciles with the authoritative ordered Turn.
	end := agent.Turn{Blocks: []agent.Block{
		{Kind: agent.BlockReasoning, ID: "r1", Text: "think hard"},
		{Kind: agent.BlockReasoningSummary, ID: "r1", Text: "summary text"},
		{Kind: agent.BlockMessage, ID: "m1", Text: "answer"},
	}}
	m = feed(t, m, AgentEvent{Event: schema.TurnEnd{Turn: end}})

	if c := cardAt(t, m, cardReasoning); c.Live || c.Expanded {
		t.Fatalf("reasoning card after TurnEnd = %+v, want folded", c)
	}
	if c := cardAt(t, m, cardSummary); c.Live || c.Expanded {
		t.Fatalf("summary card after TurnEnd = %+v, want folded", c)
	}
	if got := cardAt(t, m, cardAssistant).Text; got != "answer" {
		t.Fatalf("assistant text = %q, want %q", got, "answer")
	}
	if m.streaming {
		t.Fatal("model still streaming after TurnEnd")
	}
}

// A click on the reasoning label toggles it, and only that card.
func TestClickTogglesReasoningCard(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	m = feed(t, m, AgentEvent{Event: schema.TurnStart{Timestamp: time.Now()}})
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
	m = feed(t, m, AgentEvent{Event: schema.OutputDelta{Text: "late"}})

	next, _ = m.Update(turnResult{TurnID: staleTurn, Result: agent.Turn{}, Err: errors.New("boom")})
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
	// Count drawn rows only: chromeGap is layout slack, not a rendered row.
	if used := headerRows + statusRows + usageRows + m.input.Height() + m.vp.Height(); used > m.height {
		t.Fatalf("chrome uses %d rows of %d", used, m.height)
	}
}

// The status line must never exceed the terminal width.
func TestViewFitsWidth(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	m.usage = llm.TokenUsage{PromptTokens: 1234, CompletionTokens: 340, TotalTokens: 1574, CachedTokens: 680}
	m.totalUsage = llm.TokenUsage{PromptTokens: 12340, CompletionTokens: 2100, TotalTokens: 14440, CachedTokens: 7404}
	m = feed(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})

	for name, s := range map[string]string{
		"status": m.statusView(),
		"header": m.headerView(),
		"usage":  m.usageView(),
	} {
		if got := displayWidth(s); got > 40 {
			t.Fatalf("%s width = %d, want <= 40", name, got)
		}
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
	m = feed(t, m, AgentEvent{Event: schema.TurnStart{Timestamp: time.Now()}})
	m = feed(t, m, AgentEvent{Event: schema.TurnEnd{Turn: turnOf("r", "hi")}})

	next, _ := m.Update(
		turnResult{
			TurnID:   m.turn,
			Result:   turnOf("r", "hi"),
			Duration: 1500 * time.Millisecond,
		},
	)
	m = next.(model)

	c := cardAt(t, m, cardAssistant)
	if c.Duration != 1500*time.Millisecond {
		t.Fatalf("assistant duration = %v, want 1.5s", c.Duration)
	}
	if label := c.labelText(); !strings.Contains(label, "1.5s") {
		t.Fatalf("assistant label = %q, want it to contain %q", label, "1.5s")
	}
}

// The usage row shows this turn's figures and the session total once reported.
func TestUsageRowShowsTurnAndSession(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	m = feed(t, m, AgentEvent{Event: schema.TurnStart{Timestamp: time.Now()}})
	m = feed(
		t, m, AgentEvent{Event: schema.TurnEnd{
			Turn:       turnOf("r", "hi"),
			Usage:      llm.TokenUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500, CachedTokens: 500},
			TotalUsage: llm.TokenUsage{PromptTokens: 2000, CompletionTokens: 1000, TotalTokens: 3000, CachedTokens: 1000},
		}},
	)

	got := m.usageView()
	if !strings.Contains(got, "This turn ↑1.0k ↓500 · Cache 50%") {
		t.Fatalf("usage row = %q, want current-turn figures", got)
	}
	if !strings.Contains(got, "Total ↑2.0k ↓1.0k · Cache 50%") {
		t.Fatalf("usage row = %q, want session figures", got)
	}
}

// Usage must be recorded even when the command result settles the turn before
// the terminal event arrives.
func TestUsageRecordedAfterTurnResultSettles(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	m = feed(t, m, AgentEvent{Event: schema.TurnStart{Timestamp: time.Now()}})

	next, _ := m.Update(
		turnResult{
			TurnID:   m.turn,
			Result:   turnOf("r", "hi"),
			Duration: time.Second,
		},
	)
	m = next.(model)
	if m.streaming {
		t.Fatal("turnResult did not settle the turn")
	}

	m = feed(
		t, m, AgentEvent{Event: schema.TurnEnd{
			Turn:       turnOf("r", "hi"),
			Usage:      llm.TokenUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, CachedTokens: 50},
			TotalUsage: llm.TokenUsage{PromptTokens: 200, CompletionTokens: 100, TotalTokens: 300, CachedTokens: 100},
		}},
	)

	if m.usage.TotalTokens != 150 || m.totalUsage.TotalTokens != 300 {
		t.Fatalf("usage not recorded after settle: usage=%+v total=%+v", m.usage, m.totalUsage)
	}
}

// A new turn clears the per-turn usage but keeps the session total.
func TestUsageResetsPerTurn(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	m = feed(t, m, AgentEvent{Event: schema.TurnStart{Timestamp: time.Now()}})
	m = feed(
		t, m, AgentEvent{Event: schema.TurnEnd{
			Turn:       turnOf("r", "hi"),
			Usage:      llm.TokenUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, CachedTokens: 50},
			TotalUsage: llm.TokenUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, CachedTokens: 50},
		}},
	)
	if m.usage.TotalTokens == 0 || m.totalUsage.TotalTokens == 0 {
		t.Fatal("usage not set after first turn")
	}

	m = feed(t, m, AgentEvent{Event: schema.TurnStart{Timestamp: time.Now()}})
	if m.usage.TotalTokens != 0 {
		t.Fatalf("per-turn usage not reset on new turn: %+v", m.usage)
	}
	if m.totalUsage.TotalTokens == 0 {
		t.Fatal("session total must survive a new turn")
	}
}

// A failed turn still records the usage the provider reported before failing.
func TestFailedTurnRecordsUsage(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	m = feed(t, m, AgentEvent{Event: schema.TurnStart{Timestamp: time.Now()}})
	m = feed(
		t, m, AgentEvent{Event: schema.TurnFailed{
			Err:        errors.New("boom"),
			Usage:      llm.TokenUsage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110},
			TotalUsage: llm.TokenUsage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110},
		}},
	)
	if m.usage.TotalTokens != 110 || m.totalUsage.TotalTokens != 110 {
		t.Fatalf("failed-turn usage not recorded: usage=%+v total=%+v", m.usage, m.totalUsage)
	}
}

// A cancelled turn must drop its usage entirely.
func TestCancelledTurnDropsUsage(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	m.input.SetValue("hi")
	next, _ := m.Update(keyPress(tea.KeyEnter, 0))
	m = next.(model)
	next, _ = m.Update(keyPress(tea.KeyEsc, 0))
	m = next.(model)

	m = feed(
		t, m, AgentEvent{Event: schema.TurnEnd{
			Turn:       turnOf("r", "hi"),
			Usage:      llm.TokenUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, CachedTokens: 50},
			TotalUsage: llm.TokenUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, CachedTokens: 50},
		}},
	)
	if m.usage.TotalTokens != 0 || m.totalUsage.TotalTokens != 0 {
		t.Fatalf("cancelled-turn usage recorded: usage=%+v total=%+v", m.usage, m.totalUsage)
	}
}

// The header shows the model and, when set, the working directory.
func TestHeaderShowsWorkDir(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	m.workDir = "D:/data/code"
	m.home = "C:/Users/me"
	if got := m.headerView(); !strings.Contains(got, "winky · test/model · D:/data/code") {
		t.Fatalf("header = %q, want model and workdir", got)
	}
}

// The input defaults to one line, grows up to maxInputHeight, and keeps
// accepting newlines (scrolling) beyond that instead of blocking them.
func TestInputGrowsAndScrolls(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	if h := m.input.Height(); h != minInputHeight {
		t.Fatalf("empty input height = %d, want %d", h, minInputHeight)
	}

	for i := 0; i < 5; i++ {
		m = feed(t, m, keyPress('j', tea.ModCtrl))
	}

	if h := m.input.Height(); h != maxInputHeight {
		t.Fatalf("input height = %d, want capped at %d", h, maxInputHeight)
	}
	if got := strings.Count(m.input.Value(), "\n"); got != 5 {
		t.Fatalf("input newlines = %d, want 5 (newlines must not be blocked)", got)
	}
}
