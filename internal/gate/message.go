// Message types and field grammar for the external gate contract,
// gateway seat — protocol 3 (opencrab docs/design/external-gate.md,
// "D-1099 改訂": the core owns sessions only). The subset omoikane uses
// is: gate→core hello and said; core→gate say and activity; and the
// ok/err responses (every response carries m:"ok"|"err"). Every
// session-scoped frame carries the gateway's opaque session key
// ("session") — for omoikane that is the /talk thread id. There is no
// bind/provision/binding_id any more.
// Unknown members are ignored at every nesting level; duplicate members
// are rejected (wire.go). Client-side validation rejects outgoing
// grammar violations before any bytes reach the wire.
package gate

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
)

// ErrClosed reports an operation on a closed connection.
var ErrClosed = errors.New("gate: connection closed")

// WireError is an err response surfaced to callers: the code is one of
// the §5.4 stable codes, detail is nullable free text.
type WireError struct {
	Code   string
	Detail *string
}

func (e *WireError) Error() string {
	msg := "gate: wire error " + e.Code
	if e.Detail != nil {
		msg += ": " + *e.Detail
	}
	return msg
}

// Attachment is the spec's Attachment = {kind:"image", url}. url must
// be an absolute https URL (§3.2).
type Attachment struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
}

// Said is one gate→core said message: one external utterance,
// idempotent on (session, origin). Text may be empty only when
// Attachments is nonempty. Attachments is always present on the wire
// ([] when there are no images).
type Said struct {
	Session     string // opaque session key (the /talk thread id)
	Origin      string // nonempty, stable per external utterance
	AuthorID    string // nonempty
	AuthorLabel string // optional display name ("" = omitted)
	// Caller is the gateway-asserted caller role ("owner",
	// "trusted_user", "agent"; "" = omitted, which the core reads as
	// "agent"). The core trusts it as-is — the operator-installed
	// gateway is the sole authority on who the speaker is.
	Caller      string
	Text        string
	Attachments []Attachment
}

// Caller roles the gateway may assert (protocol 3 said.caller.role).
const (
	CallerOwner       = "owner"
	CallerTrustedUser = "trusted_user"
	CallerAgent       = "agent"
)

// MaxSessionKeyBytes is the protocol 3 upper bound of a session key.
const MaxSessionKeyBytes = 512

// validateSessionKey enforces the protocol 3 session key grammar:
// nonempty, at most 512 bytes, no control characters.
func validateSessionKey(key string) error {
	if key == "" {
		return errors.New("gate: session key must be nonempty")
	}
	if len(key) > MaxSessionKeyBytes {
		return fmt.Errorf("gate: session key exceeds %d bytes", MaxSessionKeyBytes)
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return errors.New("gate: session key must not contain control characters")
		}
	}
	return nil
}

func validateSaid(s *Said) error {
	if err := validateSessionKey(s.Session); err != nil {
		return fmt.Errorf("said: %w", err)
	}
	switch s.Caller {
	case "", CallerOwner, CallerTrustedUser, CallerAgent:
	default:
		return fmt.Errorf("gate: said caller %q is not a supported role", s.Caller)
	}
	if s.Origin == "" {
		return errors.New("gate: said origin must be nonempty")
	}
	if s.AuthorID == "" {
		return errors.New("gate: said author_id must be nonempty")
	}
	if s.Text == "" && len(s.Attachments) == 0 {
		return errors.New("gate: said requires text or nonempty attachments")
	}
	for i := range s.Attachments {
		a := &s.Attachments[i]
		if a.Kind != "image" {
			return fmt.Errorf("gate: said attachments[%d].kind must be \"image\"", i)
		}
		if !isAbsoluteHTTPSURL(a.URL) {
			return fmt.Errorf("gate: said attachments[%d].url must be an absolute https URL", i)
		}
	}
	return nil
}

// Say is one core→gate say request. ID is the delivery_id (canonical
// lowercase UUID); the response must echo it. Payload is the core's
// {"text": body} object; the gateway ignores members other than text
// (§3.4).
type Say struct {
	ID      string
	Session string
	Payload json.RawMessage
}

// Activity is one core→gate activity notification (display-only,
// best-effort, no response frame exists for it). State is "started" or
// "ended" (protocol 3 also sends "read" — the core is about to call
// the LLM with new inbound included; omoikane has no display for it and
// drops it).
type Activity struct {
	Session    string
	ActivityID string
	State      string
}

// SayResult is the handler's verdict on a say. The zero value is the
// unknown outcome: when the gateway cannot tell whether the external
// API accepted the post it must not fabricate ok/err — the connection
// closes without answering and the core records the delivery
// indeterminate (§3.4).
type SayResult struct {
	kind   sayResultKind
	detail *string
}

type sayResultKind int

const (
	sayUnknown sayResultKind = iota
	sayDelivered
	sayRejected
)

