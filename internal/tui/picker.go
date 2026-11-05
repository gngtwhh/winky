package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"winky/internal/session"
)

// ErrSelectionAborted reports that the user quit the chooser with ctrl+c, so no
// session was picked and none should be started either.
var ErrSelectionAborted = errors.New("tui: session selection aborted")

// ChooseSession lets the user pick one of the sessions started in cwd and opens
// it. It returns nil, nil when the user cancels or cwd has no session, so the
// caller can start a new one, and ErrSelectionAborted when the user asked to
// quit instead. The session package owns the stored data; this only renders the
// choice.
func ChooseSession(ctx context.Context, agentDir, cwd string) (*session.Session, error) {
	sessions, err := session.List(agentDir, cwd)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, nil
	}

	sel, err := pick(ctx, agentDir, sessions, cwd, os.Stdin, os.Stdout)
	if err != nil {
		return nil, err
	}
	if sel.aborted {
		return nil, ErrSelectionAborted
	}
	if sel.index < 0 {
		return nil, nil
	}
	return session.Open(agentDir, sessions[sel.index].ID)
}

// selection is the outcome of the chooser.
type selection struct {
	index   int  // -1 when nothing was picked
	aborted bool // the user quit with ctrl+c
}

// pick runs the chooser and reports what the user did. Deletions are applied to
// disk through the session package, so the model below never removes anything
// itself.
func pick(ctx context.Context, agentDir string, sessions []session.Summary, cwd string, in io.Reader, out io.Writer) (selection, error) {
	program := tea.NewProgram(
		newPicker(agentDir, sessions, cwd),
		tea.WithContext(ctx),
		tea.WithInput(in),
		tea.WithOutput(out),
	)
	final, err := program.Run()
	if err != nil {
		// A signal (or a cancelled context) means quit, not "start a new one".
		if errors.Is(err, tea.ErrInterrupted) || errors.Is(err, tea.ErrProgramKilled) {
			return selection{index: -1, aborted: true}, nil
		}
		return selection{index: -1}, err
	}
	p, ok := final.(picker)
	if !ok {
		return selection{index: -1}, nil
	}
	return selection{index: p.chosen, aborted: p.aborted}, nil
}

// picker is the session chooser model.
type picker struct {
	agentDir   string
	sessions   []session.Summary
	cwd        string
	home       string
	cursor     int
	top        int
	width      int
	height     int
	chosen     int
	aborted    bool
	confirming bool
	status     string
	style      Styles
}

func newPicker(agentDir string, sessions []session.Summary, cwd string) picker {
	p := picker{agentDir: agentDir, sessions: sessions, cwd: cwd, chosen: -1, style: newStyles()}
	if home, err := os.UserHomeDir(); err == nil {
		p.home = home
	}
	return p
}

func (p picker) Init() tea.Cmd { return nil }

func (p picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width, p.height = msg.Width, msg.Height
		p.scroll()
		return p, nil

	case tea.KeyPressMsg:
		if p.confirming {
			switch msg.String() {
			case "enter":
				return p.deleteCurrent()
			case "esc":
				p.confirming = false
			case "ctrl+c":
				p.aborted = true
				return p, tea.Quit
			}
			return p, nil
		}

		// A session held by another process cannot be resumed or deleted; say so
		// instead of failing later.
		p.status = ""
		if p.sessions[p.cursor].InUse {
			switch msg.String() {
			case "enter", "ctrl+d":
				p.status = "session is in use by another process"
				return p, nil
			}
		}

		switch msg.String() {
		case "up", "k":
			if p.cursor > 0 {
				p.cursor--
				p.scroll()
			}
		case "down", "j":
			if p.cursor < len(p.sessions)-1 {
				p.cursor++
				p.scroll()
			}
		case "enter":
			p.chosen = p.cursor
			return p, tea.Quit
		case "ctrl+d":
			if len(p.sessions) > 0 {
				p.confirming = true
			}
		case "ctrl+c":
			p.aborted = true
			return p, tea.Quit
		case "esc", "q":
			return p, tea.Quit
		}
	}
	return p, nil
}

