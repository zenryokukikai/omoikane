// Command kb is the omoikane CLI entry point. All logic lives in
// internal/cli; this file is a 3-line shim so the cmd/ directory stays
// uncovered by design (see docs/design.md §19).
package main

import (
	"os"

	"github.com/kojira/omoikane/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
