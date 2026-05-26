// Package samples is the binary-embedded form of the sample helper
// scripts shipped under dist/samples/.
//
// Why embed: an agent that reads /skill.md and tries to follow up by
// fetching a helper script gets the best UX if the script comes from
// the SAME origin it's already trusting. Sending agents to GitHub raw
// URLs adds a second trust boundary, depends on the repo being public
// at fetch time, and breaks for agents whose runtime restricts
// outbound network to a single host. Same-origin fetch from
// https://kb.zenryoku.work/samples/<name> avoids all of that.
//
// The on-disk path under dist/samples/agent-helpers/ remains the
// editable source — `go:embed` snapshots the files at build time.
// Edit, run `go build`, redeploy.
package samples

import "embed"

// AgentHelpers exposes the sample shell scripts that wrap common
// omoikane usage paths (post entry, lookup, feedback). The dashboard
// serves them under /samples/{name} with content-type
// text/x-shellscript.
//
//go:embed agent-helpers/*.sh
var AgentHelpers embed.FS
