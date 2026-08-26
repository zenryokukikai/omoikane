// omoikane-gate: the /talk delivery gateway (issue #104 slice G3b).
//
// One process holds one protocol 2 connection per ACTIVE personal
// librarian (platform ruling: one process, N sockets) and translates
// between the external gate wire and omoikane's chat HTTP surface. All
// logic lives in internal/gate/runtime; this main only parses the
// environment and runs until SIGINT/SIGTERM.
//
// Required environment:
//
//	OPENCRAB_GATE_SOCKET  the core's Unix socket path
//	KB_BASE_URL           omoikane API base URL
//	GATEWAY_TOKEN         omoikane API token with scope "gateway".
//	                      MUST be issued USER-LESS (user_id empty):
//	                      binding it to a user — an agent-role user
//	                      especially — would let the author-stamping
//	                      path compound that user's authority onto the
//	                      gateway's. Issue with the admin CLI leaving
//	                      user_id blank.
//
// Optional: GATE_DISCOVERY_INTERVAL (60s), GATE_RECONNECT_MIN (1s),
// GATE_RECONNECT_MAX (60s), GATE_HELLO_REVISION (1).
//
// Conformance static mode (issue #104 QC E2E): setting
// GATE_STATIC_INSTANCES to a comma-separated list of canonical
// lowercase instance UUIDs runs the gate WITHOUT a KB —
// KB_BASE_URL/GATEWAY_TOKEN are not required (the socket still is).
// Each listed instance is dialed and helloed directly; binds are acked
// (no replay), activity is a no-op, and every say answers
// err(code="external_rejected") — honest, since with no KB the gateway
// performs zero external I/O and can never deliver. Intended for a
// platform-side test harness driving the core end of the wire with
// this gate as the subject. See docs/gateway-runbook.md
// (conformance 静的モード).
//
// NOT END-TO-END VERIFIED: the platform's real UDS core does not exist
// yet. This binary stays on the integration branch until it does.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/zenryokukikai/omoikane/internal/gate/runtime"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := runtime.FromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "omoikane-gate: refusing to start:\n%v\n", err)
		os.Exit(2)
	}
	rt, err := runtime.New(cfg, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "omoikane-gate: refusing to start:\n%v\n", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if n := len(cfg.StaticInstances); n > 0 {
		log.Info("omoikane-gate starting (static conformance mode)",
			"socket", cfg.SocketPath, "instances", n)
	} else {
		log.Info("omoikane-gate starting", "socket", cfg.SocketPath, "kb", cfg.KBBaseURL)
	}
	if err := rt.Run(ctx); err != nil {
		log.Error("omoikane-gate stopped", "err", err)
		os.Exit(1)
	}
	log.Info("omoikane-gate stopped")
}
