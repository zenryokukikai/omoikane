// Package version is the single source of truth for the omoikane
// application version. It is a leaf package (imports nothing internal) so
// any layer — server, api, dashboard — can read it without import cycles.
package version

// App is the human-facing semver of the omoikane application. Bump it on a
// meaningful release (this is the one place to change). It is distinct from
// docs/design.md's document version, which tracks the design, not the build.
const App = "0.10.0"

// Build is the git short SHA the binary was built from, injected at link
// time:
//
//	go build -ldflags "-X github.com/zenryokukikai/omoikane/internal/version.Build=$(git rev-parse --short HEAD)"
//
// It defaults to "dev" for local / un-stamped builds. The deploy pipeline
// passes the real SHA so "what is actually running" is always precise even
// if App was not bumped.
var Build = "dev"

// String renders "App (Build)", e.g. "0.10.0 (a1b2c3d)" or "0.10.0 (dev)".
func String() string { return App + " (" + Build + ")" }
