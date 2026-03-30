// rat: Run AnyThing.
//
// One binary. Every REPL language. Every client shares one namespace.
// The CLI is built with Cobra — each subcommand lives in its own file
// under cmd/rat/commands/.
package main

import (
	"fmt"
	"os"

	"github.com/maximerivest/rat/cmd/rat/commands"
)

func main() {
	if err := commands.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "rat: %s\n", err)
		os.Exit(1)
	}
}
