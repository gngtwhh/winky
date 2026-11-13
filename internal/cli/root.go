// Package cli wires the winky command line: flag parsing, startup wiring, and
// process-level concerns such as signal handling.
package cli

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// options holds the startup flag values. It is separate from the cobra
// command so the startup path can be tested without parsing os.Args.
type options struct {
	configPath  string
	catalogPath string
	promptPath  string
	resume      bool
}

func (o *options) bind(f *pflag.FlagSet) {
	f.StringVar(&o.configPath, "config", "", "path to config.json (default: defaults + .env)")
	f.StringVar(&o.catalogPath, "catalog", "catalog.json", "path to model catalog.json")
	f.StringVar(&o.promptPath, "prompt", "", "path to a system prompt file (default: the built-in prompt)")
	f.BoolVarP(&o.resume, "resume", "r", false, "choose a previous session started in the current directory")
}

// NewRootCmd builds the winky command tree. Running it starts the TUI.
func NewRootCmd() *cobra.Command {
	return newRootCmd(runTUI)
}

func newRootCmd(run func(context.Context, *options) error) *cobra.Command {
	opts := &options{}
	cmd := &cobra.Command{
		Use:   "winky",
		Short: "Start the winky terminal UI",
		Long: "winky runs a terminal chat interface over an agent that can read, " +
			"edit, and run code in the current workspace.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true, // main reports the error once
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd.Context(), opts)
		},
	}
	opts.bind(cmd.Flags())
	return cmd
}

// Execute runs the CLI with a context canceled on interrupt signals.
func Execute() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd := NewRootCmd()
	cmd.SetArgs(normalizeArgs(os.Args[1:]))
	return cmd.ExecuteContext(ctx)
}

// normalizeArgs keeps the historical single-dash long flags working. pflag
// reads a single dash as a shorthand prefix, so "-config" would otherwise fail
// while "--config" is the canonical spelling.
func normalizeArgs(args []string) []string {
	out := make([]string, len(args))
	literal := false
	for i, arg := range args {
		out[i] = arg
		if literal {
			continue
		}
		if arg == "--" {
			literal = true
			continue
		}
		for _, name := range []string{"config", "catalog", "prompt"} {
			legacy := "-" + name
			if arg == legacy || strings.HasPrefix(arg, legacy+"=") {
				out[i] = "-" + arg
				break
			}
		}
	}
	return out
}
