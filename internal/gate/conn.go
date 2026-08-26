// Conn: the V3 connection state machine (DESIGN-EXTGATE-V3.md §4) from
// the gateway seat. One Conn is one gate instance connection:
//
//	PRE_HELLO --hello ok--> RUNNING --fatal | socket close--> CLOSED
//
// Hello success means RUNNING immediately: there is no ready message
// and no multi-stage readiness. The core then binds every open binding
// of the instance; the only readiness state is the core's per-binding
// acknowledged set, which this side observes as OnBind callbacks it
// acks.
//
// Incoming violations from the gateway seat: framing violations
// (UTF-8/JSON/duplicate member/oversize) close the socket; a response
// whose id was never issued, whose m is missing/unknown, or whose shape
// does not match the pending request's kind closes the socket
// (response_invalid); an unknown core message m is dropped and the
// connection kept (write 0); an invalid activity frame is dropped. A
// response whose id was abandoned by local context cancellation is
// ignored.
package gate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
)

// Handler receives core→gate traffic. It is registered with Start;
// callbacks run one at a time on a dedicated dispatch goroutine (never
// on the read loop, so a callback may issue Conn requests without
// deadlocking).
type Handler interface {
	// OnBind provisions one binding. A nil return acks the bind; an
	// error answers err(code="bind_failed") — the core then closes the
	// connection (§3.3).
	OnBind(Binding) error
	// OnSay performs one outbound delivery and reports the outcome.
	// Returning the zero SayResult (unknown outcome) closes the socket
	// without answering, per the spec's no-fabrication rule (§3.4).
	OnSay(Say) SayResult
	// OnActivity observes a display-only activity notification
	// (started/ended). Best-effort; no response exists for it.
	OnActivity(Activity)
}

type connState int

const (
	stateRunning connState = iota
	stateClosed
)

type pendingKind int

const (
	pendingHello pendingKind = iota
	pendingSaid
)

type pendingResp struct {
	seq *int64
	err *WireError
}

type pendingReq struct {
	kind pendingKind
	ch   chan pendingResp
}

// Conn is one V3 connection to the core. All methods are safe for
// concurrent use.
type Conn struct {
	rwc io.ReadWriteCloser
	fw  *frameWriter
	fr  *frameReader

	mu      sync.Mutex
	state   connState
	handler Handler
	pending map[string]pendingReq
	// abandoned tombstones request ids the local caller gave up on
	// (context cancellation): a late core response to one is ignored,
	// while a response to a never-issued id closes the connection.
	abandoned map[string]struct{}
	closeErr  error

	reqID      uint64 // guarded by mu; monotonic
	dq         *dispatchQueue
	started    chan struct{} // closed by Start; gates the dispatch loop
	startOnce  sync.Once
	startedSet bool
	closed     chan struct{}
	closeOnce  sync.Once
}

// Dial connects to the core's Unix socket at socketPath and performs
// the hello exchange, returning in RUNNING state. Call Start next to
// begin handler dispatch (core binds queue until then).
func Dial(ctx context.Context, socketPath string, p HelloParams) (*Conn, error) {
	var d net.Dialer
	nc, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("gate: dial %s: %w", socketPath, err)
	}
	c, err := NewConn(ctx, nc, p)
	if err != nil {
		nc.Close()
		return nil, err
	}
	return c, nil
}

// NewConn performs the hello exchange over an existing byte stream. It
// is the seam Dial uses and tests reach with net.Pipe. On error the
// stream is closed. On success the connection is RUNNING; core frames
// arriving before Start are queued in arrival order.
func NewConn(ctx context.Context, rwc io.ReadWriteCloser, p HelloParams) (*Conn, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	c := &Conn{
		rwc:       rwc,
		fw:        &frameWriter{w: rwc},
		fr:        newFrameReader(rwc),
		pending:   make(map[string]pendingReq),
		abandoned: make(map[string]struct{}),
		dq:        newDispatchQueue(),
		started:   make(chan struct{}),
		closed:    make(chan struct{}),
	}
	go c.readLoop()
	go c.dispatchLoop()

	id := c.nextRequestID()
	resp, err := c.request(ctx, id, pendingHello, helloFrame{
		ID:           id,
		M:            "hello",
		Protocol:     2,
		InstanceID:   p.InstanceID,
		Revision:     p.Revision,
		ConfigDigest: p.ConfigDigest,
	})
	if err != nil {
		c.closeWith(fmt.Errorf("gate: hello failed: %w", err))
		return nil, err
	}
	if resp.err != nil {
		c.closeWith(fmt.Errorf("gate: hello refused: %w", resp.err))
		return nil, resp.err
	}
	return c, nil
}