// SayDelivered reports confirmed external acceptance; the response is a
// plain ok (no origin travels back on the wire in V3).
func SayDelivered() SayResult { return SayResult{kind: sayDelivered} }

// SayRejected reports definite non-acceptance by the external API:
// err(code="external_rejected"). detail may be nil.
func SayRejected(detail *string) SayResult {
	return SayResult{kind: sayRejected, detail: detail}
}

// SayUnknown reports that acceptance is indeterminate; the connection
// closes without answering.
func SayUnknown() SayResult { return SayResult{} }

// HelloParams carries the per-instance values of the protocol 3 hello.
// protocol=3 / operation_protocol=1 / final_delivery="automatic" /
// operations=[] are fixed: omoikane declares no gateway operations, so
// the core delivers each turn's final text as a plain say.
type HelloParams struct {
	InstanceID string // canonical lowercase UUID (stable per librarian)
	AgentID    string // the core agent this instance speaks for
}

func (p *HelloParams) validate() error {
	if !IsCanonicalUUID(p.InstanceID) {
		return errors.New("gate: hello instance_id must be a canonical lowercase UUID")
	}
	if p.AgentID == "" {
		return errors.New("gate: hello agent_id must be nonempty")
	}
	return nil
}

// ---- wire frames ----------------------------------------------------

type helloFrame struct {
	ID                string            `json:"id"`
	M                 string            `json:"m"`
	Protocol          int               `json:"protocol"`
	InstanceID        string            `json:"instance_id"`
	AgentID           string            `json:"agent_id"`
	OperationProtocol int               `json:"operation_protocol"`
	FinalDelivery     string            `json:"final_delivery"`
	Operations        []json.RawMessage `json:"operations"`
}

type saidCaller struct {
	Role string `json:"role"`
}

type saidFrame struct {
	ID          string       `json:"id"`
	M           string       `json:"m"`
	Session     string       `json:"session"`
	Origin      string       `json:"origin"`
	AuthorID    string       `json:"author_id"`
	AuthorLabel string       `json:"author_label,omitempty"`
	Caller      *saidCaller  `json:"caller,omitempty"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments"`
}

// okFrame is a gateway-written ok response: exactly {id, m:"ok"} —
// a say response never carries seq.
type okFrame struct {
	ID string `json:"id"`
	M  string `json:"m"`
}

// errFrame is a gateway-written err response: {id, m:"err", code,
// detail}. Detail is a pointer WITHOUT omitempty: the member is always
// present, possibly null (ErrDetail = string|null).
type errFrame struct {
	ID     string  `json:"id"`
	M      string  `json:"m"`
	Code   string  `json:"code"`
	Detail *string `json:"detail"`
}

// sayFrame / activityFrame are the incoming core→gate shapes. Decoding
// ignores unknown members (§3.1); required-member presence is checked
// against the raw frame by the caller.
type sayFrame struct {
	ID      string          `json:"id"`
	Session string          `json:"session"`
	Payload json.RawMessage `json:"payload"`
}

type activityFrame struct {
	Session    string `json:"session"`
	ActivityID string `json:"activity_id"`
	State      string `json:"state"`
}

// validateRequestID enforces the 1..128-byte request id rule (§2).
func validateRequestID(id string) error {
	if id == "" || len(id) > 128 {
		return errors.New("gate: request id must be 1..128 bytes")
	}
	return nil
}

// validateSay checks an incoming say frame after decode. Its id is the
// delivery_id (canonical lowercase UUID text).
func validateSay(f *sayFrame) error {
	if !IsCanonicalUUID(f.ID) {
		return errors.New("gate: say id must be a canonical lowercase UUID (delivery_id)")
	}
	if err := validateSessionKey(f.Session); err != nil {
		return fmt.Errorf("say: %w", err)
	}
	if f.Payload == nil {
		return errors.New("gate: say payload is required")
	}
	return nil
}

// validateActivity checks an incoming activity frame. Violations do NOT
// close the connection: activity is best-effort and the caller drops
// the frame.
func validateActivity(f *activityFrame) error {
	if err := validateSessionKey(f.Session); err != nil {
		return fmt.Errorf("activity: %w", err)
	}
	if f.ActivityID == "" {
		return errors.New("gate: activity activity_id must be nonempty")
	}
	if f.State != "started" && f.State != "ended" && f.State != "read" {
		return fmt.Errorf("gate: activity state %q unknown", f.State)
	}
	return nil
}

// ---- shared field grammar helpers -----------------------------------

// IsCanonicalUUID reports canonical lowercase 8-4-4-4-12 hex form (UUID
// version unrestricted, §2). Exported: it is the single UUID-grammar
// contract for every gate-adjacent id (runtime static-instance config
// included).
func IsCanonicalUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch i {
		case 8, 13, 18, 23:
			if s[i] != '-' {
				return false
			}
		default:
			c := s[i]
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
	}
	return true
}

// isAbsoluteHTTPSURL reports an absolute https URL with a host.
func isAbsoluteHTTPSURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != ""
}
