package tool

import (
	"context"
	"fmt"
	"sync"
)

// Access classifies what a tool wants to do, for permission decisions.
type Access int

const (
	AccessRead    Access = iota // read inside or outside the workspace
	AccessWrite                 // create, modify, delete a file or directory
	AccessShell                 // run a shell command
	AccessNetwork               // fetch a URL or search the web
)

func (a Access) String() string {
	switch a {
	case AccessRead:
		return "read"
	case AccessWrite:
		return "write"
	case AccessShell:
		return "shell"
	case AccessNetwork:
		return "network"
	}
	return "?"
}

// Permission is the session's tool authority level.
type Permission int

const (
	ReadOnly       Permission = iota // read files inside the workspace
	WorkspaceWrite                   // read and modify files inside the workspace
	FullAccess                       // anything: outside paths, shell, network
)

func (p Permission) String() string {
	switch p {
	case ReadOnly:
		return "read-only"
	case WorkspaceWrite:
		return "workspace"
	case FullAccess:
		return "full"
	}
	return "?"
}

// Request is one permission decision a tool needs before it acts.
type Request struct {
	Tool   string
	Access Access
	Target string // path, URL, or command, for display
	Inside bool   // the target is inside the workspace
}

// Prompter asks the user whether to allow a request; nil means "deny".
type Prompter func(ctx context.Context, req Request) (bool, error)

// Policy holds the session's tool authority, shared by the tools and the TUI.
// Tools consult it before sensitive operations; the TUI flips the level with a
// command and answers prompts through the prompter.
type Policy struct {
	mu       sync.RWMutex
	perm     Permission
	prompter Prompter
	promptMu sync.Mutex // serializes concurrent prompts to the TUI
}

// NewPolicy returns a policy at the given level.
func NewPolicy(p Permission) *Policy { return &Policy{perm: p} }

func (p *Policy) Set(perm Permission) { p.mu.Lock(); p.perm = perm; p.mu.Unlock() }
func (p *Policy) Get() Permission     { p.mu.RLock(); defer p.mu.RUnlock(); return p.perm }

// SetPrompter installs the ask-the-user hook; call once before running tools.
func (p *Policy) SetPrompter(fn Prompter) { p.mu.Lock(); p.prompter = fn; p.mu.Unlock() }

func allows(perm Permission, a Access, inside bool) bool {
	switch a {
	case AccessRead:
		return inside || perm >= FullAccess
	case AccessWrite:
		return (inside && perm >= WorkspaceWrite) || perm >= FullAccess
	case AccessShell, AccessNetwork:
		return perm >= FullAccess
	}
	return false
}

// guard returns nil when the request is allowed or the user approves it, else a
// denial error. It is safe for concurrent use.
func (p *Policy) guard(ctx context.Context, req Request) error {
	p.mu.RLock()
	perm, prompter := p.perm, p.prompter
	p.mu.RUnlock()

	if allows(perm, req.Access, req.Inside) {
		return nil
	}
	if prompter == nil {
		return &DeniedError{Req: req}
	}

	// One prompt at a time: concurrent tool calls that both need approval must
	// not overwrite each other in the UI.
	p.promptMu.Lock()
	defer p.promptMu.Unlock()
	ok, err := prompter(ctx, req)
	if err != nil {
		return err
	}
	if !ok {
		return &DeniedError{Req: req}
	}
	return nil
}

// DeniedError reports an operation the user did not authorize.
type DeniedError struct{ Req Request }

func (e *DeniedError) Error() string {
	return fmt.Sprintf(
		"permission denied: %s needs %s access to %s (switch with /full or /workspace)",
		e.Req.Tool, e.Req.Access, e.Req.Target,
	)
}