// Start registers the handler and begins dispatching queued core
// frames. It must be called exactly once, with a non-nil handler.
func (c *Conn) Start(h Handler) error {
	if h == nil {
		return errors.New("gate: handler must not be nil")
	}
	c.mu.Lock()
	if c.state == stateClosed {
		err := c.closedErrLocked()
		c.mu.Unlock()
		return err
	}
	if c.startedSet {
		c.mu.Unlock()
		return errors.New("gate: Start is only legal once")
	}
	c.handler = h
	c.startedSet = true
	c.mu.Unlock()
	c.startOnce.Do(func() { close(c.started) })
	return nil
}

// SendSaid submits one external utterance. When the core records (or
// has already recorded) it, recorded is true and seq is the
// core-assigned sequence; a re-sent origin returns the first delivery's
// seq (§3.5), so callers can use seq equality for idempotency checks.
// seq null in the ok means the core discarded the said without
// recording it: recorded is false and seq is 0 — that outcome is NOT a
// transport error, err is nil and the connection stays open. Grammar
// violations are refused locally before any bytes are written.
func (c *Conn) SendSaid(ctx context.Context, s Said) (seq int64, recorded bool, err error) {
	if err := validateSaid(&s); err != nil {
		return 0, false, err
	}
	attachments := s.Attachments
	if attachments == nil {
		attachments = []Attachment{}
	}
	id := c.nextRequestID()
	resp, err := c.request(ctx, id, pendingSaid, saidFrame{
		ID:          id,
		M:           "said",
		BindingID:   s.BindingID,
		Origin:      s.Origin,
		AuthorID:    s.AuthorID,
		Text:        s.Text,
		Attachments: attachments,
	})
	if err != nil {
		return 0, false, err
	}
	if resp.err != nil {
		return 0, false, resp.err
	}
	if resp.seq == nil {
		return 0, false, nil // core discarded the said without recording it
	}
	return *resp.seq, true, nil
}

// Closed is closed when the connection dies for any reason.
func (c *Conn) Closed() <-chan struct{} { return c.closed }

// Err returns the reason the connection closed, or nil while it lives.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != stateClosed {
		return nil
	}
	return c.closeErr
}

// Close tears the connection down locally.
func (c *Conn) Close() error {
	c.closeWith(ErrClosed)
	return nil
}

// ---- internals ------------------------------------------------------

func (c *Conn) closedErrLocked() error {
	if c.closeErr != nil && c.closeErr != ErrClosed {
		return fmt.Errorf("%w: %w", ErrClosed, c.closeErr)
	}
	return ErrClosed
}

// nextRequestID returns a monotonic request id (decimal string, always
// within the 1..128-byte grammar).
func (c *Conn) nextRequestID() string {
	c.mu.Lock()
	c.reqID++
	id := c.reqID
	c.mu.Unlock()
	return strconv.FormatUint(id, 10)
}

// request registers id, writes frame, and waits for the correlated
// response. Context cancellation abandons the wait and tombstones the
// id so the read loop ignores the core's late response instead of
// treating it as never-issued.
func (c *Conn) request(ctx context.Context, id string, kind pendingKind, frame any) (pendingResp, error) {
	ch := make(chan pendingResp, 1)
	c.mu.Lock()
	if c.state == stateClosed {
		err := c.closedErrLocked()
		c.mu.Unlock()
		return pendingResp{}, err
	}
	c.pending[id] = pendingReq{kind: kind, ch: ch}
	c.mu.Unlock()

	if err := c.fw.writeFrame(frame); err != nil {
		c.forget(id)
		c.closeWith(err)
		return pendingResp{}, err
	}
	select {
	case r := <-ch:
		return r, nil
	case <-ctx.Done():
		c.abandon(id)
		return pendingResp{}, ctx.Err()
	case <-c.closed:
		c.mu.Lock()
		err := c.closedErrLocked()
		c.mu.Unlock()
		return pendingResp{}, err
	}
}

