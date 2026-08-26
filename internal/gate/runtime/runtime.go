package runtime

// Runtime: the orchestrator. Owns the roster of instance runners
// (discovery: GET /v1/gateway/librarians, re-polled so newly
// provisioned librarians connect without a restart) and the single SSE
// subscription that fans human chat.message events out to the right
// instance connection as said messages.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/zenryokukikai/omoikane/internal/gate"
)

// DialFunc opens the byte stream to the core socket.
type DialFunc func(ctx context.Context, socketPath string) (io.ReadWriteCloser, error)

// unixDial is the production DialFunc.
func unixDial(ctx context.Context, socketPath string) (io.ReadWriteCloser, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", socketPath)
}

// Runtime holds the process state of omoikane-gate.
type Runtime struct {
	cfg     Config
	kb      KB // nil in static conformance mode (no KB exists)
	cursors CursorStore
	log     *slog.Logger
	// static marks wire-conformance static mode
	// (Config.StaticInstances): fixed instance set, no KB, no roster
	// polling, no SSE inbound, no replay; every say answers
	// external_rejected. See Config.StaticInstances.
	static bool

	mu      sync.Mutex
	runners map[string]*instanceRunner // gate_instance_id → runner

	cursorGapOnce sync.Once
}

// New validates cfg and builds a Runtime. logger nil = slog.Default().
func New(cfg Config, logger *slog.Logger) (*Runtime, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	static := len(cfg.StaticInstances) > 0
	if cfg.Dial == nil {
		cfg.Dial = unixDial
	}
	if cfg.KB == nil && !static {
		cfg.KB = newHTTPKB(cfg.KBBaseURL, cfg.Token)
	}
	if cfg.Cursors == nil {
		// The real httpKB doubles as the CursorStore (GET/PUT
		// /v1/gateway/threads/{id}/cursor). A fake KB that does not
		// implement it degrades to the no-op store.
		if cs, ok := cfg.KB.(CursorStore); ok {
			cfg.Cursors = cs
		} else {
			cfg.Cursors = noCursorStore{}
		}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Runtime{
		cfg: cfg, kb: cfg.KB, cursors: cfg.Cursors, log: logger,
		static:  static,
		runners: map[string]*instanceRunner{},
	}, nil
}

// Run serves until ctx is done: an immediate roster sync, the periodic
// discovery loop, and the inbound SSE loop. On return every runner is
// stopped. In static conformance mode there is no KB, so only the fixed
// instance runners exist — no roster loop, no SSE loop.
func (rt *Runtime) Run(ctx context.Context) error {
	if rt.static {
		rt.startStatic(ctx)
		<-ctx.Done()
		rt.stopAll()
		return nil
	}
	rt.syncRoster(ctx)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		rt.inboundLoop(ctx)
	}()

	ticker := time.NewTicker(rt.cfg.DiscoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			rt.stopAll()
			wg.Wait()
			return nil
		case <-ticker.C:
			rt.syncRoster(ctx)
		}
	}
}

// syncRoster reconciles the runner set against the current librarian
// roster. Only rows with a registered gate instance are connectable;
// rows that vanished (librarian deactivated / instance dropped) get
// their runner stopped. A roster fetch failure changes nothing — the
// existing runners keep serving.
func (rt *Runtime) syncRoster(ctx context.Context) {
	libs, err := rt.kb.ListLibrarians(ctx)
	if err != nil {
		rt.log.Warn("librarian roster fetch failed; keeping current set", "err", err)
		return
	}
	want := make(map[string]Librarian, len(libs))
	for _, l := range libs {
		if l.GateInstanceID != "" {
			want[l.GateInstanceID] = l
		}
	}

	var toStop []*instanceRunner
	rt.mu.Lock()
	for id, r := range rt.runners {
		if _, keep := want[id]; !keep {
			toStop = append(toStop, r)
			delete(rt.runners, id)
		}
	}
	for id, lib := range want {
		if _, exists := rt.runners[id]; exists {
			continue
		}
		r := rt.newInstanceRunner(ctx, lib)
		rt.runners[id] = r
		go r.run()
		rt.log.Info("serving gate instance",
			"instance_id", id, "user_id", lib.UserID, "agent_id", lib.AgentID)
	}
	rt.mu.Unlock()

	for _, r := range toStop {
		rt.log.Info("stopping gate instance (left roster)",
			"instance_id", r.lib.GateInstanceID, "user_id", r.lib.UserID)
		r.stop()
	}
}

