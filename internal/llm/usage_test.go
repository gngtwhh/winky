package llm

import "testing"

func TestTokenUsageAdd(t *testing.T) {
	a := TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, CachedTokens: 3}
	b := TokenUsage{PromptTokens: 20, CompletionTokens: 8, TotalTokens: 28, CachedTokens: 7}
	got := a.Add(b)
	want := TokenUsage{PromptTokens: 30, CompletionTokens: 13, TotalTokens: 43, CachedTokens: 10}
	if got != want {
		t.Fatalf("Add = %+v, want %+v", got, want)
	}
}

func TestTokenUsageCacheHitRate(t *testing.T) {
	cases := []struct {
		name string
		u    TokenUsage
		want float64
	}{
		{"no input", TokenUsage{PromptTokens: 0, CachedTokens: 5}, 0},
		{"no cache", TokenUsage{PromptTokens: 100, CachedTokens: 0}, 0},
		{"half", TokenUsage{PromptTokens: 100, CachedTokens: 50}, 0.5},
		{"clamp over", TokenUsage{PromptTokens: 100, CachedTokens: 150}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.u.CacheHitRate(); got != tc.want {
				t.Fatalf("CacheHitRate() = %v, want %v", got, tc.want)
			}
		})
	}
}
