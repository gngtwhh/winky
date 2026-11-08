//go:build !windows

package tool

import (
	"os"
	"os/exec"
	"syscall"
)

// isolateShell puts the command in its own process group and installs the cancel
// hook that kills the group.
func isolateShell(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = shellExitGrace
	cmd.Cancel = func() error { return killShellTree(cmd) }
}

// killShellTree signals the whole process group. The command runs under a shell,
// so the direct child is only an intermediate: killing it alone leaves the
// actual work running and the output pipe open.
func killShellTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
