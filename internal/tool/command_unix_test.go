//go:build !windows

package tool

// echoCommand returns a shell command that prints hi.
func echoCommand(text string) string { return "echo " + text }

// exitCommand returns a shell command that exits with the given status.
func exitCommand(code int) string {
	return "exit " + itoa(code)
}

// hangCommand returns a shell command that keeps running until it is killed.
func hangCommand() string { return "sleep 60" }

// detachedCommand leaves a descendant holding the output pipe after the shell
// itself has exited.
func detachedCommand() string { return "sleep 5 &" }
