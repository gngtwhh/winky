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
	"winky/internal/tool"
)

// fakeAgent is a scripted Agent: Turn only records calls, while Events returns a
// channel the test drives directly.
type fakeAgent struct {
	ch chan schema.Event

	mu    sync.Mutex
	turns []string
	err   error

	compacts int
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

// Compact records a manual compaction request.
func (f *fakeAgent) Compact(_ context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.compacts++
	return true, nil
}

func (f *fakeAgent) compactCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.compacts
}

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
	for _, c := range m.cards {
		if c.Kind == kind {
			return c
		}
	}
	t.Fatalf("no card of kind %v in %d cards", kind, len(m.cards))
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
	if len(m.cards) != 0 {
		t.Fatalf("no cards should be created eagerly, got %d", len(m.cards))
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

	before := len(m.cards)
	m = feed(t, m, AgentEvent{Event: schema.OutputDelta{Text: "late"}})

	next, _ = m.Update(turnResult{TurnID: staleTurn, Result: agent.Turn{}, Err: errors.New("boom")})
	m = next.(model)

	if len(m.cards) != before {
		t.Fatalf("history grew by %d cards after cancel, want 0", len(m.cards)-before)
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

// A cancelled turn is settled by TurnAborted, keeping partial output and usage.
func TestCancelledTurnSettlesWithAbortedUsage(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	m.input.SetValue("hi")
	next, _ := m.Update(keyPress(tea.KeyEnter, 0))
	m = next.(model)
	next, _ = m.Update(keyPress(tea.KeyEsc, 0))
	m = next.(model)
	if !m.cancelling || !m.streaming {
		t.Fatal("esc must keep the turn open until the agent settles it")
	}

	m = feed(
		t, m, AgentEvent{Event: schema.TurnAborted{
			Turn:       turnOf("r", "partial"),
			Usage:      llm.TokenUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, CachedTokens: 50},
			TotalUsage: llm.TokenUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, CachedTokens: 50},
		}},
	)
	if m.streaming || m.cancelling {
		t.Fatal("TurnAborted did not settle the turn")
	}
	if m.usage.TotalTokens != 150 || m.totalUsage.TotalTokens != 150 {
		t.Fatalf("aborted-turn usage not recorded: usage=%+v total=%+v", m.usage, m.totalUsage)
	}
	if got := cardAt(t, m, cardAssistant).Text; got != "partial" {
		t.Fatalf("assistant card = %q, want the partial output", got)
	}
	if got := cardAt(t, m, cardNotice).Text; got != "request aborted" {
		t.Fatalf("notice card = %q", got)
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

// /compact runs a manual compaction and clears the status when it succeeds.
func TestCompactCommand(t *testing.T) {
	ag := newFakeAgent()
	m := newTestModel(t, ag)

	m.input.SetValue("/compact")
	next, cmd := m.Update(keyPress(tea.KeyEnter, 0))
	m = next.(model)
	if cmd == nil {
		t.Fatal("/compact did not start a command")
	}
	if m.status != "compacting..." {
		t.Fatalf("status = %q, want compacting...", m.status)
	}

	msg := cmd()
	res, ok := msg.(compactResult)
	if !ok {
		t.Fatalf("command returned %T, want compactResult", msg)
	}
	if ag.compactCount() != 1 {
		t.Fatalf("Compact calls = %d, want 1", ag.compactCount())
	}

	next, _ = m.Update(res)
	m = next.(model)
	if m.status != "" {
		t.Fatalf("status = %q after a successful compaction", m.status)
	}
}

// /compact is refused mid-turn, and a no-op is reported instead of silently
// doing nothing.
func TestCompactRefusedWhileStreaming(t *testing.T) {
	ag := newFakeAgent()
	m := newTestModel(t, ag)
	m.streaming = true

	m.input.SetValue("/compact")
	next, cmd := m.Update(keyPress(tea.KeyEnter, 0))
	m = next.(model)
	if cmd != nil || ag.compactCount() != 0 {
		t.Fatalf("compaction ran while streaming: cmd=%v calls=%d", cmd, ag.compactCount())
	}
	if m.status != "wait for the current turn to finish" {
		t.Fatalf("status = %q", m.status)
	}

	m.streaming = false
	next, _ = m.Update(compactResult{Changed: false})
	m = next.(model)
	if m.status != "nothing to compact" {
		t.Fatalf("status = %q, want nothing to compact", m.status)
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

// Permission prompts answer with y/n only; esc cancels the turn.
func TestPermissionPromptKeys(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	m.input.SetValue("hi")
	next, _ := m.Update(keyPress(tea.KeyEnter, 0))
	m = next.(model)

	reply := make(chan bool, 1)
	m.perm = &permRequest{reply: reply}
	next, _ = m.Update(keyPress(tea.KeyEsc, 0))
	m = next.(model)

	select {
	case got := <-reply:
		t.Fatalf("esc answered the prompt with %v, want no answer", got)
	default:
	}
	if m.perm != nil {
		t.Fatal("esc did not close the permission prompt")
	}
	if !m.cancelling {
		t.Fatal("esc did not ask the turn to cancel")
	}

	m = newTestModel(t, newFakeAgent())
	m.input.SetValue("hi")
	next, _ = m.Update(keyPress(tea.KeyEnter, 0))
	m = next.(model)
	reply = make(chan bool, 1)
	m.perm = &permRequest{reply: reply}
	next, _ = m.Update(keyPress('n', 0))
	m = next.(model)
	if got, ok := <-reply; !ok || got {
		t.Fatalf("n answered %v, want false", got)
	}
	if m.perm != nil {
		t.Fatal("n did not close the permission prompt")
	}
	if m.cancelling {
		t.Fatal("n must not cancel the turn")
	}
}

// /? lists every command, so the list cannot drift from what is dispatchable.
func TestHelpListsEveryCommand(t *testing.T) {
	for _, name := range []string{"/?", "/help"} {
		m := newTestModel(t, newFakeAgent())

		m.input.SetValue(name)
		next, _ := m.Update(keyPress(tea.KeyEnter, 0))
		m = next.(model)

		hint := m.cards[len(m.cards)-1]
		if hint.Kind != cardNotice {
			t.Fatalf("%s produced a %v card, want a notice", name, hint.Kind)
		}
		for _, c := range commands() {
			for _, n := range c.names {
				if !strings.Contains(hint.Text, n) {
					t.Fatalf("%s listing is missing %s:\n%s", name, n, hint.Text)
				}
			}
		}
	}
}

// Any alias in the table dispatches to the same command.
func TestCommandAliases(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	m.policy = tool.NewPolicy(tool.ReadOnly)

	m.input.SetValue("/write")
	next, _ := m.Update(keyPress(tea.KeyEnter, 0))
	m = next.(model)

	if m.status != "permission: workspace" {
		t.Fatalf("status = %q, want the workspace permission", m.status)
	}
}

// A trailing argument is ignored, and an unknown command points at the listing.
func TestUnknownCommandPointsAtHelp(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	m.policy = tool.NewPolicy(tool.ReadOnly)

	m.input.SetValue("/full because I said so")
	next, _ := m.Update(keyPress(tea.KeyEnter, 0))
	m = next.(model)
	if m.status != "permission: full" {
		t.Fatalf("status = %q, want the full permission", m.status)
	}

	m.input.SetValue("/nope")
	next, _ = m.Update(keyPress(tea.KeyEnter, 0))
	m = next.(model)
	if !strings.Contains(m.status, "try /?") {
		t.Fatalf("status = %q, want a pointer to /?", m.status)
	}
}

// A tool card shows how long the call took and how long it was allowed to take,
// right-aligned so the numbers line up down the scrollback.
func TestToolCardShowsElapsedAndBudget(t *testing.T) {
	c := card{
		Kind:     cardTool,
		Title:    "shell",
		Duration: 1200 * time.Millisecond,
		Budget:   10 * time.Minute,
	}

	label := c.lines(40)[0].text
	if !strings.Contains(label, "1.2s / 10m") {
		t.Fatalf("label = %q, want elapsed and budget", label)
	}
	if got := displayWidth(label); got != 40 {
		t.Fatalf("label width = %d, want it right-aligned to 40", got)
	}
}

// A call the runner never bounded prints "-" for the limit instead of implying
// one exists.
func TestToolCardWithoutBudgetShowsDash(t *testing.T) {
	c := card{Kind: cardTool, Title: "echo", Duration: time.Second}

	if label := c.lines(40)[0].text; !strings.Contains(label, "1s / -") {
		t.Fatalf("label = %q, want a dash for the missing budget", label)
	}
}

// A call that never ran has nothing to report and gets no timing at all.
func TestToolCardWithoutTiming(t *testing.T) {
	c := card{Kind: cardTool, Title: "echo"}

	if label := c.lines(40)[0].text; strings.Contains(label, "/") {
		t.Fatalf("label = %q, want no timing", label)
	}
}

// The budget reaches the card from the tool result event.
func TestToolResultEventCarriesBudget(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	m.handleEvent(schema.TurnStart{})

	m.handleEvent(schema.ToolCallStart{CallID: "c1", Name: "shell"})
	m.handleEvent(schema.ToolResult{
		CallID: "c1", Name: "shell", Output: "ok",
		Duration: 500 * time.Millisecond, Budget: 30 * time.Second,
	})

	last := m.cards[len(m.cards)-1]
	if last.Budget != 30*time.Second || last.Duration != 500*time.Millisecond {
		t.Fatalf("card = %+v, want the event's timing", last)
	}
}

// The stopwatch starts when the call starts, not when the model began writing
// its arguments.
func TestStopwatchStartsAtToolCallBegin(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	m.handleEvent(schema.TurnStart{})
	m.handleEvent(schema.ToolCallStart{CallID: "c1", Name: "shell"})

	created := m.cards[len(m.cards)-1]
	if created.Live {
		t.Fatal("a tool card is live before its call starts")
	}
	if got := created.lines(60)[0].text; strings.Contains(got, "/") {
		t.Fatalf("label = %q, want no timing before the call starts", got)
	}

	m.handleEvent(schema.ToolCallBegin{CallID: "c1", Name: "shell", Budget: 2 * time.Minute})

	running := m.cards[len(m.cards)-1]
	if !running.Live {
		t.Fatal("card is not live after the call started")
	}
	if running.Budget != 2*time.Minute {
		t.Fatalf("budget = %v, want 2m", running.Budget)
	}
	if got := running.lines(60)[0].text; !strings.HasSuffix(got, "/ 2m") {
		t.Fatalf("label = %q, want the limit while running", got)
	}
	if !m.hasRunningTool() {
		t.Fatal("hasRunningTool = false while a call runs")
	}

	m.handleEvent(schema.ToolResult{
		CallID: "c1", Name: "shell", Output: "ok",
		Duration: 1500 * time.Millisecond, Budget: 2 * time.Minute,
	})

	done := m.cards[len(m.cards)-1]
	if done.Live {
		t.Fatal("card is still live after the result")
	}
	if got := done.lines(60)[0].text; !strings.HasSuffix(got, "1.5s / 2m") {
		t.Fatalf("label = %q, want the settled timing", got)
	}
	if m.hasRunningTool() {
		t.Fatal("hasRunningTool = true after the call settled")
	}
}

// The rebuild tick refreshes a running call's stopwatch even when no output is
// streaming: the viewport is a rendered string, so nothing changes until the
// scrollback is rebuilt.
func TestTickRefreshesTheRunningStopwatch(t *testing.T) {
	m := newTestModel(t, newFakeAgent())
	m.handleEvent(schema.TurnStart{})
	m.handleEvent(schema.ToolCallStart{CallID: "c1", Name: "shell"})
	m.handleEvent(schema.ToolCallBegin{CallID: "c1", Name: "shell", Budget: 2 * time.Minute})

	// Pretend the call has already been running for three seconds, and that no
	// delta is pending.
	m.cards[len(m.cards)-1].At = time.Now().Add(-3 * time.Second)
	m.dirty = false

	next, cmd := m.Update(tickMsg{ID: m.turn})
	m = next.(model)

	if cmd == nil {
		t.Fatal("the tick did not re-arm while a turn is streaming")
	}
	if view := m.vp.View(); !strings.Contains(view, "3s / 2m") {
		t.Fatalf("viewport does not show the running stopwatch:\n%s", view)
	}
}
