package transcript

import (
	"strings"
	"testing"
	"time"
)

func TestPromptChangedRoundTrip(t *testing.T) {
	in := PromptChanged{
		Source:    "startup",
		Slot:      "base",
		Name:      "base",
		Text:      "line one\nline two\n",
		Timestamp: time.Date(2026, 11, 5, 1, 2, 3, 0, time.UTC),
	}

	record, err := Encode(WithSeq(in, 7))
	if err != nil {
		t.Fatalf("Encode() = %v", err)
	}
	if !strings.Contains(string(record), `"type":"prompt_changed"`) {
		t.Fatalf("record = %s, want the wire type prompt_changed", record)
	}

	ev, err := Decode(record)
	if err != nil {
		t.Fatalf("Decode() = %v", err)
	}
	if ev.Seq() != 7 {
		t.Fatalf("Seq() = %d, want 7", ev.Seq())
	}

	got, ok := ev.(PromptChanged)
	if !ok {
		t.Fatalf("Decode() = %T, want PromptChanged", ev)
	}
	if got.Source != in.Source || got.Slot != in.Slot || got.Name != in.Name ||
		got.Text != in.Text || got.Remove != in.Remove || !got.Timestamp.Equal(in.Timestamp) {
		t.Fatalf("Decode() = %+v, want %+v", got, in)
	}
}

// TestPromptChangedRemoveAndEmptyTextRoundTrip covers the two field values the
// omitempty tags shrink: they must come back as the same state.
func TestPromptChangedRemoveAndEmptyTextRoundTrip(t *testing.T) {
	cases := []PromptChanged{
		{Slot: "base", Name: "base", Text: ""},
		{Slot: "base", Name: "base", Remove: true},
	}
	for _, in := range cases {
		record, err := Encode(WithSeq(in, 1))
		if err != nil {
			t.Fatalf("Encode() = %v", err)
		}
		ev, err := Decode(record)
		if err != nil {
			t.Fatalf("Decode() = %v", err)
		}
		got, ok := ev.(PromptChanged)
		if !ok {
			t.Fatalf("Decode() = %T, want PromptChanged", ev)
		}
		if got.Text != in.Text || got.Remove != in.Remove {
			t.Fatalf("Decode() = %+v, want %+v", got, in)
		}
	}
}
