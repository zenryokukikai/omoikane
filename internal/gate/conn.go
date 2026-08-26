// Conn: the protocol 2 connection state machine (external-gate.md §6)
// on the gateway side. One Conn is one gate instance connection:
//
//	CONNECTED --hello ok--> SYNCHRONIZING --ready ok--> BINDING
//	BINDING --all bind acks (binding_id byte order)--> ACTIVE
//	any --fatal | failed | socket close--> CLOSED
//
// The BINDING→ACTIVE edge is observable only by the core (the gateway
// cannot know when the last bind arrived), so locally both map to the
// single stateReady: SendEvent and friends unlock when Ready is
// acknowledged, and an early event is answered by the core with
// instance_not_ready. Bind acks are serialized: core requests are
// dispatched one at a time in arrival order, and the core sends binds
// in binding_id byte order one ack at a time (§6), so ack order equals
// binding_id byte order.
//
// Incoming violations follow the §5 table from the client's seat:
// framing violations (UTF-8/JSON/duplicate member/oversize) and
// malformed response unions close the socket. Post-ready core frames
// with unknown message/field/value violations are answered with an err
// response and the connection is KEPT (pre-ready they close); activity
// violations drop the frame and keep the connection. A response whose
// id was abandoned by local context cancellation is ignored; a response
// whose id was never issued closes the connection (response_invalid).
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

// Handler receives core→gate traffic. It must be registered with
// SetHandler before Ready; callbacks run one at a time on a dedicated
// dispatch goroutine (never on the read loop, so a callback may issue
// Conn requests without deadlocking).
type Handler interface {
	// OnBind provisions one binding. A nil return acks the bind; an
	// error answers err (the core then marks the connection failed).
	OnBind(Binding) error
	// OnUnbind releases one binding; same response contract as OnBind.
	OnUnbind(Binding) error
	// OnEffect performs one outbound effect and reports the outcome.
	// Returning the zero EffectResult (unknown outcome) closes the
	// socket without answering, per the spec's no-fabrication rule.
	OnEffect(Effect) EffectResult
	// OnActivity observes a fire-and-forget activity notification.
	OnActivity(Activity)
	// OnCatchUp surfaces a catch_up instruction. A nil return acks it;
	// an error answers err. Checkpointing is a separate request the
	// handler issues later via SourceCheckpoint.
	OnCatchUp(CatchUp) error
}

type connState int

const (
	stateHello connState = iota // hello sent, awaiting ok
	stateSynchronizing
	stateReadySent
	stateReady // BINDING/ACTIVE from the core's viewpoint
	stateClosed
)

type pendingResp struct {
	ok  json.RawMessage
	err *WireError
}

// Conn is one protocol 2 connection to the core. All methods are safe
// for concurrent use.
type Conn struct {
	rwc io.ReadWriteCloser
	fw  *frameWriter
	fr  *frameReader

	mu      sync.Mutex
	state   connState
	epoch   uint64
	handler Handler
	pending map[string]chan pendingResp
	// abandoned tombstones request ids the local caller gave up on
	// (context cancellation): a late core response to one is ignored,
	// while a response to a never-issued id closes the connection.
	// Growth bound: one entry per abandoned request, removed when the
	// matching late response arrives.
	abandoned map[string]struct{}
	closeErr  error

	reqID     uint64 // guarded by mu; monotonic
	dq        *dispatchQueue
	closed    chan struct{}
	closeOnce sync.Once
}

// Dial connects to the core's Unix socket at socketPath and performs
// the hello exchange, returning after the core acknowledged it (state
// SYNCHRONIZING). Register a Handler and call Ready next.
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
// stream is closed.
func NewConn(ctx context.Context, rwc io.ReadWriteCloser, p HelloParams) (*Conn, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	c := &Conn{
		rwc:       rwc,
		fw:        &frameWriter{w: rwc},
		fr:        newFrameReader(rwc),
		pending:   make(map[string]chan pendingResp),
		abandoned: make(map[string]struct{}),
		dq:        newDispatchQueue(),
		closed:    make(chan struct{}),
	}
	go c.readLoop()
	go c.dispatchLoop()

	id := c.nextRequestID()
	okRaw, err := c.request(ctx, id, helloFrame{
		ID:               id,
		M:                "hello",
		Protocol:         2,
		KindID:           p.KindID,
		InstanceID:       p.InstanceID,
		Revision:         p.Revision,
		ConfigDigest:     p.ConfigDigest,
		OriginScope:      p.OriginScope,
		AddressForm:      p.AddressForm,
		IngressDiscovery: "prebound",
		Effects:          []string{"say"},
		Capabilities:     []string{"open"},
	})
	if err != nil {
		c.closeWith(fmt.Errorf("gate: hello failed: %w", err))
		return nil, err
	}
	var hok helloOK
	if err := decodeStrictBody(okRaw, &hok); err != nil {
		c.closeWith(err)
		return nil, err
	}
	if hok.Protocol != 2 {
		err := fmt.Errorf("gate: hello ok protocol %d, want 2", hok.Protocol)
		c.closeWith(err)
		return nil, err
	}
	c.mu.Lock()
	c.epoch = hok.ConnectionEpoch
	c.state = stateSynchronizing
	c.mu.Unlock()
	return c, nil
}

