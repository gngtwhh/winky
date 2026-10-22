package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"winky/internal/llm"
	"winky/internal/schema"
	"winky/internal/transcript"
)

const (
	// contextMarginTokens is headroom below the window for framing the
	// estimator cannot see.
	contextMarginTokens = 1024

	defaultTriggerPercent   = 80
	defaultHardPercent      = 92
	defaultKeepPercent      = 30
	defaultMaxSummaryTokens = 1024

	// maxCompactionsPerTurn bounds the extra model calls one turn spends on
	// summarizing, so a retry loop is impossible.
	maxCompactionsPerTurn = 2

	// minKeepTokens keeps a small window from compacting down to nothing.
	minKeepTokens = 1024
)

// CompactionOptions configures automatic context compaction. Zero values fall
// back to the defaults above.
type CompactionOptions struct {
	Enabled          bool
	TriggerPercent   int    // request size that triggers compaction at a turn boundary
	HardPercent      int    // request size that also triggers between steps
	KeepPercent      int    // share of the trigger limit kept verbatim
	MaxPromptTokens  int    // optional absolute cap on the trigger limit
	MaxSummaryTokens int    // cap on the generated summary
	Model            string // summary model; empty uses the agent's model
}

// WithCompaction enables automatic context compaction.
func WithCompaction(o CompactionOptions) Option {
	return func(a *Agent) { a.compact = o }
}

// WithContextWindow sets the model's context window in tokens. Zero means
// unknown, which disables compaction.
func WithContextWindow(tokens int) Option {
	return func(a *Agent) { a.contextWindow = tokens }
}

func (a *Agent) normalizeCompaction() {
	o := &a.compact
	if o.TriggerPercent <= 0 {
		o.TriggerPercent = defaultTriggerPercent
	}
	if o.TriggerPercent > 95 {
		o.TriggerPercent = 95
	}
	if o.HardPercent <= o.TriggerPercent {
		o.HardPercent = o.TriggerPercent + 8
	}
	if o.HardPercent > 100 {
		o.HardPercent = 100
	}
	if o.KeepPercent <= 0 {
		o.KeepPercent = defaultKeepPercent
	}
	if o.KeepPercent > 90 {
		o.KeepPercent = 90
	}
	if o.MaxSummaryTokens <= 0 {
		o.MaxSummaryTokens = defaultMaxSummaryTokens
	}
	if o.MaxPromptTokens < 0 {
		o.MaxPromptTokens = 0
	}
}

// compactionLimit returns the request size that triggers compaction: the soft
// limit before a turn, the hard one also between steps.
func (a *Agent) compactionLimit(hard bool) (int, bool) {
	if !a.compact.Enabled || a.contextWindow <= 0 {
		return 0, false
	}
	usable := a.contextWindow - a.maxOutputTokens - contextMarginTokens
	if usable <= 0 {
		return 0, false
	}
	percent := a.compact.TriggerPercent
	if hard {
		percent = a.compact.HardPercent
	}
	limit := usable * percent / 100
	if a.compact.MaxPromptTokens > 0 && limit > a.compact.MaxPromptTokens {
		limit = a.compact.MaxPromptTokens
	}
	return limit, true
}

// keepBudget is how many tokens stay verbatim after a compaction.
func (a *Agent) keepBudget() int {
	limit, ok := a.compactionLimit(false)
	if !ok {
		return 0
	}
	budget := limit * a.compact.KeepPercent / 100
	if budget < minKeepTokens {
		budget = minKeepTokens
	}
	return budget
}

// estimateContext estimates the next prompt size from the last exact provider
// count plus the change the local estimator has seen since then, so the system
// prompt and tool schemas cancel out.
func (a *Agent) estimateContext() int {
	est := a.estimateRequest(a.history.Messages())
	if a.lastPromptTokens <= 0 {
		return est
	}
	est = a.lastPromptTokens + est - a.lastPromptEstimate
	if est < 0 {
		return 0
	}
	return est
}

func (a *Agent) estimateRequest(msgs []llm.Message) int {
	return 8 + estimateMessages(msgs) + estimateText(a.systemPrompt) + a.toolTokens
}

// maybeCompact summarizes the oldest history when the next request would cross
// the soft (turn boundary) or hard (between steps) limit. It is bounded per
// turn, and a failed summary leaves the history untouched.
func (a *Agent) maybeCompact(ctx context.Context, hard bool) error {
	if a.tsRecorder == nil || a.compactionsThisTurn >= maxCompactionsPerTurn {
		return nil
	}
	limit, ok := a.compactionLimit(hard)
	if !ok {
		return nil
	}
	before := a.estimateContext()
	if before < limit {
		return nil
	}
	a.compactionsThisTurn++

	plan, ok, err := a.planCompaction(a.keepBudget())
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	summary, usage, err := a.summarize(ctx, plan.replaced)
	if err != nil {
		if before < a.contextWindow-a.maxOutputTokens {
			return nil
		}
		return err
	}
	a.totalUsage = a.totalUsage.Add(usage)

	kind := "auto_turn"
	if hard {
		kind = "auto_step"
	}
	return a.recordCompaction(plan, before, summary, kind)
}