// startStatic starts one runner per configured static instance id
// (validate() guarantees the ids are unique canonical UUIDs). The
// Librarian rows are synthetic — instance id only; there is no owner
// because there is no KB.
func (rt *Runtime) startStatic(ctx context.Context) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for _, id := range rt.cfg.StaticInstances {
		r := rt.newInstanceRunner(ctx, Librarian{GateInstanceID: id})
		rt.runners[id] = r
		go r.run()
		rt.log.Info("serving static gate instance (conformance mode)", "instance_id", id)
	}
}

func (rt *Runtime) stopAll() {
	rt.mu.Lock()
	rs := make([]*instanceRunner, 0, len(rt.runners))
	for id, r := range rt.runners {
		rs = append(rs, r)
		delete(rt.runners, id)
	}
	rt.mu.Unlock()
	for _, r := range rs {
		r.stop()
	}
}

// runnerForUser finds the runner serving userID's librarian.
func (rt *Runtime) runnerForUser(userID string) *instanceRunner {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for _, r := range rt.runners {
		if r.lib.UserID == userID {
			return r
		}
	}
	return nil
}

// ---- inbound (human → agent) ----------------------------------------

// chatMessageEvent is the slice of the SSE chat.message payload the
// router needs; extra members are ignored.
type chatMessageEvent struct {
	ID              string `json:"id"`
	ThreadID        string `json:"thread_id"`
	AuthorUserID    string `json:"author_user_id"`
	AuthorRole      string `json:"author_role"`
	Content         string `json:"content"`
	ThreadIntent    string `json:"thread_intent"`
	ThreadCreatedBy string `json:"thread_created_by"`
}

// inboundLoop keeps one SSE subscription alive, reconnecting with the
// same bounded backoff as the socket side. The stream is a latency
// path, not the source of truth: anything missed while resubscribing
// is covered by the on-bind replay (origin idempotency dedupes the
// overlap core-side).
func (rt *Runtime) inboundLoop(ctx context.Context) {
	backoff := rt.cfg.ReconnectMin
	for {
		if ctx.Err() != nil {
			return
		}
		ch, err := rt.kb.StreamEvents(ctx)
		if err == nil {
			rt.log.Info("event stream connected")
			backoff = rt.cfg.ReconnectMin
			for ev := range ch {
				if ev.Type == "chat.message" {
					rt.routeInbound(ctx, ev.Data)
				}
			}
			err = io.EOF
		}
		if ctx.Err() != nil {
			return
		}
		rt.log.Warn("event stream ended; reconnecting", "backoff", backoff, "err", err)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		if backoff *= 2; backoff > rt.cfg.ReconnectMax {
			backoff = rt.cfg.ReconnectMax
		}
	}
}

// routeInbound forwards one chat.message to the serving instance as a
// said, origin = the message id (idempotency key). Messages that
// are not human /talk traffic for a served librarian are ignored; a
// thread not yet bound on the live connection is dropped here and
// covered by the replay when its bind arrives.
func (rt *Runtime) routeInbound(ctx context.Context, data json.RawMessage) {
	var m chatMessageEvent
	if err := json.Unmarshal(data, &m); err != nil {
		rt.log.Warn("undecodable chat.message event", "err", err)
		return
	}
	if m.AuthorRole != "human" || m.ThreadIntent != "talk" ||
		m.ID == "" || m.ThreadID == "" || m.Content == "" {
		return
	}
	r := rt.runnerForUser(m.ThreadCreatedBy)
	if r == nil {
		return // not a served librarian's thread
	}
	conn, bindingID := r.liveBinding(m.ThreadID)
	if conn == nil || bindingID == "" {
		rt.log.Debug("inbound message for unbound thread; deferred to bind replay",
			"thread_id", m.ThreadID, "message_id", m.ID)
		return
	}
	r.sendSaid(ctx, conn, bindingID, m.ThreadID, m.ID, m.AuthorUserID, m.Content)
}

// logCursorGapOnce logs the first cursor read/advance failure once per
// process instead of once per message: replay falls back to the thread
// start (origin-deduped), so a persistent cursor problem must not spam
// the log line-for-line.
func (rt *Runtime) logCursorGapOnce(err error) {
	rt.cursorGapOnce.Do(func() {
		rt.log.Warn("thread cursor unavailable; replay runs from thread start (origin-deduped)", "err", err)
	})
}

var _ gate.Handler = (*instanceRunner)(nil)
