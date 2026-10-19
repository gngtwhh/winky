package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"winky/internal/config"
	"winky/internal/llm"
)

// recordingStreamer captures the request the agent actually sends.
type recordingStreamer struct {
	req  llm.Request
	sent bool
}

func (r *recordingStreamer) Stream(_ context.Context, req llm.Request) (<-chan llm.Chunk, error) {
	r.req = req
	r.sent = true
	ch := make(chan llm.Chunk, 2)
	ch <- llm.ResponseStart{}
	ch <- llm.ResponseEnd{StopReason: llm.StopReasonDone, Response: &llm.Response{}}
	close(ch)
	return ch, nil
}

func (*recordingStreamer) Name() string { return "recording" }

func testConfig() *config.Config {
	return &config.Config{
		Provider: config.ProviderConfig{
			Name:            "test",
			Model:           "test-model",
			MaxOutputTokens: 128,
		},
		Agent: config.AgentConfig{Temperature: 0.5},
	}
}

// writePrompt creates a prompt file and returns its path.
func writePrompt(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	return path
}

func TestNewAgentSendsSystemPrompt(t *testing.T) {
	path := writePrompt(t, "\n  You are winky. Keep answers short.  \n")
	rs := &recordingStreamer{}

	a, err := newAgent(rs, testConfig(), path, nil)
	if err != nil {
		t.Fatalf("newAgent: %v", err)
	}
	if _, err := a.Turn(context.Background(), "hi"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if !rs.sent {
		t.Fatal("streamer never called")
	}
	// Surrounding whitespace is trimmed so a blank file cannot send blank
	// instructions; the body must survive verbatim.
	if want := "You are winky. Keep answers short."; rs.req.SystemPrompt != want {
		t.Fatalf("SystemPrompt = %q, want %q", rs.req.SystemPrompt, want)
	}
	if rs.req.Model != "test-model" {
		t.Fatalf("Model = %q, want %q", rs.req.Model, "test-model")
	}
	if rs.req.MaxOutputTokens != 128 {
		t.Fatalf("MaxOutputTokens = %d, want 128", rs.req.MaxOutputTokens)
	}
}

// The flag defaults to empty, which must mean "no system prompt" rather than an
// empty instructions block.
func TestNewAgentWithoutPromptPath(t *testing.T) {
	rs := &recordingStreamer{}
	a, err := newAgent(rs, testConfig(), "", nil)
	if err != nil {
		t.Fatalf("newAgent: %v", err)
	}
	if _, err := a.Turn(context.Background(), "hi"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if rs.req.SystemPrompt != "" {
		t.Fatalf("SystemPrompt = %q, want empty", rs.req.SystemPrompt)
	}
}

func TestNewAgentBlankPromptFile(t *testing.T) {
	path := writePrompt(t, " \n\t\n")
	rs := &recordingStreamer{}
	a, err := newAgent(rs, testConfig(), path, nil)
	if err != nil {
		t.Fatalf("newAgent: %v", err)
	}
	if _, err := a.Turn(context.Background(), "hi"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if rs.req.SystemPrompt != "" {
		t.Fatalf("SystemPrompt = %q, want empty for blank file", rs.req.SystemPrompt)
	}
}

func TestNewAgentMissingPromptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.md")
	if _, err := newAgent(&recordingStreamer{}, testConfig(), path, nil); err == nil {
		t.Fatal("newAgent with missing prompt file = nil error, want error")
	}
}
