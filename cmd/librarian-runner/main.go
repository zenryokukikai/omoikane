// librarian-runner — Phase 5 harness stub.
//
// Loads a skill bundle from disk, registers an instance with the Core
// kb-server, and emits a heartbeat at the configured cadence. The
// actual LLM call / tool-execution loop is deferred to the configured
// agent runtime (Claude Code / OpenCode / etc.) — Phase 5 ships this
// stub so the contract is testable end-to-end while the agent
// integration is being designed.
package main

import (
	"os"

	"github.com/kojira/omoikane/internal/librunner"
)

func main() {
	os.Exit(librunner.Run(os.Args[1:], os.Stdout, os.Stderr))
}
