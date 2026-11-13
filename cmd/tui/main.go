// Command tui runs the winky terminal UI: a Bubble Tea front end over the same
// agent loop as cmd/agent. Startup flags and wiring live in internal/cli.
package main

import (
	"log"

	"winky/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		log.Fatal(err)
	}
}
