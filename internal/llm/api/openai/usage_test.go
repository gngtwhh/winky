package openai

import (
	"encoding/json"
	"testing"

	"github.com/openai/openai-go/v3/responses"
)

// The cache breakdown is only ever visible through toTokenUsage, so decode real
// provider payloads here rather than hand-building the struct: this is the seam
// where a wrong presence check silently drops the whole usage record.
func TestToTokenUsage(t *testing.T) {
	cases := []struct {
		name string
		json string
		want *struct {
			prompt, completion, total, cached int
		}
	}{
		{
			name: "deepseek cache hit",
			json: `{"input_tokens":656,"input_tokens_details":{"cached_tokens":512},"output_tokens":13,"output_tokens_details":{"reasoning_tokens":10},"total_tokens":669}`,
			want: &struct{ prompt, completion, total, cached int }{656, 13, 669, 512},
		},
		{
			name: "cache miss",
			json: `{"input_tokens":34,"input_tokens_details":{"cached_tokens":0},"output_tokens":26,"total_tokens":60}`,
			want: &struct{ prompt, completion, total, cached int }{34, 26, 60, 0},
		},
		{
			// Gateways in front of the model may report the token counts but
			// omit the total; usage (and its cache breakdown) must survive.
			name: "total omitted",
			json: `{"input_tokens":100,"input_tokens_details":{"cached_tokens":64},"output_tokens":20}`,
			want: &struct{ prompt, completion, total, cached int }{100, 20, 120, 64},
		},
		{
			name: "output only",
			json: `{"input_tokens":0,"output_tokens":5,"total_tokens":5}`,
			want: &struct{ prompt, completion, total, cached int }{0, 5, 5, 0},
		},
		{
			// Degenerate but real: a gateway that only reports the total must
			// not be mistaken for "no usage".
			name: "total only",
			json: `{"total_tokens":50}`,
			want: &struct{ prompt, completion, total, cached int }{0, 0, 50, 0},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var u responses.ResponseUsage
			if err := json.Unmarshal([]byte(tc.json), &u); err != nil {
				t.Fatalf("unmarshal usage: %v", err)
			}
			got := toTokenUsage(u)
			if got == nil {
				t.Fatalf("toTokenUsage(%s) = nil, want usage", tc.json)
			}
			if got.PromptTokens != tc.want.prompt ||
				got.CompletionTokens != tc.want.completion ||
				got.TotalTokens != tc.want.total ||
				got.CachedTokens != tc.want.cached {
				t.Fatalf("toTokenUsage(%s) = %+v, want prompt=%d completion=%d total=%d cached=%d",
					tc.json, *got, tc.want.prompt, tc.want.completion, tc.want.total, tc.want.cached)
			}
		})
	}
}

// No usage reported at all stays distinguishable from real zero counts.
func TestToTokenUsageAbsent(t *testing.T) {
	var u responses.ResponseUsage
	if err := json.Unmarshal([]byte(`{}`), &u); err != nil {
		t.Fatalf("unmarshal usage: %v", err)
	}
	if got := toTokenUsage(u); got != nil {
		t.Fatalf("toTokenUsage(empty) = %+v, want nil", *got)
	}
}

// A usage that carries a cache count below the rounding threshold must still
// render as a hit, not as "0%" — checked through llm.TokenUsage's own rate.
func TestToTokenUsageCacheRate(t *testing.T) {
	var u responses.ResponseUsage
	if err := json.Unmarshal(
		[]byte(`{"input_tokens":200000,"input_tokens_details":{"cached_tokens":128},"output_tokens":10,"total_tokens":200010}`),
		&u,
	); err != nil {
		t.Fatalf("unmarshal usage: %v", err)
	}
	got := toTokenUsage(u)
	if got == nil {
		t.Fatal("toTokenUsage = nil, want usage")
	}
	if rate := got.CacheHitRate(); rate <= 0 {
		t.Fatalf("CacheHitRate() = %v, want > 0", rate)
	}
}
