// Message types and field grammar for the external gate V3 minimal
// contract (DESIGN-EXTGATE-V3.md §2, §3), gateway seat. The wire union
// is exactly: gate→core hello and said; core→gate bind, say, activity;
// and the ok/err responses (every response carries m:"ok"|"err").
// Unknown members are ignored at every nesting level; duplicate members
// are rejected (wire.go). Client-side validation rejects outgoing
// grammar violations before any bytes reach the wire.
package gate

import (
	"encoding/base64"
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

// Said is one gate→core said message (§3.3): one external utterance,
// idempotent on (binding_id, origin). Text may be empty only when
// Attachments is nonempty. Attachments is always present on the wire
// ([] when there are no images).
type Said struct {
	BindingID   string // canonical lowercase UUID
	Origin      string // nonempty, stable per external utterance
	AuthorID    string // nonempty
	Text        string
	Attachments []Attachment
}

func validateSaid(s *Said) error {
	if !IsCanonicalUUID(s.BindingID) {
		return errors.New("gate: said binding_id must be a canonical lowercase UUID")
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

// Binding is one core→gate bind target.
type Binding struct {
	BindingID string
	Address   string
}

// Say is one core→gate say request. ID is the delivery_id (canonical
// lowercase UUID); the response must echo it. Payload is the core's
// {"text": body} object; the gateway ignores members other than text
// (§3.4).
type Say struct {
	ID        string
	BindingID string
	Payload   json.RawMessage
}

// Activity is one core→gate activity notification (display-only,
// best-effort, no response frame exists for it). State is "started" or
// "ended" (§3.4).
type Activity struct {
	BindingID  string
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

// HelloParams carries the per-instance values of the hello frame.
// protocol=2 is fixed by the spec and hardcoded.
type HelloParams struct {
	InstanceID   string // canonical lowercase UUID
	Revision     uint64 // positive
	ConfigDigest string // 64 lowercase hex (SHA-256 of the config bytes)
}

func (p *HelloParams) validate() error {
	if !IsCanonicalUUID(p.InstanceID) {
		return errors.New("gate: hello instance_id must be a canonical lowercase UUID")
	}
	if p.Revision == 0 {
		return errors.New("gate: hello revision must be positive")
	}
	if !isLowerHexDigest(p.ConfigDigest) {
		return errors.New("gate: hello config_digest must be 64 lowercase hex")
	}
	return nil
}

// ---- wire frames ----------------------------------------------------

type helloFrame struct {
	ID           string `json:"id"`
	M            string `json:"m"`
	Protocol     int    `json:"protocol"`
	InstanceID   string `json:"instance_id"`
	Revision     uint64 `json:"revision"`
	ConfigDigest string `json:"config_digest"`
}

type saidFrame struct {
	ID          string       `json:"id"`
	M           string       `json:"m"`
	BindingID   string       `json:"binding_id"`
	Origin      string       `json:"origin"`
	AuthorID    string       `json:"author_id"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments"`
}

// okFrame is a gateway-written ok response: exactly {id, m:"ok"} —
// hello/bind/say responses never carry seq.
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

// bindFrame / sayFrame / activityFrame are the incoming core→gate
// shapes. Decoding ignores unknown members (§3.1); required-member
// presence is checked against the raw frame by the caller.
type bindFrame struct {
	ID        string `json:"id"`
	BindingID string `json:"binding_id"`
	Address   string `json:"address"`
}

type sayFrame struct {
	ID        string          `json:"id"`
	BindingID string          `json:"binding_id"`
	Payload   json.RawMessage `json:"payload"`
}

type activityFrame struct {
	BindingID  string `json:"binding_id"`
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

// validateBind checks an incoming bind frame after decode.
func validateBind(f *bindFrame) error {
	if err := validateRequestID(f.ID); err != nil {
		return err
	}
	if !IsCanonicalUUID(f.BindingID) {
		return errors.New("gate: bind binding_id must be a canonical lowercase UUID")
	}
	if f.Address == "" {
		return errors.New("gate: bind address must be nonempty")
	}
	return nil
}

// validateSay checks an incoming say frame after decode. Its id is the
// delivery_id (canonical lowercase UUID text).
func validateSay(f *sayFrame) error {
	if !IsCanonicalUUID(f.ID) {
		return errors.New("gate: say id must be a canonical lowercase UUID (delivery_id)")
	}
	if !IsCanonicalUUID(f.BindingID) {
		return errors.New("gate: say binding_id must be a canonical lowercase UUID")
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
	if !IsCanonicalUUID(f.BindingID) {
		return errors.New("gate: activity binding_id must be a canonical lowercase UUID")
	}
	if f.ActivityID == "" {
		return errors.New("gate: activity activity_id must be nonempty")
	}
	if f.State != "started" && f.State != "ended" {
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

// isLowerHexDigest reports a 64-char lowercase hex digest.
func isLowerHexDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// isStdPaddedBase64 reports canonical RFC 4648 standard padded base64:
// nonempty, strict decode, byte-exact round trip.
func isStdPaddedBase64(s string) bool {
	if s == "" {
		return false
	}
	b, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil {
		return false
	}
	return base64.StdEncoding.EncodeToString(b) == s
}

// isAbsoluteHTTPSURL reports an absolute https URL with a host.
func isAbsoluteHTTPSURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != ""
}
