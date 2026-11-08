// Package tool defines the tool surface an Agent exposes to the model: the
// descriptions sent in llm.Request.Tools and the local implementations that run
// when the model calls one.
//
// A Tool is provider-independent. It produces an llm.ToolSpec for the request
// and consumes a Call carrying the raw JSON arguments the provider returned.
// Everything above this layer (the agent loop, the TUI) talks to a Registry and
// never to a concrete tool.
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

	// Invoke runs one call. The returned error reports that the tool could not
	// run at all (bad arguments, missing file, escaping path); the caller turns
	// it into an error result for the model. A tool that ran but failed
	// semantically should instead return Result{IsError: true} carrying output
	// the model can act on.
	Invoke(ctx context.Context, call Call) (Result, error)
}

// Call is one model-issued tool invocation.
//
// ID is the call_id the matching tool output must be rounded-trip back with.
// Arguments is the raw JSON string exactly as the provider produced it: decoding
// belongs to the tool, so the protocol layer never needs tool-specific types.
type Call struct {
	ID        string
	Name      string
	Arguments string
}

// Result is a tool's answer for one Call. Output is sent to the model verbatim
// as the function_call_output content, so it should be concise and
// self-contained: whatever the model needs to decide the next step.
type Result struct {
	Output  string
	IsError bool
	// Aborted reports that the tool stopped because the context was cancelled;
	// the agent replaces the output with an aborted marker.
	Aborted bool
}

// Func adapts a typed handler into a Tool. Arguments are decoded from JSON into
// T before the handler runs; empty or null arguments decode to T's zero value.
//
// Decoding lives here so every tool gets the same treatment of malformed input:
// a decode failure is an error, which the agent reports to the model as an error
// result instead of failing the turn.
func Func[T any](spec llm.ToolSpec, fn func(context.Context, T) (Result, error)) Tool {
	return &funcTool[T]{spec: spec, fn: fn}
}

type funcTool[T any] struct {
	spec llm.ToolSpec
	fn   func(context.Context, T) (Result, error)
}

func (f *funcTool[T]) Spec() llm.ToolSpec { return f.spec }

func (f *funcTool[T]) Invoke(ctx context.Context, call Call) (Result, error) {
	args, err := decodeArgs[T](call)
	if err != nil {
		return Result{}, err
	}
	return f.fn(ctx, args)
}

// Sequenced marks a tool whose calls must run one at a time, in issue order,
// rather than concurrently with sibling calls in the same step.
type Sequenced interface{ Sequential() bool }

// Budgeted reports how long one call may run. The runner enforces it: it cancels
// the call's context when the budget is spent, which the tool is expected to
// honour by returning the output it already produced.
//
// A zero duration means "no declared budget": the runner falls back to its own
// ceiling. Tools that have no meaningful slow path can leave it at zero; tools
// whose call may legitimately take minutes (shell, web) should declare it,
// usually from their own arguments.
type Budgeted interface {
	Deadline(c Call) time.Duration
}

// Guarded reports the permission a call needs. The runner asks before the call
// starts, so the time a user spends deciding is not billed to the call's budget
// or its elapsed time, and a refused call never runs at all.
//
// The returned Request's Tool field names the operation. An empty Request means
// the call needs no permission.
type Guarded interface {
	PolicyRequest(c Call) (Request, error)
}

// budgeted attaches a declared budget to a tool.
func budgeted(t Tool, deadline func(Call) time.Duration) Tool {
	return budgetedTool{Tool: t, deadline: deadline}
}

type budgetedTool struct {
	Tool
	deadline func(Call) time.Duration
}

func (b budgetedTool) Deadline(c Call) time.Duration { return b.deadline(c) }

// PolicyRequest forwards the inner capability: a wrapper only implements the one
// it adds, so it must not drop the others on the floor.
func (b budgetedTool) PolicyRequest(c Call) (Request, error) { return policyRequestOf(b.Tool, c) }

// guarded attaches a permission request builder to a tool.
func guarded[T any](t Tool, request func(T) (Request, error)) Tool {
	return guardedTool[T]{Tool: t, request: request}
}

type guardedTool[T any] struct {
	Tool
	request func(T) (Request, error)
}

func (g guardedTool[T]) PolicyRequest(c Call) (Request, error) {
	args, err := decodeArgs[T](c)
	if err != nil {
		return Request{}, err
	}
	return g.request(args)
}

// sequenced wraps a tool to mark it sequential.
func sequenced(t Tool) Tool { return sequencedTool{t} }

type sequencedTool struct{ Tool }

func (sequencedTool) Sequential() bool { return true }

// PolicyRequest forwards the inner capability, as budgetedTool does.
func (s sequencedTool) PolicyRequest(c Call) (Request, error) { return policyRequestOf(s.Tool, c) }

// policyRequestOf asks a tool for the permission its call needs.
func policyRequestOf(t Tool, c Call) (Request, error) {
	if g, ok := t.(Guarded); ok {
		return g.PolicyRequest(c)
	}
	return Request{}, nil
}

// decodeArgs decodes a call's raw JSON arguments into T. Empty arguments (a tool
// that takes none) and an explicit null both decode to the zero value.
func decodeArgs[T any](call Call) (T, error) {
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
