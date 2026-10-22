package openai

import (
	"testing"

	"winky/internal/llm"

	"github.com/openai/openai-go/v3/responses"
)

// A summary travels as a system message; an unknown role must not be dropped.
func TestConvertMessagesRoles(t *testing.T) {
	input, err := convertMessagesToInput([]llm.Message{
		{Role: llm.RoleSystem, Content: "summary"},
	})
	if err != nil {
		t.Fatalf("convertMessagesToInput: %v", err)
	}
	if len(input.OfInputItemList) != 1 {
		t.Fatalf("items = %d, want 1", len(input.OfInputItemList))
	}
	item := input.OfInputItemList[0]
	if item.OfMessage == nil || item.OfMessage.Role != responses.EasyInputMessageRoleSystem {
		t.Fatalf("item = %+v, want a system message", item)
	}

	if _, err := convertMessagesToInput([]llm.Message{{Role: "unknown"}}); err == nil {
		t.Fatal("expected an error for an unsupported role")
	}
}
