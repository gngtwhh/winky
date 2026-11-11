//go:build !windows

package tool

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

// shellCommand builds the command that runs a shell line. The command is one
// argument to sh, so nothing between it and the program is re-parsed.
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", command)
}

// isolateShell puts the command in its own process group and installs the cancel
// hook that kills the group.
func isolateShell(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
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

// decodeOutput is the identity here: the shell writes UTF-8.
func decodeOutput(s string) string { return s }
