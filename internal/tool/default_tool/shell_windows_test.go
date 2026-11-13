//go:build windows

package defaulttool

import (
	"strings"
	"testing"
	"unicode/utf8"

	"winky/internal/tool"
)

// Console output arrives in the console code page, not UTF-8. Left alone, those
// bytes are mojibake on screen and become U+FFFD once the transcript is encoded,
// which loses the text for good.
func TestDecodeOutputConvertsConsoleCodePage(t *testing.T) {
	// GBK (code page 936) for "周三", the weekday cmd printed for %DATE%.
	const gbk = "\xd6\xdc\xc8\xfd"

	if got := decodeOutput(gbk); got != "周三" {
		t.Fatalf("decodeOutput = %q, want 周三", got)
	}

	// Output that already is valid UTF-8 must survive untouched: git, node and
	// python in UTF-8 mode do not care what the console says.
	const utf8Text = "2026-11-11 星期三"
	if got := decodeOutput(utf8Text); got != utf8Text {
		t.Fatalf("decodeOutput = %q, want it unchanged", got)
	}
	if got := decodeOutput(""); got != "" {
		t.Fatalf("decodeOutput(\"\") = %q", got)
	}
}

// A command that carries its own quotes must reach the program intact. os/exec
// escaping plus cmd's own rules used to cancel each other out and leave the
// quotes in the argument, so `powershell -Command "(Get-Date)"` printed the
// command instead of running it.
func TestShellKeepsQuotesIntact(t *testing.T) {
	sh := shell(tool.NewPolicy(tool.FullAccess), t.TempDir())

	tests := []struct {
		command string
		want    string
	}{
		// %~A strips the quotes only if the argument really was quoted.
		{`for %A in ("a b") do @echo [%~A]`, "[a b]"},
		{`if "x y"=="x y" (echo same) else (echo diff)`, "same"},
		{`for %A in ("x" "y") do @echo [%~A]`, "[x]\r\n[y]"},
	}
	for _, tc := range tests {
		res, err := sh.Invoke(t.Context(), tool.Call{
			Name:      "shell",
			Arguments: `{"command":` + quoteJSON(tc.command) + `}`,
		})
		if err != nil {
			t.Fatalf("Invoke(%s): %v", tc.command, err)
		}
		if got := strings.TrimRight(res.Output, "\r\n"); got != tc.want {
			t.Fatalf("%s => %q, want %q", tc.command, got, tc.want)
		}
	}
}

// quoteJSON wraps s as a JSON string literal.
func quoteJSON(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// Whatever the console code page is, what leaves the shell tool must be valid
// UTF-8: the TUI measures it, and the transcript encoder would replace invalid
// bytes with U+FFFD and lose them.
func TestShellOutputIsValidUTF8(t *testing.T) {
	sh := shell(tool.NewPolicy(tool.FullAccess), t.TempDir())

	res, err := sh.Invoke(t.Context(), tool.Call{
		Name: "shell", Arguments: `{"command":"echo %DATE% \u0026 date"}`,
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !utf8.ValidString(res.Output) {
		t.Fatalf("output is not valid UTF-8: %q", res.Output)
	}
}
