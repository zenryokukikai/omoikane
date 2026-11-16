// Package runtime is the omoikane-gate orchestration layer (issue
// #104): it discovers the personal librarians to serve
// (GET /v1/gateway/librarians), holds one V3 connection per gate
// instance (platform ruling: one process may hold N sockets), and
// translates between the gate wire (protocol 3: session key = /talk
// thread id) and omoikane's chat HTTP surface — say → POST
// /v1/librarian/chat, activity → chat.status broadcasts, SSE
// chat.message → said.
//
// The package deliberately splits policy from transport: internal/gate
// speaks the wire, the KB interface (kb.go) speaks omoikane HTTP, and
// this layer only routes between them. Both sides are seams — tests
// drive the wire with a scripted fake core over net.Pipe and omoikane
// with an httptest server, and the real deployment swaps nothing but a
// socket path and a base URL.
package runtime

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/zenryokukikai/omoikane/internal/gate"
)

// Env variable names. OPENCRAB_GATE_SOCKET and GATEWAY_TOKEN are fixed
// by the G3b design; the rest follow the repo's GATE_* convention
// (internal/config).
const (
	EnvSocket            = "OPENCRAB_GATE_SOCKET"
	EnvKBBaseURL         = "KB_BASE_URL"
	EnvToken             = "GATEWAY_TOKEN" //nolint:gosec // env var NAME, not a credential
	EnvDiscoveryInterval = "GATE_DISCOVERY_INTERVAL"
	EnvReconnectMin      = "GATE_RECONNECT_MIN"
	EnvReconnectMax      = "GATE_RECONNECT_MAX"
	EnvStaticInstances   = "GATE_STATIC_INSTANCES"
	// EnvStaticAgent names the core agent static instances speak for
	// (protocol 3 hello requires agent_id). Static mode only.
	EnvStaticAgent = "GATE_STATIC_AGENT"
)

// Config parameterizes one Runtime.
type Config struct {
	// SocketPath is the core's Unix socket. In the real deployment the
	// platform provides it (volume-shared); until then it is a config
	// value. Refuse to start when empty — a gate with no core is a
	// misconfiguration, not a degraded mode.
	SocketPath string

	// KBBaseURL is the omoikane API base (scheme://host, no trailing
	// slash needed) and Token the gateway-scoped bearer token. Both
	// required — except in static conformance mode (StaticInstances
	// non-empty), where no KB exists.
	KBBaseURL string
	Token     string

	// StaticInstances switches the runtime into wire-conformance
	// static mode when non-empty (env GATE_STATIC_INSTANCES,
	// comma-separated canonical lowercase instance UUIDs). In this
	// mode the runtime has NO omoikane KB: roster discovery and the
	// inbound SSE loop are skipped, each listed instance is dialed
	// and helloed directly (agent_id = StaticAgent), there is no
	// replay, activity is a no-op, and EVERY say answers
	// err(code="external_rejected") — honest under the V3 trust
	// principle, because with no KB the gateway performs zero external
	// I/O and the delivery is definitively not accepted. Static mode
	// exists so a platform-side test harness can drive the core side
	// of the wire with this gate as the subject
	// (hello/activity/said + the say err path).
	StaticInstances []string
	// StaticAgent is the agent_id static instances present in hello
	// (required when StaticInstances is set).
	StaticAgent string

	// DiscoveryInterval is how often the librarian roster is re-polled
	// to pick up newly provisioned librarians. Default 60s.
	DiscoveryInterval time.Duration

	// ReconnectMin/ReconnectMax bound the per-instance reconnect
	// backoff (exponential, doubling from Min to Max). Defaults 1s/60s.
	ReconnectMin time.Duration
	ReconnectMax time.Duration

	// Dial opens the byte stream to the core. nil = Unix-socket dial of
	// SocketPath. Tests inject net.Pipe here.
	Dial DialFunc

	// KB is the omoikane HTTP surface. nil = the real HTTP client built
	// from KBBaseURL/Token. Tests may inject a fake.
	KB KB

	// Cursors advances the per-thread catch-up cursor. nil = the real
	// httpKB (PUT /v1/gateway/threads/{id}/cursor) when the KB is the
	// production client, else the no-op noCursorStore. The no-op replays
	// from the beginning of a thread and relies on origin idempotency.
	Cursors CursorStore
}

