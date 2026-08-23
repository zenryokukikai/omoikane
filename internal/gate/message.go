// Message types and field grammar for external gate protocol 2
// (external-gate.md §5). Wire structs declare members in the spec's
// exact order — encoding/json serializes struct fields in declaration
// order, which is how the "object field order is as written" rule is
// met. Client-side validation here rejects outgoing grammar violations
// before any bytes reach the wire.
package gate

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
)

// ErrNotReady reports a local refusal: SendEvent and friends are only
// legal once Ready has been acknowledged (spec §6: before ready/bind
// completion the core answers instance_not_ready; we refuse locally
// instead of burning a round trip).
var ErrNotReady = errors.New("gate: connection is not ready")

// ErrClosed reports an operation on a closed connection.
var ErrClosed = errors.New("gate: connection closed")

// WireError is the exact error shape of the protocol, both as the err
// member of a response frame and as the error surfaced to callers when
// the core answers a request with err. At and Detail are pointers so a
// missing value serializes as an explicit null, never as an absent
// member (spec §3 error shape, reused by §5 WireErr).
type WireError struct {
	Code   string  `json:"code"`
	At     *string `json:"at"`
	Detail *string `json:"detail"`
}

func (e *WireError) Error() string {
	msg := "gate: wire error " + e.Code
	if e.At != nil {
		msg += " at " + *e.At
	}
	if e.Detail != nil {
		msg += ": " + *e.Detail
	}
	return msg
}

// Author is the spec's Author = {id, display?}.
type Author struct {
	ID      string `json:"id"`
	Display string `json:"display,omitempty"`
}

// Content is the spec's Content = {text?}. When the member is present
// it must carry at least one member, and text is its only member, so a
// present Content must have Text set.
type Content struct {
	Text *string `json:"text,omitempty"`
}

// Text returns a Content carrying text.
func Text(s string) *Content { return &Content{Text: &s} }

// Attachment is the spec's Attachment = {kind:"image", url,
// origin_author?}. Only URL-referenced images exist in the initial
// surface (spec §8).
type Attachment struct {
	Kind         string `json:"kind"`
	URL          string `json:"url"`
	OriginAuthor string `json:"origin_author,omitempty"`
}

// Action is the spec's Action for ui_action events. Context is free
// JSON or null; the member is always present on the wire.
type Action struct {
	SurfaceID   string          `json:"surface_id"`
	ComponentID string          `json:"component_id"`
	ActionName  string          `json:"action_name"`
	Context     json.RawMessage `json:"context"`
	ResponderID string          `json:"responder_id"`
}

// HelloParams carries the per-instance values of the hello frame. The
// literal members (protocol:2, ingress_discovery:"prebound",
// effects:["say"], capabilities:["open"]) are not parameters: the spec
// fixes them byte-exactly and this package hardcodes them.
type HelloParams struct {
	KindID       string
	InstanceID   string // canonical lowercase UUID
	Revision     uint64
	ConfigDigest string // 64 lowercase hex (SHA-256 of the config file)
	OriginScope  string // "instance" | "kind_address"
	AddressForm  string
}

func (p *HelloParams) validate() error {
	if p.KindID == "" {
		return errors.New("gate: hello kind_id must be nonempty")
	}
	if !isCanonicalUUID(p.InstanceID) {
		return errors.New("gate: hello instance_id must be a canonical lowercase UUID")
	}
	if !isLowerHexDigest(p.ConfigDigest) {
		return errors.New("gate: hello config_digest must be 64 lowercase hex")
	}
	if p.OriginScope != "instance" && p.OriginScope != "kind_address" {
		return errors.New("gate: hello origin_scope must be instance or kind_address")
	}
	if p.AddressForm == "" {
		return errors.New("gate: hello address_form must be nonempty")
	}
	return nil
}

// Event is one gate→core event. Kind decides which optional members are
// required or forbidden (spec §5 per-kind table); validateEvent
// enforces the table before the wire.
type Event struct {
	Kind        string // said | edited | retracted | reacted | ui_action
	Address     string
	BindingID   string // canonical lowercase UUID
	Author      Author
	Content     *Content
	Mentions    []string
	ReplyTo     string
	Origin      string
	Target      string
	Symbol      string
	Removed     *bool
	Action      *Action
	Attachments []Attachment
}

// Binding is one core→gate bind/unbind target.
type Binding struct {
	BindingID string
	Address   string
}

