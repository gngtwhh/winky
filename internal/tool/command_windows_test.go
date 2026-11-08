//go:build windows

package tool

// echoCommand returns a shell command that prints hi.
func echoCommand(text string) string { return "echo " + text }

// exitCommand returns a shell command that exits with the given status.
func exitCommand(code int) string {
	return "exit /b " + itoa(code)
}

// hangCommand returns a shell command that keeps running until it is killed.
func hangCommand() string { return "ping -n 60 127.0.0.1 > nul" }

// detachedCommand leaves a descendant holding the output pipe after the shell
// itself has exited.
func detachedCommand() string { return "start /b ping -n 6 127.0.0.1" }
