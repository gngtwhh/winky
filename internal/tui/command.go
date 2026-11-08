package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"winky/internal/tool"
)

// commandHandler runs one "/" command. Like every other Update helper it takes
// the model by value, so the caller hands back the mutated copy.
type commandHandler func(m model) (tea.Model, tea.Cmd)

// slashCommand is one "/" command the input accepts.
type slashCommand struct {
	names []string // the canonical form first, then aliases
	desc  string
	run   commandHandler
}

// commands returns the table of "/" commands. Dispatch and the /? listing both
// read it, so a new command cannot be added to one and forgotten in the other.
//
// It is a function rather than a variable because the /? handler renders this
// table, which would make a variable depend on itself.
func commands() []slashCommand {
	return []slashCommand{
		{
			names: []string{"/readonly"},
			desc:  "read-only mode",
			run:   setPermission(tool.ReadOnly),
		},
		{
			names: []string{"/workspace", "/write"},
			desc:  "workspace read/write mode",
			run:   setPermission(tool.WorkspaceWrite),
		},
		{
			names: []string{"/full"},
			desc:  "full access, including outside paths, shell, and network",
			run:   setPermission(tool.FullAccess),
		},
		{
			names: []string{"/compact"},
			desc:  "summarize the oldest turns now",
			run:   func(m model) (tea.Model, tea.Cmd) { return m.startCompact() },
		},
		{
			names: []string{"/?", "/help"},
			desc:  "list the supported commands",
			run:   showCommands,
		},
	}
}

// applyCommand handles a "/" input. Only the command word is matched, so a
// trailing argument is ignored rather than making the whole line unknown.
func (m model) applyCommand(text string) (tea.Model, tea.Cmd) {
	m.input.Reset()
	name, _, _ := strings.Cut(strings.TrimSpace(text), " ")
	for _, c := range commands() {
		if c.matches(name) {
			return c.run(m)
		}
	}
	m.status = "unknown command " + name + " (try /?)"
	return m, nil
}

func (c slashCommand) matches(name string) bool {
	for _, n := range c.names {
		if n == name {
			return true
		}
	}
	return false
}

// setPermission switches the tool policy. The command stays listed even without
// a policy attached; it then reports why it did nothing.
func setPermission(perm tool.Permission) commandHandler {
	return func(m model) (tea.Model, tea.Cmd) {
		if m.policy == nil {
			m.status = "no tool policy attached"
			return m, nil
		}
		m.policy.Set(perm)
		m.status = "permission: " + perm.String()
		return m, nil
	}
}

// showCommands prints the table in the scrollback, where it can be scrolled back
// to instead of disappearing with the status line.
func showCommands(m model) (tea.Model, tea.Cmd) {
	m.appendCard(card{Kind: cardNotice, Text: commandsText(), At: time.Now()})
	return m, nil
}

// commandsText renders the command table as an aligned two-column list.
func commandsText() string {
	table := commands()
	labels := make([]string, len(table))
	width := 0
	for i, c := range table {
		labels[i] = strings.Join(c.names, ", ")
		width = max(width, len(labels[i]))
	}

	var b strings.Builder
	b.WriteString("Commands:")
	for i, c := range table {
		fmt.Fprintf(&b, "\n  %-*s  %s", width, labels[i], c.desc)
	}
	return b.String()
}

// startCompact runs a manual compaction in the background. The visible result
// arrives through ContextCompacted; this only tracks completion and failure.
func (m model) startCompact() (tea.Model, tea.Cmd) {
	if m.ag == nil {
		m.status = "agent is not configured"
		return m, nil
	}
	if m.streaming {
		m.status = "wait for the current turn to finish"
		return m, nil
	}
	if m.compactCanc != nil {
		m.status = "already compacting"
		return m, nil
	}

	ctx, cancel := context.WithCancel(m.tuiContext())
	m.compactCanc = cancel
	m.status = "compacting..."
	m.err = nil
	return m, doCompact(ctx, m.ag)
}

// handleCompactResult reports why a manual compaction did or did not run. The
// size change itself is already shown by the ContextCompacted event.
func (m model) handleCompactResult(res compactResult) (tea.Model, tea.Cmd) {
	m.compactCanc = nil
	switch {
	case res.Err == nil && res.Changed:
		m.status = ""
	case res.Err == nil:
		m.status = "nothing to compact"
	case errors.Is(res.Err, context.Canceled):
		m.status = "compaction cancelled"
	default:
		m.err = res.Err
		m.status = ""
		m.appendCard(card{Kind: cardError, Text: res.Err.Error(), At: time.Now()})
		return m, nil // appendCard rebuilt the view
	}
	m.rebuild()
	return m, nil
}