// Compact summarizes the oldest history now, regardless of the automatic
// thresholds or whether automatic compaction is enabled. It reports false when
// there is nothing to replace, and does not consume the automatic per-turn
// budget. It must not run while a Turn is in flight.
func (a *Agent) Compact(ctx context.Context) (bool, error) {
	if a.tsRecorder == nil {
		return false, errors.New("compaction needs a transcript recorder")
	}
	if err := a.loadHistory(); err != nil {
		return false, err
	}

	plan, ok, err := a.planCompaction(a.manualKeepBudget())
	if err != nil || !ok {
		return false, err
	}

	before := a.estimateContext()
	summary, usage, err := a.summarize(ctx, plan.replaced)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return false, cerr
		}
		return false, err
	}
	a.totalUsage = a.totalUsage.Add(usage)

	if err := a.recordCompaction(plan, before, summary, "manual"); err != nil {
		return false, err
	}
	return true, nil
}

// manualKeepBudget is the tail a manual compaction keeps: a quarter of the
// current context, so the command actually shrinks it instead of leaving almost
// everything in place.
func (a *Agent) manualKeepBudget() int {
	budget := a.estimateContext() / 4
	if budget < minKeepTokens {
		budget = minKeepTokens
	}
	return budget
}

// recordCompaction persists one compaction event and reports the new size.
func (a *Agent) recordCompaction(plan compactionPlan, before int, summary, kind string) error {
	after := before - estimateMessages(plan.replaced) + estimateText(summary)
	if after < 0 {
		after = 0
	}
	err := a.record(transcript.CompactionSummary{
		Summary:          summary,
		ReplacedStartSeq: plan.start,
		ReplacedEndSeq:   plan.end,
		Timestamp:        time.Now(),
		Kind:             kind,
		Model:            a.summaryModel(),
		TokensBefore:     before,
		TokensAfter:      after,
	})
	if err != nil {
		return err
	}
	a.emit(schema.ContextCompacted{Before: before, After: after, Kind: kind})
	return nil
}

func (a *Agent) summaryModel() string {
	if a.compact.Model != "" {
		return a.compact.Model
	}
	return a.model
}

// compactionPlan is one decided replacement: the sequence range to drop and the
// messages it covers.
type compactionPlan struct {
	start, end int64
	replaced   []llm.Message
}

// planCompaction chooses how much to replace, preferring whole turns and
// falling back to step boundaries when one turn alone is too large.
func (a *Agent) planCompaction(keepBudget int) (compactionPlan, bool, error) {
	entries, turns, steps := a.history.snapshot()
	if len(entries) < 2 || entries[0].seq <= 0 || keepBudget <= 0 {
		return compactionPlan{}, false, nil
	}

	kept := pickTail(entries, buildUnits(entries, turns), keepBudget)
	if acceptedCut(entries, kept, keepBudget) {
		return newPlan(entries, kept), true, nil
	}

	kept = pickTail(entries, buildUnits(entries, steps), keepBudget)
	if acceptedCut(entries, kept, keepBudget) {
		return newPlan(entries, kept), true, nil
	}
	if kept == 0 || kept >= len(entries) {
		// The whole history already fits the budget, so there is nothing worth
		// replacing. This is what a manual compaction reports as a no-op.
		return compactionPlan{}, false, nil
	}
	return compactionPlan{}, false, fmt.Errorf(
		"context compaction: the most recent history alone exceeds the budget; " +
			"raise contextWindow, lower maxOutputTokens, or lower keepPercent",
	)
}

// acceptedCut rejects a cut that replaces nothing or keeps more than twice the
// budget, which would trigger again immediately.
func acceptedCut(entries []historyEntry, kept, keepBudget int) bool {
	return kept > 0 && kept < len(entries) && estimateEntries(entries[kept:]) <= 2*keepBudget
}

func newPlan(entries []historyEntry, kept int) compactionPlan {
	return compactionPlan{
		start: entries[0].seq,
		end:   entries[kept-1].seq,
		// Request-safe view: a dangling tool call must not reach the summarizer.
		replaced: messagesOf(entries[:kept]),
	}
}

// unit is a run of consecutive entries that compaction may cut before.
type unit struct {
	first, last int
}

