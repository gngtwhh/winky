package tool

import (
	"context"
	"errors"
	"os/exec"
	"runtime"

	"winky/internal/llm"
)

const maxShellBytes = 64 << 10

var shellSpec = llm.ToolSpec{
	Name:        "shell",
	Description: "Run a shell command and return its combined output.",
	Parameters: object(
		map[string]any{
			"command": stringProp("Command to run."),
		},
		"command",
	),
}

type shellArgs struct {
	Command string `json:"command"`
}

// shell returns a tool that runs a command in dir. Output is capped before the
// registry's own cap, and a non-zero exit is reported as an error result.
func shell(p *Policy, dir string) Tool {
	return Func[shellArgs](shellSpec, func(ctx context.Context, a shellArgs) (Result, error) {
		if err := p.guard(ctx, Request{Tool: "shell", Access: AccessShell, Target: a.Command}); err != nil {
			return Result{}, err
		}
		if a.Command == "" {
			return Result{}, errors.New("command must not be empty")
		}

		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(ctx, "cmd", "/C", a.Command)
		} else {
			cmd = exec.CommandContext(ctx, "sh", "-c", a.Command)
		}
		cmd.Dir = dir

		out, err := cmd.CombinedOutput()
		if len(out) > maxShellBytes {
			out = out[:maxShellBytes]
		}
		if err != nil {
			return Result{Output: string(out) + "\n" + err.Error(), IsError: true}, nil
		}
		return Result{Output: string(out)}, nil
	})
}
