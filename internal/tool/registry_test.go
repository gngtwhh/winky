package tool

import (
	"context"
	"errors"
	"strings"
	"testing"

	"winky/internal/llm"
)

// echoTool is the smallest useful fake: it returns its argument verbatim.
func echoTool(name string) Tool {
	return Func[struct {
		Text string `json:"text"`
	}](llm.ToolSpec{
		Name:        name,
		Description: "echo text back",
		Parameters: object(map[string]any{
			"text": stringProp("text"),
		}),
	}, func(_ context.Context, args struct {
		Text string `json:"text"`
	}) (Result, error) {
		return Result{Output: args.Text}, nil
	})
}

// specTool wraps a bare spec with a no-op invocation, so registration checks can
// exercise the spec alone.
func specTool(spec llm.ToolSpec) Tool {
	return Func[struct{}](spec, func(context.Context, struct{}) (Result, error) {
		return Result{Output: "ok"}, nil
	})
}

func TestRegisterRejectsInvalidTools(t *testing.T) {
	valid := func() Tool { return echoTool("echo") }

	cases := []struct {
		name string
		tool Tool
		want string
	}{
		{"nil", nil, "nil tool"},
		{"bad name", echoTool("read file"), "invalid name"},
		{"too long", echoTool(strings.Repeat("a", 65)), "invalid name"},
		{"empty description", specTool(llm.ToolSpec{
			Name:       "nodesc",
			Parameters: object(nil),
		}), "description is required"},
		{"nil parameters", specTool(llm.ToolSpec{
			Name:        "noparams",
			Description: "missing schema",
		}), "parameters must be a JSON schema object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry()
			err := r.Register(tc.tool)
			if err == nil {
				t.Fatalf("Register(%v) = nil error, want error containing %q", tc.tool, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Register error = %q, want it to contain %q", err, tc.want)
			}
			if len(r.Names()) != 0 {
				t.Fatalf("rejected tool was registered: %v", r.Names())
			}
		})
	}

	r := NewRegistry()
	if err := r.Register(valid()); err != nil {
		t.Fatalf("Register(valid): %v", err)
	}
	if err := r.Register(valid()); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate Register = %v, want an already-registered error", err)
	}
}

// Specs must be sorted and stable: the order is part of the cached request
// prefix, so a map-order shuffle would cost cache hits for free.
func TestSpecsSorted(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{"zeta", "alpha", "middle"} {
		if err := r.Register(echoTool(name)); err != nil {
			t.Fatalf("Register(%s): %v", name, err)
		}
	}

	want := []string{"alpha", "middle", "zeta"}
	if got := r.Names(); !equalStrings(got, want) {
		t.Fatalf("Names = %v, want %v", got, want)
	}
	specs := r.Specs()
	for i, want := range want {
		if specs[i].Name != want {
			t.Fatalf("Specs[%d].Name = %q, want %q", i, specs[i].Name, want)
		}
	}
}

func TestRunUnknownTool(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(echoTool("echo")); err != nil {
		t.Fatalf("Register: %v", err)
	}

	res := r.Run(context.Background(), Call{Name: "nope", Arguments: `{}`})
	if !res.IsError {
		t.Fatal("IsError = false, want true for an unknown tool")
	}
	if !strings.Contains(res.Output, `unknown tool "nope"`) {
		t.Fatalf("Output = %q, want it to name the unknown tool", res.Output)
	}
	if !strings.Contains(res.Output, "echo") {
		t.Fatalf("Output = %q, want it to list the available tools", res.Output)
	}
}

