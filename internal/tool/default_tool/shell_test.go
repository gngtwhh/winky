package defaulttool

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"winky/internal/tool"
)

func itoa(n int) string { return strconv.Itoa(n) }

func TestTailBufferKeepsTheTail(t *testing.T) {
	b := newTailBuffer(3)
	if _, err := b.Write([]byte("abcdef")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := b.Text(); got != "def" {
		t.Fatalf("Text = %q, want def", got)
	}
	if b.Dropped() != 3 {
		t.Fatalf("Dropped = %d, want 3", b.Dropped())
	}
	if b.Retained() != 3 {
		t.Fatalf("Retained = %d, want 3", b.Retained())
	}
}

func TestTailBufferShortOutputIsKeptWhole(t *testing.T) {
	b := newTailBuffer(64)
	_, _ = b.Write([]byte("hi"))
	if got := b.Text(); got != "hi" {
		t.Fatalf("Text = %q, want hi", got)
	}
	if b.Dropped() != 0 {
		t.Fatalf("Dropped = %d, want 0", b.Dropped())
	}
}

// Many small writes must wrap around and still keep the newest bytes in order.
func TestTailBufferWrapsAcrossWrites(t *testing.T) {
	b := newTailBuffer(4)
	for _, chunk := range []string{"ab", "cd", "ef", "gh"} {
		_, _ = b.Write([]byte(chunk))
	}
	if got := b.Text(); got != "efgh" {
		t.Fatalf("Text = %q, want efgh", got)
	}
	if b.Dropped() != 4 {
		t.Fatalf("Dropped = %d, want 4", b.Dropped())
	}
}

// A window that starts inside a multi-byte rune must not produce invalid UTF-8.
func TestTailBufferStartsOnRuneBoundary(t *testing.T) {
	b := newTailBuffer(4)
	_, _ = b.Write([]byte("你好"))
	text := b.Text()
	if !utf8.ValidString(text) {
		t.Fatalf("Text = %q, want valid UTF-8", text)
	}
	if len(text) == 0 {
		t.Fatal("Text is empty, want the tail of the last rune")
	}
}

func TestShellDeadlineReadsTheArgument(t *testing.T) {
	tests := []struct {
		args string
		want int // seconds
	}{
		{`{"command":"ls"}`, 0},
		{`{"command":"ls","timeout":30}`, 30},
		{`{"command":"ls","timeout":0}`, 0},
		{`{"command":"ls","timeout":-5}`, 0},
		{`not json`, 0},
	}
	for _, tc := range tests {
		got := shellDeadline(tool.Call{Name: "shell", Arguments: tc.args})
		if int(got.Seconds()) != tc.want {
			t.Fatalf("shellDeadline(%s) = %v, want %ds", tc.args, got, tc.want)
		}
	}
}

func TestShellRunsAndReportsFailure(t *testing.T) {
	dir := t.TempDir()
	sh := shell(tool.NewPolicy(tool.FullAccess), dir)

	ok, err := sh.Invoke(t.Context(), tool.Call{
		Name: "shell", Arguments: `{"command":"` + echoCommand("hi") + `"}`,
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if ok.IsError || !strings.Contains(ok.Output, "hi") {
		t.Fatalf("result = %+v, want hi", ok)
	}

	bad, err := sh.Invoke(t.Context(), tool.Call{
		Name: "shell", Arguments: `{"command":"` + exitCommand(3) + `"}`,
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !bad.IsError {
		t.Fatalf("result = %+v, want an error for a non-zero exit", bad)
	}
}

// A cancelled command must stop promptly: without killing the process tree the
// call would keep waiting for a command that is still running.
func TestShellCancelKillsTheCommand(t *testing.T) {
	sh := shell(tool.NewPolicy(tool.FullAccess), t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	res, err := sh.Invoke(ctx, tool.Call{
		Name: "shell", Arguments: `{"command":"` + hangCommand() + `"}`,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("cancel took %s, want the command killed", elapsed)
	}
	if !res.IsError {
		t.Fatalf("result = %+v, want an error after cancellation", res)
	}
}

// A descendant that keeps the output pipe open after the shell exits must not be
// able to hold the call: the wait is bounded by shellExitGrace.
func TestShellDoesNotWaitForDetachedDescendants(t *testing.T) {
	// The working directory is the system temp dir, not t.TempDir(): the detached
	// descendant keeps it as its cwd and would block the cleanup.
	sh := shell(tool.NewPolicy(tool.FullAccess), os.TempDir())

	start := time.Now()
	if _, err := sh.Invoke(t.Context(), tool.Call{
		Name: "shell", Arguments: `{"command":"` + detachedCommand() + `"}`,
	}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	// The descendant runs for about five seconds, so anything close to that means
	// the call waited for it instead of for the shell.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("wait took %s, want the inherited pipe not to hold the call", elapsed)
	}
}
