package agent

import (
	"sort"

	"winky/internal/llm"
	"winky/internal/transcript"
)

// stats is the cost and context accounting the log records. It is folded out of
// the same events as the view, so a resumed session reports exactly what the
// live one did.
type stats struct {
	// total is every request the session paid for: each StepEnd, plus the
	// summarizing calls a CompactionSummary carries.
	total llm.TokenUsage

	// measured is the provider's input token count for the anchored request, and
	// estimated is what the local estimator said about that same request. Their
	// difference is the estimator's bias, carried forward by later estimates.
	measured  int
	estimated int

	// systemPromptTokens is the system prompt's local size at the anchor. The
	// system prompt can grow afterwards, so the estimate above must keep the one
	// the request actually carried.
	systemPromptTokens int

	anchored bool
}

// derive folds the log into the view the next request is built from, and into
// the accounting recorded beside it. It is the only producer of both.
func (a *Agent) derive() (*view, stats, error) {
	v := newView()
	var (
		s         stats
		stepSeq   int64 // the StepStart preceding the anchor StepEnd
		anchorSeq int64
		anchorAt  int  // entries right after the anchor compaction applied
		fromFold  bool // the anchor is a compaction, not a provider count
	)
	for _, ev := range a.log {
		if err := v.apply(ev); err != nil {
			// s holds what the log said up to the broken event, which is the best
			// total a turn that is already failing can report.
			return nil, s, err
		}
		switch e := ev.(type) {
		case transcript.StepStart:
			stepSeq = ev.Seq()

		case transcript.StepEnd:
			if e.Usage == nil {
				continue
			}
			s.total = s.total.Add(llmUsage(*e.Usage))
			if e.Usage.PromptTokens > 0 {
				s.measured, s.anchored, fromFold = e.Usage.PromptTokens, true, false
				s.systemPromptTokens = estimateTokens(v.systemPrompt())
				anchorSeq = stepSeq
			}

		case transcript.CompactionSummary:
			if e.Usage != nil {
				s.total = s.total.Add(llmUsage(*e.Usage))
			}
			// A compaction replaces the messages an earlier anchor described, so
			// that anchor can no longer be reconstructed from the log. The
			// summary carries the size of what replaced them and becomes the new
			// anchor; without it there is no measurement to anchor on.
			s.anchored = false
			if e.TokensAfter > 0 {
				s.measured, s.anchored, fromFold = e.TokensAfter, true, true
				s.systemPromptTokens = estimateTokens(v.systemPrompt())
				anchorAt = len(v.entries)
			}
		}
	}

	if s.anchored {
		s.estimated = a.estimateRequestTokens(
			s.systemPromptTokens,
			messagesOf(anchorEntries(v, fromFold, anchorSeq, anchorAt)),
		)
	}
	return v, s, nil
}

// total reports the session cost without requiring the caller to handle a log
// that does not project. That failure has already failed the turn that wrote it,
// and a partial total beats showing nothing.
func (a *Agent) total() llm.TokenUsage {
	_, s, _ := a.derive()
	return s.total
}

// anchorEntries is the part of the final view the anchored request was built
// from. A StepStart produces no entry, so the request it precedes carried the
// entries before it; a compaction's run is everything applied up to it. Neither
// can be overtaken by a later compaction, because a compaction re-anchors on
// itself.
func anchorEntries(v *view, fromFold bool, stepSeq int64, anchorAt int) []entry {
	if fromFold {
		if anchorAt > len(v.entries) {
			anchorAt = len(v.entries)
		}
		return v.entries[:anchorAt]
	}
	i := sort.Search(len(v.entries), func(i int) bool { return v.entries[i].seq >= stepSeq })
	return v.entries[:i]
}

// estimateContextTokens estimates the input token count of the next request: the
// last count the provider measured, plus the growth seen since.
// Only the estimator's change is trusted; its absolute value is not, because it
// counts code and JSON at the density of prose.
func (a *Agent) estimateContextTokens(v *view, s stats) int {
	est := a.estimateRequestTokens(estimateTokens(v.systemPrompt()), v.messages())
	if !s.anchored {
		return est
	}
	est = s.measured + est - s.estimated
	if est < 0 {
		return 0
	}
	return est
}