// forget drops a pending id whose frame never reached the wire (write
// failure); no response can arrive, so no tombstone is needed.
func (c *Conn) forget(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// abandon drops a pending id the caller gave up on after the frame was
// written, tombstoning it for the read loop (see the abandoned field).
func (c *Conn) abandon(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.abandoned[id] = struct{}{}
	c.mu.Unlock()
}

// closeWith closes the connection exactly once, recording err as the
// reason, waking Closed(), all pending waiters, and the dispatch loop.
func (c *Conn) closeWith(err error) {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.state = stateClosed
		c.closeErr = err
		c.mu.Unlock()
		close(c.closed)
		c.rwc.Close()
		c.dq.close()
	})
}

// readLoop pumps frames: responses (m:"ok"|"err") resolve pending
// requests inline; core requests are validated and queued for the
// dispatch goroutine. Framing violations and malformed responses close
// the connection.
func (c *Conn) readLoop() {
	for {
		data, err := c.fr.next()
		if err != nil {
			c.closeWith(err)
			return
		}
		if err := validateFrameShape(data); err != nil {
			c.closeWith(err)
			return
		}
		var env struct {
			M *string `json:"m"`
		}
		if err := json.Unmarshal(data, &env); err != nil {
			c.closeWith(err)
			return
		}
		if env.M == nil {
			c.closeWith(errors.New("gate: frame without m member"))
			return
		}
		switch *env.M {
		case "ok", "err":
			if err := c.handleResponse(*env.M, data); err != nil {
				c.closeWith(err)
				return
			}
		default:
			if err := c.handleCoreMessage(*env.M, data); err != nil {
				c.closeWith(err)
				return
			}
		}
	}
}

// handleResponse resolves one ok/err response frame against its pending
// request, checking the shape against the pending request's kind: a
// said ok must carry the seq member (value may be null), a hello ok
// must not be an err… any mismatch, unknown id, or consumed id is a
// core protocol violation and closes the connection (response_invalid).
// An id the client abandoned (local context cancellation) is ignored.
func (c *Conn) handleResponse(m string, data []byte) error {
	var rf struct {
		ID     *string         `json:"id"`
		Seq    json.RawMessage `json:"seq"`
		Code   *string         `json:"code"`
		Detail *string         `json:"detail"`
	}
	members, err := decodeFrame(data, &rf)
	if err != nil {
		return err
	}
	if rf.ID == nil {
		return errors.New("gate: response frame without id")
	}
	if err := validateRequestID(*rf.ID); err != nil {
		return err
	}

	c.mu.Lock()
	req, found := c.pending[*rf.ID]
	if found {
		delete(c.pending, *rf.ID)
	}
	_, wasAbandoned := c.abandoned[*rf.ID]
	if !found && wasAbandoned {
		delete(c.abandoned, *rf.ID)
	}
	c.mu.Unlock()
	if !found {
		if wasAbandoned {
			return nil // late response to a locally abandoned request
		}
		return fmt.Errorf("gate: response to never-issued request id %q (response_invalid)", *rf.ID)
	}

	var resp pendingResp
	if m == "err" {
		if rf.Code == nil || *rf.Code == "" {
			return errors.New("gate: err response without a nonempty code (response_invalid)")
		}
		resp.err = &WireError{Code: *rf.Code, Detail: rf.Detail}
		req.ch <- resp
		return nil
	}
	// m == "ok": shape per pending kind (§3.3 — hello ok is {id,m},
	// said ok is {id,m,seq} with seq positive-i64|null).
	if req.kind == pendingSaid {
		seqRaw, present := members["seq"]
		if !present {
			return errors.New("gate: said ok without the seq member (response_invalid)")
		}
		if string(seqRaw) != "null" {
			var seq int64
			if err := json.Unmarshal(seqRaw, &seq); err != nil || seq <= 0 {
				return fmt.Errorf("gate: said ok seq %s is not a positive integer or null (response_invalid)", seqRaw)
			}
			resp.seq = &seq
		}
	}
	req.ch <- resp
	return nil
}

// handleCoreMessage and the dispatch queue live in conn_dispatch.go.