// CatchUp is one core→gate catch_up instruction, surfaced to the
// handler as-is (cursor logic beyond surfacing is out of G1 scope).
// Mode is "now", "beginning", or "cursor"; the cursor members are set
// only for "cursor".
type CatchUp struct {
	BindingID    string
	Address      string
	Mode         string
	CursorB64    string
	CursorDigest string
}

// Effect is one core→gate effect request. ID is the byte-equal
// canonical UUID text of the delivery_id; the response must echo it.
type Effect struct {
	ID        string
	BindingID string
	Address   string
	Kind      string // always "say" in the initial surface
	Payload   json.RawMessage
}

// Activity is one core→gate activity notification (fire-and-forget, no
// response frame exists for it).
type Activity struct {
	Address    string
	ActivityID string
	State      string // started | progress | ended
	Kind       string // turn | background; only for started
	Label      string
}

// EffectResult is the handler's verdict on an effect. The zero value is
// the unknown outcome: per the spec's no-fabrication rule (§7), when
// the gateway cannot tell whether the external API accepted the effect
// it must not invent delivered/rejected/err — the connection is closed
// without answering and the core records disconnect.
type EffectResult struct {
	kind    effectResultKind
	origin  string
	wireErr WireError
}

type effectResultKind int

const (
	effectUnknown effectResultKind = iota
	effectDelivered
	effectRejected
	effectWireErr
)

// EffectDelivered reports external acceptance with the platform's
// stable origin ID. origin must be nonempty; an empty origin is treated
// as the unknown outcome (connection closed without answering) because
// a delivered:true with empty origin is itself a protocol error.
func EffectDelivered(origin string) EffectResult {
	return EffectResult{kind: effectDelivered, origin: origin}
}

// EffectRejected reports a definitive non-delivery ({delivered:false}).
func EffectRejected() EffectResult {
	return EffectResult{kind: effectRejected}
}

// EffectWireErr reports a definitive external refusal as an err
// response. Only legal when the gateway is certain the external API did
// not accept the effect (spec §7).
func EffectWireErr(code string, at, detail *string) EffectResult {
	return EffectResult{kind: effectWireErr, wireErr: WireError{Code: code, At: at, Detail: detail}}
}

// EffectUnknown reports that acceptance is indeterminate; the
// connection closes without answering.
func EffectUnknown() EffectResult { return EffectResult{} }

// ReadParams parameterizes a gate→core read request. From==0 omits
// from; Limit==0 omits limit. When present, From must be positive and
// Limit within 1..1000.
type ReadParams struct {
	Address string
	From    int64
	Limit   uint32
}

// ReadEvent is one element of a read response.
type ReadEvent struct {
	Seq     int64   `json:"seq"`
	Kind    string  `json:"kind"`
	Author  Author  `json:"author"`
	Content Content `json:"content"`
	ReplyTo *int64  `json:"reply_to,omitempty"`
	Origin  string  `json:"origin,omitempty"`
}

// ReadResult is the decoded success payload of a read request.
type ReadResult struct {
	Events []ReadEvent
	Next   *int64
}

// ---- wire frames (field order = spec order) -------------------------

type helloFrame struct {
	ID               string   `json:"id"`
	M                string   `json:"m"`
	Protocol         int      `json:"protocol"`
	KindID           string   `json:"kind_id"`
	InstanceID       string   `json:"instance_id"`
	Revision         uint64   `json:"revision"`
	ConfigDigest     string   `json:"config_digest"`
	OriginScope      string   `json:"origin_scope"`
	AddressForm      string   `json:"address_form"`
	IngressDiscovery string   `json:"ingress_discovery"`
	Effects          []string `json:"effects"`
	Capabilities     []string `json:"capabilities"`
}

type helloOK struct {
	Protocol        int    `json:"protocol"`
	ConnectionEpoch uint64 `json:"connection_epoch"`
}

type readyFrame struct {
	ID              string `json:"id"`
	M               string `json:"m"`
	ConnectionEpoch uint64 `json:"connection_epoch"`
}

type failedFrame struct {
	ID              string `json:"id"`
	M               string `json:"m"`
	ConnectionEpoch uint64 `json:"connection_epoch"`
	Code            string `json:"code"`
}

type bindFrame struct {
	ID        string `json:"id"`
	M         string `json:"m"`
	BindingID string `json:"binding_id"`
	Address   string `json:"address"`
}

