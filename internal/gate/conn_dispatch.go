// Core→gate frame handling and handler dispatch for Conn (split from
// conn.go by responsibility: conn.go owns the state machine and the
// gate→core request side; this file owns what the core sends us).
//
// Two rules shape this file:
//
//  1. Liveness: the read loop must NEVER block on dispatch. A handler
//     callback may itself wait on a core response (OnEffect calling
//     SendEvent), and that response can only arrive if the read loop
//     keeps draining the socket — so the dispatch queue is unbounded
//     (memory bounded by what the core pipelines ahead of the handler)
//     and enqueueing never blocks.
//  2. Violation-table keep semantics (external-gate.md §5): post-ready,
//     an unknown core message or a field/value violation in a core
//     frame is answered with an err response (unknown_message |
//     unknown_field | missing_field | invalid_field | unknown_enum) and
//     the connection is KEPT. Pre-ready (hello phase / synchronizing)
//     the same violations close the connection. Framing violations and
//     malformed response unions always close (handled in conn.go).
package gate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// dispatchQueue is an unbounded FIFO of handler callbacks drained by
// the dispatch goroutine. push never blocks (rule 1 above).
type dispatchQueue struct {
	mu    sync.Mutex
	cond  *sync.Cond
	items []func()
	done  bool
}

func newDispatchQueue() *dispatchQueue {
	q := &dispatchQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// push appends f without ever blocking; after close it is a no-op.
func (q *dispatchQueue) push(f func()) {
	q.mu.Lock()
	if !q.done {
		q.items = append(q.items, f)
	}
	q.mu.Unlock()
	q.cond.Signal()
}

// pop blocks until an item arrives or the queue closes; ok=false means
// closed (undrained items are dropped — the connection is dead).
func (q *dispatchQueue) pop() (f func(), ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && !q.done {
		q.cond.Wait()
	}
	if q.done {
		return nil, false
	}
	f = q.items[0]
	q.items[0] = nil
	q.items = q.items[1:]
	return f, true
}

func (q *dispatchQueue) close() {
	q.mu.Lock()
	q.done = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

// dispatchLoop runs handler callbacks strictly one at a time, in
// arrival order — this is what serializes bind acks.
func (c *Conn) dispatchLoop() {
	for {
		f, ok := c.dq.pop()
		if !ok {
			return
		}
		f()
	}
}

// handleCoreMessage validates one core→gate request/notification and
// queues its handler dispatch. A returned error closes the connection;
// post-ready violations are answered and return nil (rule 2 above).
func (c *Conn) handleCoreMessage(m string, data []byte) error {
	c.mu.Lock()
	h := c.handler
	postReady := c.state == stateReadySent || c.state == stateReady
	c.mu.Unlock()

	var vio *coreViolation
	switch m {
	case "bind", "unbind":
		var f bindFrame
		if vio = decodeCoreFrame(data, &f, "id", "m", "binding_id", "address"); vio == nil {
			vio = validateBind(&f)
		}
		if vio == nil {
			if h == nil {
				return fmt.Errorf("gate: %s before a handler was registered", m)
			}
			unbind := m == "unbind"
			c.dq.push(func() { c.dispatchBind(h, f, unbind) })
			return nil
		}
	case "catch_up":
		var f catchUpFrame
		if vio = decodeCoreFrame(data, &f, "id", "m", "binding_id", "address", "start"); vio == nil {
			vio = validateCatchUp(&f)
		}
		if vio == nil {
			if h == nil {
				return errors.New("gate: catch_up before a handler was registered")
			}
			c.dq.push(func() { c.dispatchCatchUp(h, f) })
			return nil
		}
	case "effect":
		var f effectFrame
		if vio = decodeCoreFrame(data, &f, "id", "m", "binding_id", "address", "kind", "payload"); vio == nil {
			vio = validateEffect(&f)
		}
		if vio == nil {
			if h == nil {
				return errors.New("gate: effect before a handler was registered")
			}
			c.dq.push(func() { c.dispatchEffect(h, f) })
			return nil
		}
	case "activity":
		// Activity violations are dropped without a response in every
		// state (§5: invalid_field, response なし, keep).
		var f activityFrame
		if v := decodeCoreFrame(data, &f); v != nil {
			return nil
		}
		if err := validateActivity(&f); err != nil {
			return nil
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
		c.dq.push(func() { h.OnActivity(a) })
		return nil
	default:
		vio = &coreViolation{code: "unknown_message", detail: fmt.Sprintf("unknown core message %q", m)}
	}
	return c.answerViolation(m, data, postReady, vio)
}

// answerViolation applies the §5 table to one violating core frame:
// post-ready it answers an err response (when the frame carries an
// answerable id — the response goes through the dispatch queue so it
// cannot overtake responses for earlier frames) and keeps the
// connection; pre-ready it closes.
func (c *Conn) answerViolation(m string, data []byte, postReady bool, vio *coreViolation) error {
	if !postReady {
		return fmt.Errorf("gate: pre-ready core %s frame: %s (%s)", m, vio.code, vio.detail)
	}
	if id, ok := answerableID(data); ok {
		we := WireError{Code: vio.code, Detail: &vio.detail}
		c.dq.push(func() { c.respondErr(id, we) })
	}
	return nil
}

// answerableID extracts the frame's top-level id when it is a string
// within the 1..128-byte request-id grammar; ok=false means the frame
// cannot be answered and the violation is dropped (write 0).
func answerableID(data []byte) (id string, ok bool) {
	var probe struct {
		ID *string `json:"id"`
	}
	if err := json.Unmarshal(data, &probe); err != nil || probe.ID == nil {
		return "", false
	}
	if validateRequestID(*probe.ID) != nil {
		return "", false
	}
	return *probe.ID, true
}

// decodeCoreFrame strict-decodes one core→gate frame body into v and
// checks the required top-level members, classifying failures per §5:
// an unknown member is unknown_field, an absent required member is
// missing_field, a type mismatch is invalid_field. Frame-level JSON
// validity and duplicate members are settled by validateFrameShape
// before this runs.
func decodeCoreFrame(data []byte, v any, required ...string) *coreViolation {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return invalidField(err.Error())
		}
		if strings.HasPrefix(err.Error(), "json: unknown field") {
			return &coreViolation{code: "unknown_field", detail: err.Error()}
		}
		return invalidField(err.Error())
	}
	if dec.More() {
		return invalidField("trailing data after frame object")
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return invalidField(err.Error())
	}
	for _, name := range required {
		if _, present := members[name]; !present {
			return missingField(name)
		}
	}
	return nil
}

// ---- handler dispatch (runs on the dispatch goroutine) --------------

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
