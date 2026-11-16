package runtime

// Static conformance mode pins (GATE_STATIC_INSTANCES, issue #104 QC
// E2E): fixed instance set, no KB at all (rt.kb stays nil — any KB call
// would panic the test, which is the point), no replay, every say
// external_rejected, activity a no-op.

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

// startStaticRuntime boots a Runtime in static mode: no KB server
// exists anywhere — KBBaseURL/Token empty, KB nil.
func startStaticRuntime(t *testing.T, instances ...string) *harness {
	t.Helper()
	cores := make(chan net.Conn, 8)
	cfg := Config{
		SocketPath:        "/nonexistent/test.sock",
		StaticInstances:   instances,
		StaticAgent:       "agent-static",
		DiscoveryInterval: 25 * time.Millisecond,
		ReconnectMin:      10 * time.Millisecond,
		ReconnectMax:      50 * time.Millisecond,
		Dial: func(ctx context.Context, _ string) (io.ReadWriteCloser, error) {
			client, server := net.Pipe()
			cores <- server
			return client, nil
		},
	}
	rt, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if rt.kb != nil {
		t.Fatal("static mode built a KB client; static mode must have NO KB")
	}
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		_ = rt.Run(ctx)
	}()
	h := &harness{t: t, kb: nil, cores: cores, rt: rt, stop: cancel, ended: ended}
	t.Cleanup(func() {
		cancel()
		select {
		case <-ended:
		case <-time.After(5 * time.Second):
			t.Error("runtime did not stop")
		}
	})
	return h
}

// Static mode: both listed instances hello directly (no roster
// discovery — there is no KB to discover from) as the configured
// agent, and a say with a perfectly valid payload still answers
// external_rejected —
// the rejection is the mode (no KB, zero external I/O), not payload
// validation. The connection survives the err (rejected ≠ violation).
func TestStaticModeHelloSayRejected(t *testing.T) {
	h := startStaticRuntime(t, testInstanceA, testInstanceB)

	// Dial order is slice order, but identify by hello to stay robust.
	cores := map[string]*fakeCore{}
	for i := 0; i < 2; i++ {
		c := h.nextCore()
		cores[c.serveHandshakeAny()] = c
	}
	if cores[testInstanceA] == nil || cores[testInstanceB] == nil {
		t.Fatalf("helloed instances = %v, want A and B", cores)
	}
	core := cores[testInstanceA]

	// Say with a VALID payload → err external_rejected.
	core.send(map[string]any{
		"id": testDeliveryID, "m": "say", "session": testThread,
		"payload": map[string]any{"text": "a real reply"},
	})
	m := core.recv()
	if got := core.str(m, "id"); got != testDeliveryID {
		t.Fatalf("say response id=%q, want %q", got, testDeliveryID)
	}
	if core.str(m, "m") != "err" || core.str(m, "code") != "external_rejected" {
		t.Fatalf("static say response = %v, want err external_rejected", m)
	}

	// Activity: dropped silently (no KB to broadcast to), connection
	// kept — the next say still gets answered.
	core.send(map[string]any{
		"m": "activity", "session": testThread, "activity_id": "act-1",
		"state": "started",
	})
	const delivery2 = "0190a1b2-c3d4-7e5f-8a6b-0000000000ef"
	core.send(map[string]any{
		"id": delivery2, "m": "say", "session": testThread,
		"payload": map[string]any{"text": "again"},
	})
	m = core.recv()
	if core.str(m, "id") != delivery2 || core.str(m, "code") != "external_rejected" {
		t.Fatalf("follow-up say response = %v, want err external_rejected for %s", m, delivery2)
	}
}

// validate(): static mode relaxes exactly KB_BASE_URL/GATEWAY_TOKEN —
// the same config without the static list still refuses to start
// (regression pin: the relaxation must never leak into normal mode).
func TestStaticModeValidateRelaxesOnlyKBVars(t *testing.T) {
	base := Config{
		SocketPath:        "/tmp/core.sock",
		DiscoveryInterval: time.Second, ReconnectMin: time.Second,
		ReconnectMax: time.Second,
	}

	static := base
	static.StaticInstances = []string{testInstanceA}
	static.StaticAgent = "agent-static"
	if err := static.validate(); err != nil {
		t.Fatalf("static config without KB vars refused: %v", err)
	}

	noAgent := static
	noAgent.StaticAgent = ""
	if err := noAgent.validate(); err == nil {
		t.Fatal("static mode accepted a config without GATE_STATIC_AGENT")
	}

	normal := base
	if err := normal.validate(); err == nil {
		t.Fatal("normal mode accepted a config without KB_BASE_URL/GATEWAY_TOKEN")
	}

	noSocket := static
	noSocket.SocketPath = ""
	if err := noSocket.validate(); err == nil {
		t.Fatal("static mode accepted an empty socket path")
	}
}

// validate(): the instance list must be canonical lowercase UUIDs
// (gate.IsCanonicalUUID — the one grammar contract) with no duplicates.
func TestStaticModeValidateInstanceGrammar(t *testing.T) {
	base := Config{
		SocketPath:        "/tmp/core.sock",
		DiscoveryInterval: time.Second, ReconnectMin: time.Second,
		ReconnectMax: time.Second, StaticAgent: "agent-static",
	}
	for name, list := range map[string][]string{
		"uppercase":  {"0190A1B2-C3D4-7E5F-8A6B-000000000001"},
		"not a uuid": {"omoikane-talk"},
		"duplicate":  {testInstanceA, testInstanceA},
	} {
		cfg := base
		cfg.StaticInstances = list
		if err := cfg.validate(); err == nil {
			t.Errorf("%s: validate accepted %v", name, list)
		}
	}
}

// FromEnv: GATE_STATIC_INSTANCES splits on commas (whitespace-tolerant)
// and passes validation without the KB vars; a malformed id is refused.
func TestFromEnvStaticInstances(t *testing.T) {
	t.Setenv(EnvSocket, "/tmp/core.sock")
	t.Setenv(EnvKBBaseURL, "")
	t.Setenv(EnvToken, "")
	t.Setenv(EnvStaticInstances, testInstanceA+" , "+testInstanceB)
	t.Setenv(EnvStaticAgent, "agent-static")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if len(cfg.StaticInstances) != 2 ||
		cfg.StaticInstances[0] != testInstanceA || cfg.StaticInstances[1] != testInstanceB {
		t.Fatalf("StaticInstances = %v", cfg.StaticInstances)
	}

	t.Setenv(EnvStaticInstances, "not-a-uuid")
	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv accepted a malformed static instance id")
	}
}