// SetHandler registers the core-traffic handler. It must run before
// Ready and cannot change afterwards.
func (c *Conn) SetHandler(h Handler) error {
	if h == nil {
		return errors.New("gate: handler must not be nil")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == stateReadySent || c.state == stateReady {
		return errors.New("gate: handler must be registered before Ready")
	}
	if c.state == stateClosed {
		return c.closedErrLocked()
	}
	c.handler = h
	return nil
}

// Ready sends ready and returns after the core acknowledged it. The
// core then starts sending binds; a Handler must already be registered.
func (c *Conn) Ready(ctx context.Context) error {
	c.mu.Lock()
	if c.state == stateClosed {
		err := c.closedErrLocked()
		c.mu.Unlock()
		return err
	}
	if c.handler == nil {
		c.mu.Unlock()
		return errors.New("gate: Ready requires a registered Handler")
	}
	if c.state != stateSynchronizing {
		c.mu.Unlock()
		return errors.New("gate: Ready is only legal once, after hello")
	}
	c.state = stateReadySent
	epoch := c.epoch
	c.mu.Unlock()

	id := c.nextRequestID()
	_, err := c.request(ctx, id, readyFrame{ID: id, M: "ready", ConnectionEpoch: epoch})
	if err != nil {
		// request only fails after the ready frame was written (or the
		// connection already died): rolling back to synchronizing would
		// re-legalize SetHandler and permit a second ready frame while
		// the first is already on the wire, so a failed Ready is
		// terminal — close the connection. Errors before the write
		// (nil handler, wrong state) return above and stay retryable.
		c.closeWith(fmt.Errorf("gate: ready abandoned after send: %w", err))
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == stateReadySent {
		c.state = stateReady
	}
	return nil
}

// Failed reports a fatal gateway-side startup failure to the core
// (message `failed`) and closes the connection. code must be nonempty.
func (c *Conn) Failed(ctx context.Context, code string) error {
	if code == "" {
		return errors.New("gate: failed code must be nonempty")
	}
	c.mu.Lock()
	if c.state == stateClosed {
		err := c.closedErrLocked()
		c.mu.Unlock()
		return err
	}
	if c.state == stateHello {
		c.mu.Unlock()
		return errors.New("gate: failed is only legal after hello")
	}
	epoch := c.epoch
	c.mu.Unlock()

	id := c.nextRequestID()
	_, err := c.request(ctx, id, failedFrame{ID: id, M: "failed", ConnectionEpoch: epoch, Code: code})
	c.closeWith(fmt.Errorf("gate: gateway reported failed(%s)", code))
	return err
}

// SendEvent submits one inbound event. When the core records (or has
// already recorded) the event, recorded is true and seq is the
// core-assigned sequence; the same origin always maps to the same seq,
// so a re-sent origin returns the seq of the first delivery and callers
// can use seq equality for idempotency checks. seq null in the ok
// payload means the core REJECTED/discarded the event without recording
// it: recorded is false and seq is 0. That outcome is NOT a transport
// error and NOT a duplicate — err is nil, the connection stays open,
// and the caller decides what a discarded event means. Grammar
// violations are refused locally before any bytes are written, and
// calls before Ready fail with ErrNotReady. The core must echo the
// request's binding_id in the ok payload; a mismatch is a core protocol
// violation and closes the connection (response_invalid).
func (c *Conn) SendEvent(ctx context.Context, ev Event) (seq int64, recorded bool, err error) {
	if err := c.requireReady(); err != nil {
		return 0, false, err
	}
	if err := validateEvent(&ev); err != nil {
		return 0, false, err
	}
	id := c.nextRequestID()
	okRaw, err := c.request(ctx, id, eventFrame{
		ID:          id,
		M:           "event",
		Kind:        ev.Kind,
		Address:     ev.Address,
		BindingID:   ev.BindingID,
		Author:      ev.Author,
		Content:     ev.Content,
		Mentions:    ev.Mentions,
		ReplyTo:     ev.ReplyTo,
		Origin:      ev.Origin,
		Target:      ev.Target,
		Symbol:      ev.Symbol,
		Removed:     ev.Removed,
		Action:      ev.Action,
		Attachments: ev.Attachments,
	})
	if err != nil {
		return 0, false, err
	}
	var ok eventOK
	if err := c.decodeResponse(okRaw, &ok); err != nil {
		return 0, false, err
	}
	if ok.BindingID != ev.BindingID {
		err := fmt.Errorf("gate: event ok binding_id %q does not echo request binding_id %q (response_invalid)",
			ok.BindingID, ev.BindingID)
		c.closeWith(err)
		return 0, false, err
	}
	if ok.Seq == nil {
		return 0, false, nil // core discarded the event without recording it
	}
	return *ok.Seq, true, nil
}

// SourceCheckpoint persists a source cursor after a definitively acked
// catch-up page. expectedDigest nil means "no cursor stored yet" (CAS
// from empty); otherwise it must be the 64-hex digest last returned.
func (c *Conn) SourceCheckpoint(ctx context.Context, bindingID string, expectedDigest *string, cursorB64 string) (digest string, updatedAt int64, err error) {
	if err := c.requireReady(); err != nil {
		return "", 0, err
	}
	if !isCanonicalUUID(bindingID) {
		return "", 0, errors.New("gate: source_checkpoint binding_id must be a canonical lowercase UUID")
	}
	if expectedDigest != nil && !isLowerHexDigest(*expectedDigest) {
		return "", 0, errors.New("gate: source_checkpoint expected_cursor_digest must be 64 lowercase hex")
	}
	if !isStdPaddedBase64(cursorB64) {
		return "", 0, errors.New("gate: source_checkpoint cursor_b64 must be standard padded base64")
	}
	id := c.nextRequestID()
	okRaw, err := c.request(ctx, id, sourceCheckpointFrame{
		ID:                   id,
		M:                    "source_checkpoint",
		BindingID:            bindingID,
		ExpectedCursorDigest: expectedDigest,
		CursorB64:            cursorB64,
	})
	if err != nil {
		return "", 0, err
	}
	var ok sourceCheckpointOK
	if err := c.decodeResponse(okRaw, &ok); err != nil {
		return "", 0, err
	}
	return ok.CursorDigest, ok.UpdatedAt, nil
}

// PlaceClosed reports an authoritative external closure of a place.
// reason must be one of deleted, archived, left, unavailable; closure
// must never be inferred from disconnects or fetch failures (spec §7).
func (c *Conn) PlaceClosed(ctx context.Context, bindingID, address, reason string) error {
	if err := c.requireReady(); err != nil {
		return err
	}
	if !isCanonicalUUID(bindingID) {
		return errors.New("gate: place_closed binding_id must be a canonical lowercase UUID")
	}
	if address == "" {
		return errors.New("gate: place_closed address must be nonempty")
	}
	switch reason {
	case "deleted", "archived", "left", "unavailable":
	default:
		return fmt.Errorf("gate: place_closed reason %q unknown", reason)
	}
	id := c.nextRequestID()
	okRaw, err := c.request(ctx, id, placeClosedFrame{
		ID: id, M: "place_closed", BindingID: bindingID, Address: address, Reason: reason,
	})
	if err != nil {
		return err
	}
	var ok placeClosedOK
	if err := c.decodeResponse(okRaw, &ok); err != nil {
		return err
	}
	if !ok.Closed {
		err := errors.New("gate: place_closed ok reported closed:false")
		c.closeWith(err)
		return err
	}
	return nil
}

// ReadEvents fetches recent place history via the read request.
func (c *Conn) ReadEvents(ctx context.Context, p ReadParams) (ReadResult, error) {
	if err := c.requireReady(); err != nil {
		return ReadResult{}, err
	}
	if p.Address == "" {
		return ReadResult{}, errors.New("gate: read address must be nonempty")
	}
	if p.From < 0 {
		return ReadResult{}, errors.New("gate: read from must be positive")
	}
	if p.Limit > 1000 {
		return ReadResult{}, errors.New("gate: read limit must be within 1..1000")
	}
	f := readFrame{ID: c.nextRequestID(), M: "read", Address: p.Address}
	if p.From > 0 {
		f.From = &p.From
	}
	if p.Limit > 0 {
		f.Limit = &p.Limit
	}
	okRaw, err := c.request(ctx, f.ID, f)
	if err != nil {
		return ReadResult{}, err
	}
	var ok readOK
	if err := c.decodeResponse(okRaw, &ok); err != nil {
		return ReadResult{}, err
	}
	return ReadResult{Events: ok.Events, Next: ok.Next}, nil
}

// Epoch returns the connection_epoch assigned by the hello ok.
func (c *Conn) Epoch() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.epoch
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

func (c *Conn) requireReady() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.state {
	case stateReady:
		return nil
	case stateClosed:
		return c.closedErrLocked()
	default:
		return ErrNotReady
	}
}

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
func (c *Conn) request(ctx context.Context, id string, frame any) (json.RawMessage, error) {
	ch := make(chan pendingResp, 1)
	c.mu.Lock()
	if c.state == stateClosed {
		err := c.closedErrLocked()
		c.mu.Unlock()
		return nil, err
	}
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.fw.writeFrame(frame); err != nil {
		c.forget(id)
		c.closeWith(err)
		return nil, err
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		return r.ok, nil
	case <-ctx.Done():
		c.abandon(id)
		return nil, ctx.Err()
	case <-c.closed:
		c.mu.Lock()
		err := c.closedErrLocked()
		c.mu.Unlock()
		return nil, err
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

// decodeResponse strict-decodes a response ok payload; a payload that
// does not match the spec shape is a malformed frame and closes the
// connection.
func (c *Conn) decodeResponse(okRaw json.RawMessage, v any) error {
	if err := decodeStrictBody(okRaw, v); err != nil {
		c.closeWith(err)
		return err
	}
	return nil
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

// readLoop pumps frames: responses resolve pending requests inline;
// core requests are validated and queued for the dispatch goroutine.
// Any framing violation or malformed core frame (activity excepted)
// closes the connection.
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
			if err := c.handleResponse(data); err != nil {
				c.closeWith(err)
				return
			}
			continue
		}
		if err := c.handleCoreMessage(*env.M, data); err != nil {
			c.closeWith(err)
			return
		}
	}
}

// handleResponse resolves one {id,ok} xor {id,err} frame. An id the
// client abandoned (local context cancellation) is ignored; an id that
// was never issued is a core protocol violation and closes the
// connection (violation table: response_invalid). A malformed response
// union also closes the connection.
func (c *Conn) handleResponse(data []byte) error {
	var rf struct {
		ID  *string         `json:"id"`
		Ok  json.RawMessage `json:"ok"`
		Err json.RawMessage `json:"err"`
	}
	if err := decodeStrictBody(data, &rf); err != nil {
		return err
	}
	if rf.ID == nil {
		return errors.New("gate: response frame without id")
	}
	if err := validateRequestID(*rf.ID); err != nil {
		return err
	}
	if (rf.Ok == nil) == (rf.Err == nil) {
		return errors.New("gate: response frame must carry exactly one of ok/err")
	}
	resp := pendingResp{ok: rf.Ok}
	if rf.Err != nil {
		var we WireError
		if err := decodeStrictBody(rf.Err, &we); err != nil {
			return err
		}
		if we.Code == "" {
			return errors.New("gate: response err.code must be nonempty")
		}
		resp.err = &we
	}
	c.mu.Lock()
	ch, found := c.pending[*rf.ID]
	if found {
		delete(c.pending, *rf.ID)
		c.mu.Unlock()
		ch <- resp
		return nil
	}
	if _, wasAbandoned := c.abandoned[*rf.ID]; wasAbandoned {
		delete(c.abandoned, *rf.ID)
		c.mu.Unlock()
		return nil // late response to a locally abandoned request
	}
	c.mu.Unlock()
	return fmt.Errorf("gate: response to never-issued request id %q (response_invalid)", *rf.ID)
}

// handleCoreMessage and the dispatch queue live in conn_dispatch.go.
