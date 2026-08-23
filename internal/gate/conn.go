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
// malformed core frames close the socket; activity violations drop the
// frame and keep the connection; a response whose id matches no pending
// request is ignored.
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

	mu       sync.Mutex
	state    connState
	epoch    uint64
	handler  Handler
	pending  map[string]chan pendingResp
	closeErr error

	reqID      uint64 // guarded by mu; monotonic
	dispatchCh chan func()
	closed     chan struct{}
	closeOnce  sync.Once
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
		rwc:        rwc,
		fw:         &frameWriter{w: rwc},
		fr:         newFrameReader(rwc),
		pending:    make(map[string]chan pendingResp),
		dispatchCh: make(chan func(), 64),
		closed:     make(chan struct{}),
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
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		if c.state == stateReadySent {
			c.state = stateSynchronizing
		}
		return err
	}
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

// SendEvent submits one inbound event and returns the core-assigned
// seq. When the core reports seq null (already-known origin), seq is 0.
// Grammar violations are refused locally before any bytes are written,
// and calls before Ready fail with ErrNotReady.
func (c *Conn) SendEvent(ctx context.Context, ev Event) (int64, error) {
	if err := c.requireReady(); err != nil {
		return 0, err
	}
	if err := validateEvent(&ev); err != nil {
		return 0, err
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
		return 0, err
	}
	var ok eventOK
	if err := c.decodeResponse(okRaw, &ok); err != nil {
		return 0, err
	}
	if ok.Seq == nil {
		return 0, nil
	}
	return *ok.Seq, nil
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
// response. Context cancellation abandons the wait; a late response to
// an abandoned id is then an unknown id and is ignored by the read
// loop.
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
		c.forget(id)
		return nil, ctx.Err()
	case <-c.closed:
		c.mu.Lock()
		err := c.closedErrLocked()
		c.mu.Unlock()
		return nil, err
	}
}

func (c *Conn) forget(id string) {
	c.mu.Lock()
	delete(c.pending, id)
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
// reason, waking Closed() and all pending waiters.
func (c *Conn) closeWith(err error) {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.state = stateClosed
		c.closeErr = err
		c.mu.Unlock()
		close(c.closed)
		c.rwc.Close()
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

// handleResponse resolves one {id,ok} xor {id,err} frame. An id with no
// pending request is ignored (a late response after local context
// cancellation); a malformed response union closes the connection.
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
	}
	c.mu.Unlock()
	if found {
		ch <- resp
	}
	return nil
}

// handleCoreMessage validates one core→gate request/notification and
// queues its handler dispatch. Returned errors close the connection.
func (c *Conn) handleCoreMessage(m string, data []byte) error {
	c.mu.Lock()
	h := c.handler
	c.mu.Unlock()
	switch m {
	case "bind", "unbind":
		var f bindFrame
		if err := decodeStrictBody(data, &f); err != nil {
			return err
		}
		if err := validateBind(&f); err != nil {
			return err
		}
		if h == nil {
			return fmt.Errorf("gate: %s before a handler was registered", m)
		}
		unbind := m == "unbind"
		c.enqueue(func() { c.dispatchBind(h, f, unbind) })
	case "catch_up":
		var f catchUpFrame
		if err := decodeStrictBody(data, &f); err != nil {
			return err
		}
		if err := validateCatchUp(&f); err != nil {
			return err
		}
		if h == nil {
			return errors.New("gate: catch_up before a handler was registered")
		}
		c.enqueue(func() { c.dispatchCatchUp(h, f) })
	case "effect":
		var f effectFrame
		if err := decodeStrictBody(data, &f); err != nil {
			return err
		}
		if err := validateEffect(&f); err != nil {
			return err
		}
		if h == nil {
			return errors.New("gate: effect before a handler was registered")
		}
		c.enqueue(func() { c.dispatchEffect(h, f) })
	case "activity":
		var f activityFrame
		if err := decodeStrictBody(data, &f); err != nil {
			return nil // violation: drop, keep connection
		}
		if err := validateActivity(&f); err != nil {
			return nil // violation: drop, keep connection
		}
		if h == nil {
			return nil // pre-handler activity: nothing to notify, drop
		}
		a := Activity{Address: f.Address, ActivityID: f.ActivityID, State: f.State}
		if f.Kind != nil {
			a.Kind = *f.Kind
		}
		if f.Label != nil {
			a.Label = *f.Label
		}
		c.enqueue(func() { h.OnActivity(a) })
	default:
		return fmt.Errorf("gate: unknown core message %q", m)
	}
	return nil
}

// dispatchLoop runs handler callbacks strictly one at a time, in
// arrival order — this is what serializes bind acks.
func (c *Conn) dispatchLoop() {
	for {
		select {
		case f := <-c.dispatchCh:
			f()
		case <-c.closed:
			return
		}
	}
}

func (c *Conn) enqueue(f func()) {
	select {
	case c.dispatchCh <- f:
	case <-c.closed:
	}
}

func (c *Conn) dispatchBind(h Handler, f bindFrame, unbind bool) {
	b := Binding{BindingID: f.BindingID, Address: f.Address}
	var err error
	if unbind {
		err = h.OnUnbind(b)
	} else {
		err = h.OnBind(b)
	}
	if err != nil {
		detail := err.Error()
		c.respondErr(f.ID, WireError{Code: "bind_failed", Detail: &detail})
		return
	}
	c.respondOk(f.ID, emptyOK{})
}

func (c *Conn) dispatchCatchUp(h Handler, f catchUpFrame) {
	cu := CatchUp{BindingID: f.BindingID, Address: f.Address, Mode: f.Start.Mode}
	if f.Start.CursorB64 != nil {
		cu.CursorB64 = *f.Start.CursorB64
	}
	if f.Start.CursorDigest != nil {
		cu.CursorDigest = *f.Start.CursorDigest
	}
	if err := h.OnCatchUp(cu); err != nil {
		detail := err.Error()
		c.respondErr(f.ID, WireError{Code: "catch_up_failed", Detail: &detail})
		return
	}
	c.respondOk(f.ID, emptyOK{})
}

func (c *Conn) dispatchEffect(h Handler, f effectFrame) {
	res := h.OnEffect(Effect{
		ID: f.ID, BindingID: f.BindingID, Address: f.Address, Kind: f.Kind, Payload: f.Payload,
	})
	switch res.kind {
	case effectDelivered:
		if res.origin == "" {
			// delivered:true with an empty origin is a protocol error
			// on the wire; refusing to fabricate a valid answer, close
			// without answering (core records disconnect).
			c.closeWith(errors.New("gate: effect delivered with empty origin"))
			return
		}
		c.respondOk(f.ID, effectDeliveredOK{Delivered: true, Origin: res.origin})
	case effectRejected:
		c.respondOk(f.ID, effectRejectedOK{Delivered: false})
	case effectWireErr:
		if res.wireErr.Code == "" {
			c.closeWith(errors.New("gate: effect err with empty code"))
			return
		}
		c.respondErr(f.ID, res.wireErr)
	default:
		// Unknown outcome: never fabricate delivered/rejected/err —
		// close the socket without answering (spec §7).
		c.closeWith(errors.New("gate: effect outcome unknown, closing without answering"))
	}
}

func (c *Conn) respondOk(id string, ok any) {
	if err := c.fw.writeFrame(okResponseFrame{ID: id, Ok: ok}); err != nil {
		c.closeWith(err)
	}
}

func (c *Conn) respondErr(id string, we WireError) {
	if err := c.fw.writeFrame(errResponseFrame{ID: id, Err: we}); err != nil {
		c.closeWith(err)
	}
}
