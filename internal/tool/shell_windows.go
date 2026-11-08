//go:build windows

package tool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

// isolateShell gives the command its own process group and installs the cancel
// hook that kills the tree.
func isolateShell(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
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
