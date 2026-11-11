//go:build windows

package tool

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"unicode/utf8"
	"unsafe"
)

// utf8CodePage is the console code page that needs no conversion.
const utf8CodePage = 65001

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleOutputCP  = kernel32.NewProc("GetConsoleOutputCP")
	procGetOEMCP            = kernel32.NewProc("GetOEMCP")
	procMultiByteToWideChar = kernel32.NewProc("MultiByteToWideChar")
)

// shellCommand builds the command that runs a shell line.
//
// The command line is assembled here rather than by os/exec, because cmd.exe has
// its own idea of where quotes begin and end. Letting os/exec escape the line
// with C runtime rules and then handing it to cmd means the two rules cancel
// each other out: `powershell -Command "(Get-Date)"` arrives as a quoted string
// literal and is printed instead of evaluated. /S keeps everything between the
// outer quotes literal, so a command may carry its own quotes intact.
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cmd")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine: `cmd /S /C "` + command + `"`,
	}
	return cmd
}

// isolateShell gives the command its own process group and installs the cancel
// hook that kills the tree.
func isolateShell(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags = syscall.CREATE_NEW_PROCESS_GROUP
	cmd.WaitDelay = shellExitGrace
	cmd.Cancel = func() error { return killShellTree(cmd) }
}

// killShellTree kills the command and every descendant. taskkill is the only
// portable way to walk the tree on Windows, and it is invoked from the trusted
// system directory so cleanup does not depend on PATH.
func killShellTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	taskkill := filepath.Join(os.Getenv("SystemRoot"), "System32", "taskkill.exe")
	err := exec.Command(
		taskkill, "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid),
	).Run()
	if err != nil {
		return cmd.Process.Kill()
	}
	return nil
}

// decodeOutput converts console output to UTF-8.
//
// cmd.exe and most console programs write bytes in the console code page (936,
// GBK, on a Chinese Windows), not UTF-8. Left alone those bytes are mojibake on
// screen, and worse: json replaces them with U+FFFD when the transcript is
// written, so the original text is gone for good.
//
// Output that already is valid UTF-8 is left untouched — git, node and python in
// UTF-8 mode emit UTF-8 whatever the console says.
func decodeOutput(s string) string {
	if s == "" || utf8.ValidString(s) {
		return s
	}
	cp := consoleCodePage()
	if cp == 0 || cp == utf8CodePage {
		return s
	}
	decoded, err := fromCodePage(cp, s)
	if err != nil {
		return s
	}
	return decoded
}

// consoleCodePage is the code page a child's console output uses: what chcp
// sets, and the OEM code page when there is no console to ask.
func consoleCodePage() uint32 {
	if cp, _, _ := procGetConsoleOutputCP.Call(); cp != 0 {
		return uint32(cp)
	}
	if cp, _, _ := procGetOEMCP.Call(); cp != 0 {
		return uint32(cp)
	}
	return 0
}

func fromCodePage(cp uint32, s string) (string, error) {
	raw := []byte(s)
	needed, _, _ := procMultiByteToWideChar.Call(
		uintptr(cp), 0,
		uintptr(unsafe.Pointer(&raw[0])), uintptr(len(raw)),
		0, 0,
	)
	if needed == 0 {
		return "", errors.New("shell: cannot convert console output")
	}
	wide := make([]uint16, needed)
	written, _, _ := procMultiByteToWideChar.Call(
		uintptr(cp), 0,
		uintptr(unsafe.Pointer(&raw[0])), uintptr(len(raw)),
		uintptr(unsafe.Pointer(&wide[0])), needed,
	)
	if written == 0 {
		return "", errors.New("shell: cannot convert console output")
	}
	return syscall.UTF16ToString(wide), nil
}