// deleteCurrent removes the session under the cursor after the user confirmed
// with enter.
func (p picker) deleteCurrent() (tea.Model, tea.Cmd) {
	p.confirming = false
	s := p.sessions[p.cursor]
	if err := session.Delete(p.agentDir, s.ID); err != nil {
		p.status = "delete failed: " + err.Error()
		return p, nil
	}

	p.sessions = append(p.sessions[:p.cursor], p.sessions[p.cursor+1:]...)
	p.status = "deleted " + s.ID
	if p.cursor >= len(p.sessions) {
		p.cursor = max(0, len(p.sessions)-1)
	}
	p.scroll()
	if len(p.sessions) == 0 {
		return p, tea.Quit
	}
	return p, nil
}

func (p picker) View() tea.View {
	width := max(minViewportWidth, p.width)
	lines := []string{
		p.style.Header.Render(fitToWidth([]string{
			"winky · resume · " + abbrevPath(p.cwd, p.home),
			"winky · resume · " + tailPath(p.cwd),
			"winky · resume",
		}, width)),
		"",
	}
	for i := p.top; i < len(p.sessions) && i < p.top+p.rows(); i++ {
		lines = append(lines, p.row(i, width))
	}
	lines = append(lines, "", p.footer(width))
	return tea.NewView(strings.Join(lines, "\n"))
}

// footer shows the confirmation prompt, then a transient message, and the key
// hints otherwise, so the chooser keeps a stable height.
func (p picker) footer(width int) string {
	switch {
	case p.confirming:
		return p.style.Err.Render(truncate(fmt.Sprintf(
			"Delete %q and its transcript? [enter] Confirm · [esc] Cancel", sessionLabel(p.sessions[p.cursor]),
		), width, "…"))
	case p.status != "":
		return p.style.Status.Render(truncate(p.status, width, "…"))
	default:
		return p.style.Status.Render(truncate(
			"[↑↓] Select · [enter] Resume · [ctrl+d] Delete · [esc] New session · [ctrl+c] Quit",
			width, "…",
		))
	}
}

// sessionLabel names a session in prompts: its title, or its id when empty.
func sessionLabel(s session.Summary) string {
	if s.Title == "" {
		return s.ID
	}
	return s.Title
}

// row renders one session: cursor marker, last use, model, and title.
func (p picker) row(i, width int) string {
	s := p.sessions[i]
	title := s.Title
	if title == "" {
		title = "(no messages)"
	}
	model := s.Model
	if model == "" {
		model = "-"
	}
	marker := "  "
	if i == p.cursor {
		marker = "› "
	}
	line := fmt.Sprintf("%s%-12s %-20s %s", marker, formatAge(s.UpdatedAt), model, title)
	if s.InUse {
		line += " · in use"
	}
	line = truncate(line, width, "…")
	switch {
	case i == p.cursor:
		return p.style.User.Render(line)
	case s.InUse:
		return p.style.Hint.Render(line)
	default:
		return p.style.Assistant.Render(line)
	}
}

// rows is the number of session lines that fit under the header and above the
// hint.
func (p picker) rows() int {
	if p.height <= 0 {
		return len(p.sessions)
	}
	return max(1, p.height-3)
}

// scroll keeps the cursor inside the visible window.
func (p *picker) scroll() {
	visible := p.rows()
	if p.cursor < p.top {
		p.top = p.cursor
	}
	if p.cursor >= p.top+visible {
		p.top = p.cursor - visible + 1
	}
	if maxTop := len(p.sessions) - visible; p.top > maxTop {
		p.top = max(0, maxTop)
	}
}

// formatAge renders when a session was last used.
func formatAge(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	switch d := time.Since(t); {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Local().Format("2006-01-02 15:04")
	}
}