// buildUnits splits entries at the given sequence boundaries, cutting before the
// first entry at or after each one. That covers both a turn start (the user
// message itself) and a step start (which precedes the assistant message).
func buildUnits(entries []historyEntry, starts []int64) []unit {
	if len(entries) == 0 {
		return nil
	}
	units := make([]unit, 0, len(starts)+1)
	first := 0
	for _, s := range starts {
		i := sort.Search(len(entries), func(i int) bool { return entries[i].seq >= s })
		if i <= first || i >= len(entries) {
			continue
		}
		units = append(units, unit{first: first, last: i - 1})
		first = i
	}
	return append(units, unit{first: first, last: len(entries) - 1})
}

// pickTail returns the first entry to keep: units are added newest first until
// the next would exceed the budget, and at least one is always kept.
func pickTail(entries []historyEntry, units []unit, keepBudget int) int {
	kept := len(entries)
	tokens := 0
	for i := len(units) - 1; i >= 0; i-- {
		u := units[i]
		t := estimateEntries(entries[u.first : u.last+1])
		if tokens > 0 && tokens+t > keepBudget {
			break
		}
		tokens += t
		kept = u.first
	}
	return kept
}

const summarySystemPrompt = `You compress the history of a coding-agent session so the agent can keep working with less context.

Write a dense factual handover, not a narrative. Keep, when present:
- the user's goals, requirements and constraints, quoted verbatim where they are exact;
- decisions already made and the reasons behind them;
- files, paths, symbols and commands that were touched, with their current state;
- errors encountered and how they were resolved or are still open;
- work that is unfinished and what the next step was.

Preserve identifiers exactly (paths, function names, flags, error strings, versions). Omit tool output bodies, full file contents and reasoning. Do not invent anything that is not in the conversation. Reply with the summary only, no preamble.`

const summaryInstruction = `Summarize the conversation above as the handover described by your instructions.`

// summarize runs one silent model call over the replaced messages. It bypasses
// Agent.stream so no delta reaches the event channel.
func (a *Agent) summarize(ctx context.Context, msgs []llm.Message) (string, llm.TokenUsage, error) {
	seq := make([]llm.Message, 0, len(msgs)+1)
	seq = append(seq, msgs...)
	seq = append(seq, llm.Message{Role: llm.RoleUser, Content: summaryInstruction})

	req := llm.Request{
		Model:           a.summaryModel(),
		Messages:        seq,
		SystemPrompt:    summarySystemPrompt,
		MaxOutputTokens: a.compact.MaxSummaryTokens,
	}

	ch, err := a.streamer.Stream(ctx, req)
	if err != nil {
		return "", llm.TokenUsage{}, fmt.Errorf("summarize: %w", err)
	}

	var (
		resp      *llm.Response
		usage     llm.TokenUsage
		stop      = llm.StopReasonUnknown
		streamErr error
	)
	for chunk := range ch {
		end, ok := chunk.(llm.ResponseEnd)
		if !ok {
			continue
		}
		stop = end.StopReason
		streamErr = end.Err
		resp = end.Response
		if resp != nil && resp.Usage != nil {
			usage = *resp.Usage
		}
	}
	if stop != llm.StopReasonDone {
		if streamErr == nil {
			streamErr = fmt.Errorf("stream stopped: %s", stop)
		}
		return "", usage, fmt.Errorf("summarize: %w", streamErr)
	}

	text := strings.TrimSpace(turnFromResponse(resp).Output())
	if text == "" {
		return "", usage, errors.New("summarize: model returned an empty summary")
	}
	return truncateSummary(text, a.compact.MaxSummaryTokens), usage, nil
}

// truncateSummary caps the summary at roughly maxTokens without splitting a
// rune; four bytes per token is a safe upper bound.
func truncateSummary(s string, maxTokens int) string {
	if maxTokens <= 0 {
		return s
	}
	max := maxTokens * 4
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// estimateText counts ASCII at four bytes per token and every non-ASCII rune as
// one token, which is close for CJK.
func estimateText(s string) int {
	if s == "" {
		return 0
	}
	ascii, wide := 0, 0
	for _, r := range s {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			wide++
		}
	}
	return (ascii+3)/4 + wide
}

func estimateMessages(msgs []llm.Message) int {
	total := 0
	for _, m := range msgs {
		total += 4 + estimateText(m.Content) + estimateText(m.Name)
		for _, tc := range m.ToolCalls {
			total += 4 + estimateText(tc.Name) + estimateText(tc.Arguments)
		}
	}
	return total
}

func estimateEntries(entries []historyEntry) int {
	return estimateMessages(messagesOf(entries))
}

// estimateTools counts the tool schemas, which are sent on every request but
// never appear in Messages.
func estimateTools(toolset Toolset) int {
	if toolset == nil {
		return 0
	}
	total := 0
	for _, spec := range toolset.Specs() {
		total += 16 + estimateText(spec.Name) + estimateText(spec.Description)
		if data, err := json.Marshal(spec.Parameters); err == nil {
			total += estimateText(string(data))
		}
	}
	return total
}
