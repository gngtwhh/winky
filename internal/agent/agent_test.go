package agent

import (
	"context"
	"testing"

	"winky/internal/llm"
)

// fakeStreamer is a deterministic in-memory llm.Streamer for tests.
type fakeStreamer struct {
	replies []string
	calls   []llm.Request
}

func (f *fakeStreamer) Stream(_ context.Context, req llm.Request) (<-chan llm.Chunk, error) {
	f.calls = append(f.calls, req)
	reply := f.replies[0]
	f.replies = f.replies[1:]

	ch := make(chan llm.Chunk, len(reply)+1)
	go func() {
		defer close(ch)
		for _, r := range reply {
			ch <- llm.TextDelta{Text: string(r)}
		}
		ch <- llm.FinishChunk{Reason: "stop"}
	}()
	return ch, nil
}

func (f *fakeStreamer) Name() string { return "fake" }

func TestAgentMultiTurnCarriesHistory(t *testing.T) {
	f := &fakeStreamer{replies: []string{"hello!", "i remember: hi"}}
	a := New(f, WithModel("test-model"))

	ctx := context.Background()

	first, err := a.Turn(ctx, "hi")
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if first != "hello!" {
		t.Fatalf("first reply = %q, want %q", first, "hello!")
	}

	second, err := a.Turn(ctx, "what did i say?")
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if second != "i remember: hi" {
		t.Fatalf("second reply = %q, want %q", second, "i remember: hi")
	}

	if len(f.calls) != 2 {
		t.Fatalf("stream calls = %d, want 2", len(f.calls))
	}

	// The second request must carry the full conversation so far.
	secondReq := f.calls[1]
	if secondReq.Model != "test-model" {
		t.Errorf("second request model = %q, want %q", secondReq.Model, "test-model")
	}

	wantRoles := []llm.Role{llm.RoleUser, llm.RoleAssistant, llm.RoleUser}
	if len(secondReq.Messages) != len(wantRoles) {
		t.Fatalf("second request messages = %d, want %d", len(secondReq.Messages), len(wantRoles))
	}
	for i, want := range wantRoles {
		if secondReq.Messages[i].Role != want {
			t.Errorf("message %d role = %q, want %q", i, secondReq.Messages[i].Role, want)
		}
	}
	if secondReq.Messages[0].Content != "hi" {
		t.Errorf("first message content = %q, want %q", secondReq.Messages[0].Content, "hi")
	}
	if secondReq.Messages[1].Content != "hello!" {
		t.Errorf("assistant message content = %q, want %q", secondReq.Messages[1].Content, "hello!")
	}

	if got := len(a.History()); got != 4 {
		t.Errorf("history length = %d, want 4", got)
	}
}