func TestRunRecoversPanic(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Func[struct{}](llm.ToolSpec{
		Name:        "boom",
		Description: "panics",
		Parameters:  object(nil),
	}, func(context.Context, struct{}) (Result, error) {
		panic("kaboom")
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}

	res := r.Run(context.Background(), Call{Name: "boom"})
	if !res.IsError || !strings.Contains(res.Output, "kaboom") {
		t.Fatalf("Run = %+v, want a recovered panic result", res)
	}
}

func TestRunErrorBecomesErrorResult(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Func[struct{}](llm.ToolSpec{
		Name:        "fail",
		Description: "fails to run",
		Parameters:  object(nil),
	}, func(context.Context, struct{}) (Result, error) {
		return Result{}, errors.New("no such file")
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}

	res := r.Run(context.Background(), Call{Name: "fail"})
	if !res.IsError {
		t.Fatal("IsError = false, want true for an error return")
	}
	if res.Output != "error: no such file" {
		t.Fatalf("Output = %q, want %q", res.Output, "error: no such file")
	}
}

// A tool that ran and reports IsError must keep its message intact: the prefix
// is only added to errors raised before the tool could run.
func TestRunPreservesToolErrorResult(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Func[struct{}](llm.ToolSpec{
		Name:        "nonzero",
		Description: "ran but failed",
		Parameters:  object(nil),
	}, func(context.Context, struct{}) (Result, error) {
		return Result{Output: "exit status 1: tests failed", IsError: true}, nil
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}

	res := r.Run(context.Background(), Call{Name: "nonzero"})
	if !res.IsError || res.Output != "exit status 1: tests failed" {
		t.Fatalf("Run = %+v, want the tool's own error output", res)
	}
}

func TestRunTruncatesOutput(t *testing.T) {
	r := NewRegistry().SetMaxOutputBytes(40)
	if err := r.Register(Func[struct{}](llm.ToolSpec{
		Name:        "bulk",
		Description: "returns a lot",
		Parameters:  object(nil),
	}, func(context.Context, struct{}) (Result, error) {
		return Result{Output: strings.Repeat("x", 100)}, nil
	})); err != nil {
		t.Fatalf("Register: %v", err)
	}

	res := r.Run(context.Background(), Call{Name: "bulk"})
	if len(res.Output) > 40 {
		t.Fatalf("len(Output) = %d, want <= 40", len(res.Output))
	}
	if !strings.Contains(res.Output, "bytes elided") {
		t.Fatalf("Output = %q, want an elision marker", res.Output)
	}
}

func TestFuncDecodesArguments(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(echoTool("echo")); err != nil {
		t.Fatalf("Register: %v", err)
	}

	res := r.Run(context.Background(), Call{Name: "echo", Arguments: `{"text":"hi"}`})
	if res.IsError || res.Output != "hi" {
		t.Fatalf("Run = %+v, want hi", res)
	}
}

// A no-argument tool receives empty arguments from some providers and an
// explicit null from others; both must decode to the zero value.
func TestFuncAcceptsEmptyArguments(t *testing.T) {
	calls := 0
	tl := Func[struct{}](llm.ToolSpec{
		Name:        "ping",
		Description: "no arguments",
		Parameters:  object(nil),
	}, func(context.Context, struct{}) (Result, error) {
		calls++
		return Result{Output: "pong"}, nil
	})

	for _, args := range []string{"", "null", "{}"} {
		if _, err := tl.Invoke(context.Background(), Call{Name: "ping", Arguments: args}); err != nil {
			t.Fatalf("Invoke(%q): %v", args, err)
		}
	}
	if calls != 3 {
		t.Fatalf("handler ran %d times, want 3", calls)
	}
}

func TestFuncRejectsMalformedArguments(t *testing.T) {
	tl := echoTool("echo")
	for _, args := range []string{`{"text":`, `{"text":42}`} {
		if _, err := tl.Invoke(context.Background(), Call{Name: "echo", Arguments: args}); err == nil {
			t.Fatalf("Invoke(%q) = nil error, want a decode error", args)
		}
	}
}

// A nil registry is a valid value: the agent may run without tools.
func TestNilRegistryRun(t *testing.T) {
	var r *Registry
	res := r.Run(context.Background(), Call{Name: "echo"})
	if !res.IsError || res.Output == "" {
		t.Fatalf("nil registry Run = %+v, want an error result", res)
	}
	if got := r.Specs(); got != nil {
		t.Fatalf("nil registry Specs = %v, want nil", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
