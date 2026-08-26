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
	log.Info("omoikane-gate starting", "socket", cfg.SocketPath, "kb", cfg.KBBaseURL)
	if err := rt.Run(ctx); err != nil {
		log.Error("omoikane-gate stopped", "err", err)
		os.Exit(1)
	}
	log.Info("omoikane-gate stopped")
}