// FromEnv builds a Config from the process environment. Missing
// required values are reported all at once so the operator fixes one
// startup, not three.
func FromEnv() (Config, error) {
	cfg := Config{
		SocketPath:        os.Getenv(EnvSocket),
		KBBaseURL:         os.Getenv(EnvKBBaseURL),
		Token:             os.Getenv(EnvToken),
		StaticInstances:   splitInstanceList(os.Getenv(EnvStaticInstances)),
		StaticAgent:       os.Getenv(EnvStaticAgent),
		DiscoveryInterval: 60 * time.Second,
		ReconnectMin:      time.Second,
		ReconnectMax:      60 * time.Second,
	}
	var errs []error
	if d, err := envDuration(EnvDiscoveryInterval); err != nil {
		errs = append(errs, err)
	} else if d > 0 {
		cfg.DiscoveryInterval = d
	}
	if d, err := envDuration(EnvReconnectMin); err != nil {
		errs = append(errs, err)
	} else if d > 0 {
		cfg.ReconnectMin = d
	}
	if d, err := envDuration(EnvReconnectMax); err != nil {
		errs = append(errs, err)
	} else if d > 0 {
		cfg.ReconnectMax = d
	}
	if err := cfg.validate(); err != nil {
		errs = append(errs, err)
	}
	return cfg, errors.Join(errs...)
}

// splitInstanceList splits a comma-separated instance list, trimming
// whitespace and dropping empty items. Grammar (canonical lowercase
// UUID, no duplicates) is checked in validate() — one contract, one
// place.
func splitInstanceList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if id := strings.TrimSpace(part); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func envDuration(name string) (time.Duration, error) {
	v := os.Getenv(name)
	if v == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration (e.g. 30s), got %q", name, v)
	}
	return d, nil
}

// validate reports every hard misconfiguration. KBBaseURL/Token are
// checked only when no injected KB replaces the HTTP client AND the
// runtime is not in static conformance mode (StaticInstances set) —
// static mode is the ONLY mode that runs KB-less; the socket path stays
// required in every mode.
func (c *Config) validate() error {
	var errs []error
	if c.SocketPath == "" {
		errs = append(errs, fmt.Errorf("%s is required: the core's Unix socket path", EnvSocket))
	}
	if c.KB == nil && len(c.StaticInstances) == 0 {
		if c.KBBaseURL == "" {
			errs = append(errs, fmt.Errorf("%s is required: the omoikane API base URL", EnvKBBaseURL))
		}
		if c.Token == "" {
			errs = append(errs, fmt.Errorf("%s is required: a gateway-scoped omoikane API token", EnvToken))
		}
	}
	seen := make(map[string]bool, len(c.StaticInstances))
	for _, id := range c.StaticInstances {
		switch {
		case !gate.IsCanonicalUUID(id):
			errs = append(errs, fmt.Errorf("%s: %q is not a canonical lowercase UUID", EnvStaticInstances, id))
		case seen[id]:
			errs = append(errs, fmt.Errorf("%s: duplicate instance id %q", EnvStaticInstances, id))
		}
		seen[id] = true
	}
	if c.DiscoveryInterval <= 0 || c.ReconnectMin <= 0 || c.ReconnectMax < c.ReconnectMin {
		errs = append(errs, errors.New("intervals must be positive and reconnect max >= min"))
	}
	if len(c.StaticInstances) > 0 && c.StaticAgent == "" {
		errs = append(errs, fmt.Errorf("%s is required with %s: the agent_id the static instances speak for", EnvStaticAgent, EnvStaticInstances))
	}
	return errors.Join(errs...)
}