type eventFrame struct {
	ID          string       `json:"id"`
	M           string       `json:"m"`
	Kind        string       `json:"kind"`
	Address     string       `json:"address"`
	BindingID   string       `json:"binding_id"`
	Author      Author       `json:"author"`
	Content     *Content     `json:"content,omitempty"`
	Mentions    []string     `json:"mentions,omitempty"`
	ReplyTo     string       `json:"reply_to,omitempty"`
	Origin      string       `json:"origin"`
	Target      string       `json:"target,omitempty"`
	Symbol      string       `json:"symbol,omitempty"`
	Removed     *bool        `json:"removed,omitempty"`
	Action      *Action      `json:"action,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

type eventOK struct {
	Seq       *int64 `json:"seq"`
	BindingID string `json:"binding_id"`
}

type sourceCheckpointFrame struct {
	ID                   string  `json:"id"`
	M                    string  `json:"m"`
	BindingID            string  `json:"binding_id"`
	ExpectedCursorDigest *string `json:"expected_cursor_digest"`
	CursorB64            string  `json:"cursor_b64"`
}

type sourceCheckpointOK struct {
	CursorDigest string `json:"cursor_digest"`
	UpdatedAt    int64  `json:"updated_at"`
}

type placeClosedFrame struct {
	ID        string `json:"id"`
	M         string `json:"m"`
	BindingID string `json:"binding_id"`
	Address   string `json:"address"`
	Reason    string `json:"reason"`
}

type placeClosedOK struct {
	Closed bool `json:"closed"`
}

type catchUpFrame struct {
	ID        string       `json:"id"`
	M         string       `json:"m"`
	BindingID string       `json:"binding_id"`
	Address   string       `json:"address"`
	Start     catchUpStart `json:"start"`
}

type catchUpStart struct {
	Mode         string  `json:"mode"`
	CursorB64    *string `json:"cursor_b64,omitempty"`
	CursorDigest *string `json:"cursor_digest,omitempty"`
}

type effectFrame struct {
	ID        string          `json:"id"`
	M         string          `json:"m"`
	BindingID string          `json:"binding_id"`
	Address   string          `json:"address"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
}

type effectDeliveredOK struct {
	Delivered bool   `json:"delivered"`
	Origin    string `json:"origin"`
}

type effectRejectedOK struct {
	Delivered bool `json:"delivered"`
}

type activityFrame struct {
	M          string  `json:"m"`
	Address    string  `json:"address"`
	ActivityID string  `json:"activity_id"`
	State      string  `json:"state"`
	Kind       *string `json:"kind,omitempty"`
	Label      *string `json:"label,omitempty"`
}

type readFrame struct {
	ID      string  `json:"id"`
	M       string  `json:"m"`
	Address string  `json:"address"`
	From    *int64  `json:"from,omitempty"`
	Limit   *uint32 `json:"limit,omitempty"`
}

type readOK struct {
	Events []ReadEvent `json:"events"`
	Next   *int64      `json:"next,omitempty"`
}

type okResponseFrame struct {
	ID string `json:"id"`
	Ok any    `json:"ok"`
}

type errResponseFrame struct {
	ID  string    `json:"id"`
	Err WireError `json:"err"`
}

// emptyOK is the exact `{}` success payload of ready/bind/unbind acks.
type emptyOK struct{}

// ---- validation -----------------------------------------------------

