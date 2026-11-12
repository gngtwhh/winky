package prompt

import (
	_ "embed"
	"errors"
	"fmt"
	"sort"
	"strings"
)

//go:embed default_system_prompt.md
var embeddedDefault string

// DefaultSystemPrompt is the prompt a new session starts with when the -prompt
// flag is not given.
var DefaultSystemPrompt = strings.TrimSpace(embeddedDefault)

// Slot orders sections in the rendered prompt. It reaches the transcript as a
// string, so a value must never be renamed; add new ones instead.
type Slot string

const SlotBase Slot = "base"

const BaseName = "base"

const (
	separator       = "\n\n"
	unknownSlotRank = 1 << 30
)

var (
	ErrUnknownSlot = errors.New("prompt: unknown slot")
	ErrEmptyName   = errors.New("prompt: empty section name")
)

type Section struct {
	Slot Slot
	Name string
	Text string
}

type State struct {
	Sections []Section
}

func (s *State) Set(slot Slot, name, text string) error {
	if !KnownSlot(slot) {
		return fmt.Errorf("%w: %q", ErrUnknownSlot, slot)
	}
	if name == "" {
		return ErrEmptyName
	}
	for i := range s.Sections {
		if s.Sections[i].Slot == slot && s.Sections[i].Name == name {
			s.Sections[i].Text = text
			return nil
		}
	}
	s.Sections = append(s.Sections, Section{Slot: slot, Name: name, Text: text})
	return nil
}

func (s *State) Remove(slot Slot, name string) error {
	if !KnownSlot(slot) {
		return fmt.Errorf("%w: %q", ErrUnknownSlot, slot)
	}
	if name == "" {
		return ErrEmptyName
	}
	for i := range s.Sections {
		if s.Sections[i].Slot == slot && s.Sections[i].Name == name {
			s.Sections = append(s.Sections[:i], s.Sections[i+1:]...)
			return nil
		}
	}
	return nil
}

func (s *State) Has(slot Slot) bool {
	for i := range s.Sections {
		if s.Sections[i].Slot == slot {
			return true
		}
	}
	return false
}

func KnownSlot(slot Slot) bool {
	_, ok := slotRank(slot)
	return ok
}

func slotRank(slot Slot) (rank int, known bool) {
	switch slot {
	case SlotBase:
		return 0, true
	}
	return 0, false
}

func (s State) Render() string {
	sections := make([]Section, 0, len(s.Sections))
	for _, sec := range s.Sections {
		if sec.Text != "" {
			sections = append(sections, sec)
		}
	}
	if len(sections) == 0 {
		return ""
	}
	sort.Slice(sections, func(i, j int) bool { return lessSection(sections[i], sections[j]) })

	texts := make([]string, len(sections))
	for i, sec := range sections {
		texts[i] = sec.Text
	}
	return strings.Join(texts, separator)
}

func lessSection(a, b Section) bool {
	if ra, rb := rankOf(a.Slot), rankOf(b.Slot); ra != rb {
		return ra < rb
	}
	if a.Slot != b.Slot {
		return a.Slot < b.Slot
	}
	return a.Name < b.Name
}

func rankOf(slot Slot) int {
	if rank, ok := slotRank(slot); ok {
		return rank
	}
	return unknownSlotRank
}
