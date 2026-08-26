// Core→gate frame handling and handler dispatch for Conn (split from
// conn.go by responsibility: conn.go owns the state machine and the
// gate→core request side; this file owns what the core sends us).
//
// Rules shaping this file:
//
//  1. Liveness: the read loop must NEVER block on dispatch. A handler
//     callback may itself wait on a core response (OnSay calling
//     SendSaid), and that response can only arrive if the read loop
//     keeps draining the socket — so the dispatch queue is unbounded
//     (memory bounded by what the core pipelines ahead of the handler)
//     and enqueueing never blocks. Dispatch begins once Start
//     registered the handler; frames queue in arrival order until then.
//  2. Gateway failure codes (V3 §3.3/§10): a bind the gateway cannot
//     accept is err(code="bind_failed"); a say the external API
//     definitely did not accept — malformed payload included (§3.4) —
//     is err(code="external_rejected"). No other gateway failure codes
//     exist. An indeterminate say outcome closes the socket without
//     answering (no fabrication).
//  3. A structurally invalid bind/say frame from the core (missing
//     required members, wrong types) is a contract violation by a
//     trusted peer: the connection closes (the core moves pending
//     deliveries to indeterminate). Invalid activity frames are dropped
//     (display-only best-effort). An unknown message m is dropped and
//     the connection kept (write 0).
package gate

import (
	"fmt"
	"sync"
)

// dispatchQueue is an unbounded FIFO of handler callbacks drained by
// the dispatch goroutine. push never blocks (rule 1 above).
type dispatchQueue struct {
	mu    sync.Mutex
	cond  *sync.Cond
	items []func(Handler)
	done  bool
}

func newDispatchQueue() *dispatchQueue {
	q := &dispatchQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// push appends f without ever blocking; after close it is a no-op.
func (q *dispatchQueue) push(f func(Handler)) {
	q.mu.Lock()
	if !q.done {
		q.items = append(q.items, f)
	}
	q.mu.Unlock()
	q.cond.Signal()
}

// pop blocks until an item arrives or the queue closes; ok=false means
// closed (undrained items are dropped — the connection is dead).
func (q *dispatchQueue) pop() (f func(Handler), ok bool) {
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
// arrival order. It waits for Start (or close) before the first pop, so
// binds arriving right after the hello ok are held until a handler
// exists.
func (c *Conn) dispatchLoop() {
	select {
	case <-c.started:
	case <-c.closed:
		return
	}
	c.mu.Lock()
	h := c.handler
	c.mu.Unlock()
	for {
		f, ok := c.dq.pop()
		if !ok {
			return
		}
		f(h)
	}
}

// handleCoreMessage validates one core→gate request/notification and
// queues its handler dispatch. A returned error closes the connection.
func (c *Conn) handleCoreMessage(m string, data []byte) error {
	switch m {
	case "bind":
		var f bindFrame
		members, err := decodeFrame(data, &f)
		if err != nil {
			return err
		}
		if err := requireMembers(members, "id", "m", "binding_id", "address"); err != nil {
			return fmt.Errorf("gate: bind frame: %w", err)
		}
		if err := validateBind(&f); err != nil {
			return err
		}
		c.dq.push(func(h Handler) { c.dispatchBind(h, f) })
	case "say":
		var f sayFrame
		members, err := decodeFrame(data, &f)
		if err != nil {
			return err
		}
		if err := requireMembers(members, "id", "m", "binding_id", "payload"); err != nil {
			return fmt.Errorf("gate: say frame: %w", err)
		}
		if err := validateSay(&f); err != nil {
			return err
		}
		c.dq.push(func(h Handler) { c.dispatchSay(h, f) })
	case "activity":
		// Activity violations are dropped without a response: the
		// message is display-only best-effort (§3.4).
		var f activityFrame
		if _, err := decodeFrame(data, &f); err != nil {
			return nil
		}
		if err := validateActivity(&f); err != nil {
			return nil
		}
		c.dq.push(func(h Handler) {
			h.OnActivity(Activity{BindingID: f.BindingID, ActivityID: f.ActivityID, State: f.State})
		})
	default:
		// Unknown message m: keep the connection, effect 0. The core is
		// a trusted peer that only sends the V3 union; anything else is
		// a version skew this side does not police (§1).
	}
	return nil
}

// ---- handler dispatch (runs on the dispatch goroutine) --------------

func (c *Conn) dispatchBind(h Handler, f bindFrame) {
	if err := h.OnBind(Binding{BindingID: f.BindingID, Address: f.Address}); err != nil {
		detail := err.Error()
		c.respondErr(f.ID, "bind_failed", &detail)
		return
	}
	c.respondOk(f.ID)
}

func (c *Conn) dispatchSay(h Handler, f sayFrame) {
	res := h.OnSay(Say{ID: f.ID, BindingID: f.BindingID, Payload: f.Payload})
	switch res.kind {
	case sayDelivered:
		c.respondOk(f.ID)
	case sayRejected:
		c.respondErr(f.ID, "external_rejected", res.detail)
	default:
		// Unknown outcome: never fabricate ok/err — close the socket
		// without answering; the core records the delivery
		// indeterminate (§3.4, §6.3).
		c.closeWith(fmt.Errorf("gate: say %s outcome unknown, closing without answering", f.ID))
	}
}

// respondOk writes {id, m:"ok"} — the exact success response for bind
// and say (no seq, no origin).
func (c *Conn) respondOk(id string) {
	if err := c.fw.writeFrame(okFrame{ID: id, M: "ok"}); err != nil {
		c.closeWith(err)
	}
}

// respondErr writes {id, m:"err", code, detail} with detail always
// present (null when nil).
func (c *Conn) respondErr(id, code string, detail *string) {
	if err := c.fw.writeFrame(errFrame{ID: id, M: "err", Code: code, Detail: detail}); err != nil {
		c.closeWith(err)
	}
}