// validateEvent enforces the per-kind required/forbidden table of §5
// before the wire. Members outside the table are forbidden for that
// kind.
func validateEvent(ev *Event) error {
	if ev.Address == "" {
		return errors.New("gate: event address must be nonempty")
	}
	if !isCanonicalUUID(ev.BindingID) {
		return errors.New("gate: event binding_id must be a canonical lowercase UUID")
	}
	if ev.Author.ID == "" {
		return errors.New("gate: event author.id must be nonempty")
	}
	if ev.Origin == "" {
		return errors.New("gate: event origin must be nonempty")
	}
	if ev.Content != nil && ev.Content.Text == nil {
		return errors.New("gate: event content must carry at least one member")
	}
	for i, m := range ev.Mentions {
		if m == "" {
			return fmt.Errorf("gate: event mentions[%d] must be nonempty", i)
		}
	}
	for i := range ev.Attachments {
		a := &ev.Attachments[i]
		if a.Kind != "image" {
			return fmt.Errorf("gate: event attachments[%d].kind must be \"image\"", i)
		}
		if !isAbsoluteHTTPSURL(a.URL) {
			return fmt.Errorf("gate: event attachments[%d].url must be an absolute https URL", i)
		}
	}

	hasText := ev.Content != nil && ev.Content.Text != nil && *ev.Content.Text != ""
	switch ev.Kind {
	case "said":
		if !hasText && len(ev.Attachments) == 0 {
			return errors.New("gate: said requires content.text or nonempty attachments")
		}
		return forbidEventMembers(ev, "said", forbidTarget|forbidSymbol|forbidRemoved|forbidAction)
	case "edited":
		if !hasText {
			return errors.New("gate: edited requires content.text")
		}
		if ev.Target == "" {
			return errors.New("gate: edited requires nonempty target")
		}
		return forbidEventMembers(ev, "edited", forbidSymbol|forbidRemoved|forbidAction)
	case "retracted":
		if ev.Target == "" {
			return errors.New("gate: retracted requires nonempty target")
		}
		if ev.Removed == nil || !*ev.Removed {
			return errors.New("gate: retracted requires removed:true")
		}
		return forbidEventMembers(ev, "retracted",
			forbidContent|forbidMentions|forbidReplyTo|forbidAttachments|forbidSymbol|forbidAction)
	case "reacted":
		if ev.Target == "" {
			return errors.New("gate: reacted requires nonempty target")
		}
		if ev.Symbol == "" {
			return errors.New("gate: reacted requires nonempty symbol")
		}
		if ev.Removed == nil {
			return errors.New("gate: reacted requires removed boolean")
		}
		return forbidEventMembers(ev, "reacted",
			forbidContent|forbidMentions|forbidReplyTo|forbidAttachments|forbidAction)
	case "ui_action":
		if ev.Action == nil {
			return errors.New("gate: ui_action requires action")
		}
		if ev.Action.SurfaceID == "" || ev.Action.ComponentID == "" ||
			ev.Action.ActionName == "" || ev.Action.ResponderID == "" {
			return errors.New("gate: ui_action action members must be nonempty")
		}
		return forbidEventMembers(ev, "ui_action",
			forbidContent|forbidMentions|forbidReplyTo|forbidAttachments|forbidTarget|forbidSymbol|forbidRemoved)
	default:
		return fmt.Errorf("gate: unknown event kind %q", ev.Kind)
	}
}

type eventMemberSet uint

const (
	forbidContent eventMemberSet = 1 << iota
	forbidMentions
	forbidReplyTo
	forbidAttachments
	forbidTarget
	forbidSymbol
	forbidRemoved
	forbidAction
)

// forbidEventMembers rejects members the per-kind table forbids. An
// empty-but-non-nil Mentions/Attachments slice counts as absent: under
// omitempty it never reaches the wire, so it cannot violate the table.
func forbidEventMembers(ev *Event, kind string, set eventMemberSet) error {
	fail := func(member string) error {
		return fmt.Errorf("gate: %s forbids %s", kind, member)
	}
	if set&forbidContent != 0 && ev.Content != nil {
		return fail("content")
	}
	if set&forbidMentions != 0 && len(ev.Mentions) > 0 {
		return fail("mentions")
	}
	if set&forbidReplyTo != 0 && ev.ReplyTo != "" {
		return fail("reply_to")
	}
	if set&forbidAttachments != 0 && len(ev.Attachments) > 0 {
		return fail("attachments")
	}
	if set&forbidTarget != 0 && ev.Target != "" {
		return fail("target")
	}
	if set&forbidSymbol != 0 && ev.Symbol != "" {
		return fail("symbol")
	}
	if set&forbidRemoved != 0 && ev.Removed != nil {
		return fail("removed")
	}
	if set&forbidAction != 0 && ev.Action != nil {
		return fail("action")
	}
	return nil
}

// validateRequestID enforces the 1..128-byte nonempty request id rule.
func validateRequestID(id string) error {
	if id == "" || len(id) > 128 {
		return errors.New("gate: request id must be 1..128 bytes")
	}
	return nil
}

// coreViolation classifies one incoming core-frame violation with its
// §5 violation-table stable code; detail feeds the err response.
type coreViolation struct {
	code   string // unknown_message | unknown_field | missing_field | invalid_field | unknown_enum
	detail string
}

func invalidField(detail string) *coreViolation {
	return &coreViolation{code: "invalid_field", detail: detail}
}

