package tool

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"winky/internal/llm"
)

// nameRE matches the tool-name grammar shared by OpenAI and Anthropic: letters,
// digits, underscore and hyphen, up to 64 characters.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Registry is the set of tools an Agent exposes, keyed by name.
//
// Registration happens once at startup: Register validates the spec and rejects
// duplicates, so a misconfigured tool fails instead of silently
// shadowing another. Registering during a session is allowed but discouraged:
// the tool list is part of the request prefix, so changing it invalidates
// provider-side prompt caching for every later turn.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	limit int // 0 means DefaultMaxOutputBytes
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// SetMaxOutputBytes overrides the per-call output cap for every tool in the
// registry. It returns the registry so construction can be chained.
func (r *Registry) SetMaxOutputBytes(n int) *Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.limit = n
	return r
}

// Register adds a tool. It rejects a nil tool, a name outside the provider
// grammar, a duplicate name, missing parameters, and a missing description: all
// of these make the tool unusable to the model rather than merely imprecise.
func (r *Registry) Register(t Tool) error {
	if t == nil {
		return fmt.Errorf("tool: nil tool")
	}
	spec := t.Spec()
	if !nameRE.MatchString(spec.Name) {
		return fmt.Errorf(
			"tool: invalid name %q: must match %s", spec.Name, nameRE,
		)
	}
	if strings.TrimSpace(spec.Description) == "" {
		return fmt.Errorf("tool %q: description is required", spec.Name)
	}
	if spec.Parameters == nil {
		return fmt.Errorf(
			"tool %q: parameters must be a JSON schema object (use object(nil) for none)",
			spec.Name,
		)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tools == nil {
		r.tools = make(map[string]Tool)
	}
	if _, ok := r.tools[spec.Name]; ok {
		return fmt.Errorf("tool %q: already registered", spec.Name)
	}
	r.tools[spec.Name] = t
	return nil
}

// Lookup returns the tool registered under name.
func (r *Registry) Lookup(name string) (Tool, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Names returns the registered tool names, sorted.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sortedNamesLocked()
}

// Specs returns the tool descriptions to send in llm.Request.Tools, sorted by
// name.
//
// The order is stable on purpose. Tool specs sit in the request prefix that
// providers cache, and a map-order shuffle would change the serialized prefix
// between sessions for no reason. Callers must not mutate the returned specs.
func (r *Registry) Specs() []llm.ToolSpec {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]llm.ToolSpec, 0, len(r.tools))
	for _, name := range r.sortedNamesLocked() {
		out = append(out, r.tools[name].Spec())
	}
	return out
}

// Sequential reports whether the named tool must run alone, in issue order.
func (r *Registry) Sequential(name string) bool {
	t, ok := r.Lookup(name)
	if !ok {
		return false
	}
	s, ok := t.(Sequenced)
	return ok && s.Sequential()
}

// Run executes one call and never fails the agent loop.
//
// Every failure mode is folded into a Result the model can read and correct: an
// unknown tool name (a hallucinated tool), a panic inside a tool, or an error
// return. Protocol integrity requires a result for every call_id, so Run always
// returns one. The only case the caller must handle itself is context
// cancellation, which it checks around the call and reports as a failed turn
// instead of feeding "context canceled" to the model.
func (r *Registry) Run(ctx context.Context, call Call) Result {
	if r == nil {
		return Result{Output: "error: no tools are registered", IsError: true}
	}

	t, ok := r.Lookup(call.Name)
	if !ok {
		return Result{
			Output: fmt.Sprintf(
				"error: unknown tool %q (available: %s)",
				call.Name, strings.Join(r.Names(), ", "),
			),
			IsError: true,
		}
	}

	res, err := invokeSafely(ctx, t, call)
	if err != nil {
		res = Result{Output: "error: " + err.Error(), IsError: true}
	}
	res.Output = Truncate(res.Output, r.maxOutputBytes())
	return res
}

// invokeSafely runs a tool, converting a panic into an error so a single buggy
// tool cannot take down the session.
func invokeSafely(ctx context.Context, t Tool, call Call) (res Result, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("tool %q panicked: %v", call.Name, p)
		}
	}()
	return t.Invoke(ctx, call)
}

func (r *Registry) sortedNamesLocked() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) maxOutputBytes() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.limit <= 0 {
		return DefaultMaxOutputBytes
	}
	return r.limit
}
