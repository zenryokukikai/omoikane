// Command kb-server is the omoikane HTTP server and admin CLI entry point.
// All logic lives in internal/server; this file is a 3-line shim so the
// cmd/ directory stays uncovered by design (see docs/design.md §19).
package main

import (
	"os"

	"github.com/zenryokukikai/omoikane/internal/server"
)

func main() {
	os.Exit(server.Run(os.Args[1:], os.Stdout, os.Stderr))
}