func missingField(member string) *coreViolation {
	return &coreViolation{code: "missing_field", detail: "missing member " + member}
}

func unknownEnum(detail string) *coreViolation {
	return &coreViolation{code: "unknown_enum", detail: detail}
}

// validateBind checks an incoming bind/unbind frame. Member presence is
// checked by the caller first, so an empty value here is present-but-
// invalid, never missing.
func validateBind(f *bindFrame) *coreViolation {
	if err := validateRequestID(f.ID); err != nil {
		return invalidField(err.Error())
	}
	if !isCanonicalUUID(f.BindingID) {
		return invalidField("bind binding_id must be a canonical lowercase UUID")
	}
	if f.Address == "" {
		return invalidField("bind address must be nonempty")
	}
	return nil
}

// validateCatchUp checks an incoming catch_up frame.
func validateCatchUp(f *catchUpFrame) *coreViolation {
	if err := validateRequestID(f.ID); err != nil {
		return invalidField(err.Error())
	}
	if !isCanonicalUUID(f.BindingID) {
		return invalidField("catch_up binding_id must be a canonical lowercase UUID")
	}
	if f.Address == "" {
		return invalidField("catch_up address must be nonempty")
	}
	switch f.Start.Mode {
	case "now", "beginning":
		if f.Start.CursorB64 != nil || f.Start.CursorDigest != nil {
			return invalidField("catch_up start forbids cursor members for mode " + f.Start.Mode)
		}
	case "cursor":
		if f.Start.CursorB64 == nil {
			return missingField("start.cursor_b64")
		}
		if !isStdPaddedBase64(*f.Start.CursorB64) {
			return invalidField("catch_up start cursor_b64 must be standard padded base64")
		}
		if f.Start.CursorDigest == nil {
			return missingField("start.cursor_digest")
		}
		if !isLowerHexDigest(*f.Start.CursorDigest) {
			return invalidField("catch_up start cursor_digest must be 64 lowercase hex")
		}
	case "":
		return missingField("start.mode")
	default:
		return unknownEnum(fmt.Sprintf("catch_up start mode %q unknown", f.Start.Mode))
	}
	return nil
}

// validateEffect checks an incoming effect frame. Its id is the
// canonical UUID text of the delivery_id, not a free-form request id.
func validateEffect(f *effectFrame) *coreViolation {
	if !isCanonicalUUID(f.ID) {
		return invalidField("effect id must be a canonical lowercase UUID")
	}
	if !isCanonicalUUID(f.BindingID) {
		return invalidField("effect binding_id must be a canonical lowercase UUID")
	}
	if f.Address == "" {
		return invalidField("effect address must be nonempty")
	}
	if f.Kind != "say" {
		return unknownEnum(fmt.Sprintf("effect kind %q unknown", f.Kind))
	}
	if f.Payload == nil {
		return missingField("payload")
	}
	return nil
}

// validateActivity checks an incoming activity frame. Violations do NOT
// close the connection: the caller drops the frame (spec §5 activity
// rule).
func validateActivity(f *activityFrame) error {
	if f.Address == "" {
		return errors.New("gate: activity address must be nonempty")
	}
	if f.ActivityID == "" {
		return errors.New("gate: activity activity_id must be nonempty")
	}
	switch f.State {
	case "started":
		if f.Kind == nil || (*f.Kind != "turn" && *f.Kind != "background") {
			return errors.New("gate: activity started requires kind turn|background")
		}
		// label is optional here and absent from the spec's nonempty
		// list, so label:"" counts as label-absent, not a violation.
	case "progress":
		if f.Kind != nil {
			return errors.New("gate: activity progress forbids kind")
		}
		if f.Label == nil || *f.Label == "" {
			return errors.New("gate: activity progress requires label")
		}
	case "ended":
		if f.Kind != nil || f.Label != nil {
			return errors.New("gate: activity ended forbids kind and label")
		}
	default:
		return fmt.Errorf("gate: activity state %q unknown", f.State)
	}
	return nil
}

// ---- shared field grammar helpers -----------------------------------

// isCanonicalUUID reports canonical lowercase 8-4-4-4-12 hex form.
func isCanonicalUUID(s string) bool {
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
// nonempty, strict decode (no non-zero trailing padding bits), and
// byte-exact round trip, which rejects whitespace/newlines and any
// other non-canonical encoding of the same bytes.
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
