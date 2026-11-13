package defaulttool

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"winky/internal/llm"
	"winky/internal/tool"
)

// maxShellBytes is how much command output is kept for the model. The tail is
// kept rather than the head: build and test failures print their cause last.
const maxShellBytes = 64 << 10

// shellExitGrace bounds the wait for inherited stdio handles after the shell has
// exited. A short-lived shell can exit while a detached descendant still holds
// the pipe open; without this the collection would wait for an EOF that may
// never come.
const shellExitGrace = 100 * time.Millisecond

var shellSpec = llm.ToolSpec{
	Name: "shell",
	Description: "Run a shell command and return its combined output. " +
		"Long output keeps only its tail. Provide timeout to cap how long it may run.",
	Parameters: object(
		map[string]any{
			"command": stringProp("Command to run."),
			"timeout": intProp(
				"Optional limit in seconds. Omit it and the runner applies its own ceiling; "+
					"when the limit is reached the whole process tree is killed.",
				1, 0,
			),
		},
		"command",
	),
}

type shellArgs struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// shell returns a tool that runs a command in dir.
//
// The command runs in its own process group so that cancellation kills the whole
// tree: the shell is only an intermediate, and the process that actually does
// the work is its child. Killing just the shell would leave the work running and
// leave the output pipe open, which is how a cancelled call used to hang.
func shell(_ *tool.Policy, dir string) tool.Tool {
	return tool.WithBudget(
		tool.WithGuard(
			tool.Func[shellArgs](shellSpec, func(ctx context.Context, a shellArgs) (tool.Result, error) {
				if a.Command == "" {
					return tool.Result{}, errors.New("command must not be empty")
				}
				return runShell(ctx, dir, a.Command)
			}),
			func(a shellArgs) (tool.Request, error) {
				return tool.Request{Tool: "shell", Access: tool.AccessShell, Target: a.Command}, nil
			},
		),
		shellDeadline,
	)
}

// shellDeadline honours the optional timeout argument. Zero means the runner's
// own ceiling applies.
func shellDeadline(c tool.Call) time.Duration {
	args, err := tool.DecodeArgs[shellArgs](c)
	if err != nil || args.Timeout <= 0 {
		return 0
	}
	return time.Duration(args.Timeout) * time.Second
}

func runShell(ctx context.Context, dir, command string) (tool.Result, error) {
	cmd := shellCommand(ctx, command)
	cmd.Dir = dir
	isolateShell(cmd)

	out := newTailBuffer(maxShellBytes)
	cmd.Stdout = out
	cmd.Stderr = out

	err := cmd.Run()
	text := decodeOutput(out.Text())
	if dropped := out.Dropped(); dropped > 0 {
		text = fmt.Sprintf(
			"[output truncated: kept the last %s of %s]\n%s",
			humanSize(out.Retained()), humanSize(out.Retained()+dropped), text,
		)
	}

	switch {
	case ctx.Err() != nil:
		// Return what ran as a result, not an error: the registry discards the
		// output attached to an error, and the runner is the one that knows why
		// the call stopped.
		return tool.Result{Output: text, IsError: true}, nil
	case err == nil:
		return tool.Result{Output: text}, nil
	case errors.Is(err, exec.ErrWaitDelay):
		return tool.Result{
			Output:  joinStatus(text, "(output handles stayed open after exit; stopped waiting for them)"),
			IsError: true,
		}, nil
	default:
		return tool.Result{Output: joinStatus(text, err.Error()), IsError: true}, nil
	}
}

// joinStatus appends a status line under whatever output already exists.
func joinStatus(text, status string) string {
	if text == "" {
		return status
	}
	return text + "\n" + status
}
