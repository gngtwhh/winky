// Package tool defines the tool surface an Agent exposes to the model: the Tool
// interface, the Registry that runs calls, and the permission policy.
//
// A Tool is provider-independent. It exposes an llm.ToolSpec and consumes a
// Call carrying the provider's raw JSON arguments. The built-in tools live in
// internal/tool/default_tool.
package tool

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"strings"
	"time"

	"winky/internal/llm"
)

// Tool is one capability the model may invoke.
type Tool interface {
	// Spec describes the tool to the model.
	Spec() llm.ToolSpec

	// Invoke runs one call. An error means the tool could not run at all, for
	// example because of bad arguments or a missing file. A call that ran but
	// failed semantically should return Result{IsError: true} instead, with
	// output the model can act on.
	Invoke(ctx context.Context, call Call) (Result, error)
}

// Call is one model-issued tool invocation. Arguments is the raw JSON exactly
// as the provider produced it; each tool decodes it itself.
type Call struct {
	ID        string
	Name      string
	Arguments string
}

// Result is a tool's answer for one Call. Output is sent to the model verbatim
// as the function_call_output content.
type Result struct {
	Output  string
	IsError bool
	// Aborted reports that the call stopped because its context was cancelled;
	// the agent replaces the output with an aborted marker.
	Aborted bool
}

// Func adapts a typed handler into a Tool. Arguments are decoded from JSON into
// T before the handler runs; empty or null arguments decode to T's zero value.
// A decode failure is returned as an error.
func Func[T any](spec llm.ToolSpec, fn func(context.Context, T) (Result, error)) Tool {
	return &funcTool[T]{spec: spec, fn: fn}
}

type funcTool[T any] struct {
	spec llm.ToolSpec
	fn   func(context.Context, T) (Result, error)
}

func (f *funcTool[T]) Spec() llm.ToolSpec { return f.spec }

func (f *funcTool[T]) Invoke(ctx context.Context, call Call) (Result, error) {
	args, err := DecodeArgs[T](call)
	if err != nil {
		return Result{}, err
	}
	return f.fn(ctx, args)
}

// Sequenced marks a tool that must run alone, in issue order.
type Sequenced interface{ Sequential() bool }

// Budgeted reports how long one call may run. The runner cancels the call when
// the budget is spent. A zero duration means no declared budget, so the
// runner's own ceiling applies.
type Budgeted interface {
	Deadline(c Call) time.Duration
}

// Guarded reports the permission a call needs. The runner asks before the call
// starts, so a refused call never runs. An empty Request means no permission is
// required.
type Guarded interface {
	PolicyRequest(c Call) (Request, error)
}

// capableTool decorates a Tool with any combination of the optional interfaces
// above. Capabilities fall through to the wrapped tool when not overridden, so
// wrappers compose in any order.
type capableTool struct {
	Tool
	sequential bool
	deadline   func(Call) time.Duration
	request    func(Call) (Request, error)
}

func (t capableTool) Sequential() bool {
	return t.sequential || sequencedOf(t.Tool)
}

func (t capableTool) Deadline(call Call) time.Duration {
	if t.deadline != nil {
		return t.deadline(call)
	}
	return deadlineOf(t.Tool, call)
}

func (t capableTool) PolicyRequest(call Call) (Request, error) {
	if t.request != nil {
		return t.request(call)
	}
	return policyRequestOf(t.Tool, call)
}

// WithSequential marks a tool's calls as ordered.
func WithSequential(t Tool) Tool {
	return capableTool{Tool: t, sequential: true}
}

// WithBudget attaches a declared call budget.
func WithBudget(t Tool, deadline func(Call) time.Duration) Tool {
	return capableTool{Tool: t, deadline: deadline}
}

// WithGuard attaches a permission request built from the call's decoded
// arguments.
func WithGuard[T any](t Tool, request func(T) (Request, error)) Tool {
	return capableTool{Tool: t, request: func(call Call) (Request, error) {
		args, err := DecodeArgs[T](call)
		if err != nil {
			return Request{}, err
		}
		return request(args)
	}}
}

func sequencedOf(t Tool) bool {
	s, ok := t.(Sequenced)
	return ok && s.Sequential()
}

func deadlineOf(t Tool, call Call) time.Duration {
	if b, ok := t.(Budgeted); ok {
		return b.Deadline(call)
	}
	return 0
}

func policyRequestOf(t Tool, call Call) (Request, error) {
	if g, ok := t.(Guarded); ok {
		return g.PolicyRequest(call)
	}
	return Request{}, nil
}

// DecodeArgs decodes a call's raw JSON arguments into T. Empty arguments (a tool
// that takes none) and an explicit null both decode to the zero value.
func DecodeArgs[T any](call Call) (T, error) {
	var args T
	raw := strings.TrimSpace(call.Arguments)
	if raw == "" || raw == "null" {
		return args, nil
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return args, fmt.Errorf("invalid arguments for tool %q: %w", call.Name, err)
	}
	return args, nil
}
