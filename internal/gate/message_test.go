// Field-grammar tests: the per-kind event table and the shared value
// grammars, all client-side (nothing here touches a wire).
package gate

import (
	"encoding/json"
	"strings"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func validSaid() Event { return saidEventForGrammar() }

func saidEventForGrammar() Event {
	return Event{
		Kind:      "said",
		Address:   "place-1",
		BindingID: testBindingA,
		Author:    Author{ID: "author-1"},
		Content:   Text("hi"),
		Origin:    "origin-1",
	}
}

func TestValidateEventPerKindTable(t *testing.T) {
	mutate := func(f func(*Event)) Event {
		ev := validSaid()
		f(&ev)
		return ev
	}
	cases := []struct {
		name string
		ev   Event
		ok   bool
	}{
		{"said minimal text", validSaid(), true},
		{"said attachment only", mutate(func(e *Event) {
			e.Content = nil
			e.Attachments = []Attachment{{Kind: "image", URL: "https://example.invalid/a.png"}}
		}), true},
		{"said with optionals", mutate(func(e *Event) {
			e.Mentions = []string{"author-2"}
			e.ReplyTo = "origin-0"
		}), true},
		{"said no text no attachments", mutate(func(e *Event) { e.Content = nil }), false},
		{"said forbids target", mutate(func(e *Event) { e.Target = "origin-0" }), false},
		{"said forbids symbol", mutate(func(e *Event) { e.Symbol = "+1" }), false},
		{"said forbids removed", mutate(func(e *Event) { e.Removed = boolPtr(false) }), false},
		{"said forbids action", mutate(func(e *Event) { e.Action = &Action{} }), false},
		{"empty origin", mutate(func(e *Event) { e.Origin = "" }), false},
		{"empty address", mutate(func(e *Event) { e.Address = "" }), false},
		{"empty author id", mutate(func(e *Event) { e.Author.ID = "" }), false},
		{"bad binding uuid", mutate(func(e *Event) { e.BindingID = "NOT-A-UUID" }), false},
		{"uppercase binding uuid", mutate(func(e *Event) {
			e.BindingID = strings.ToUpper(testBindingA)
		}), false},
		{"empty mention", mutate(func(e *Event) { e.Mentions = []string{""} }), false},
		{"content without members", mutate(func(e *Event) { e.Content = &Content{} }), false},
		{"attachment http url", mutate(func(e *Event) {
			e.Attachments = []Attachment{{Kind: "image", URL: "http://example.invalid/a.png"}}
		}), false},
		{"attachment relative url", mutate(func(e *Event) {
			e.Attachments = []Attachment{{Kind: "image", URL: "/a.png"}}
		}), false},
		{"attachment unknown kind", mutate(func(e *Event) {
			e.Attachments = []Attachment{{Kind: "video", URL: "https://example.invalid/a.mp4"}}
		}), false},

		{"edited valid", mutate(func(e *Event) {
			e.Kind = "edited"
			e.Target = "origin-0"
		}), true},
		{"edited requires text", mutate(func(e *Event) {
			e.Kind = "edited"
			e.Target = "origin-0"
			e.Content = nil
		}), false},
		{"edited requires target", mutate(func(e *Event) { e.Kind = "edited" }), false},

		{"retracted valid", mutate(func(e *Event) {
			e.Kind = "retracted"
			e.Content = nil
			e.Target = "origin-0"
			e.Removed = boolPtr(true)
		}), true},
		{"retracted requires removed true", mutate(func(e *Event) {
			e.Kind = "retracted"
			e.Content = nil
			e.Target = "origin-0"
			e.Removed = boolPtr(false)
		}), false},
		{"retracted forbids content", mutate(func(e *Event) {
			e.Kind = "retracted"
			e.Target = "origin-0"
			e.Removed = boolPtr(true)
		}), false},
		{"retracted forbids mentions", mutate(func(e *Event) {
			e.Kind = "retracted"
			e.Content = nil
			e.Target = "origin-0"
			e.Removed = boolPtr(true)
			e.Mentions = []string{"author-2"}
		}), false},
		// Empty-but-non-nil slices never hit the wire under omitempty,
		// so they count as absent for the forbidden checks.
		{"retracted empty non-nil mentions ok", mutate(func(e *Event) {
			e.Kind = "retracted"
			e.Content = nil
			e.Target = "origin-0"
			e.Removed = boolPtr(true)
			e.Mentions = []string{}
		}), true},
		{"retracted empty non-nil attachments ok", mutate(func(e *Event) {
			e.Kind = "retracted"
			e.Content = nil
			e.Target = "origin-0"
			e.Removed = boolPtr(true)
			e.Attachments = []Attachment{}
		}), true},

		{"reacted valid removed false", mutate(func(e *Event) {
			e.Kind = "reacted"
			e.Content = nil
			e.Target = "origin-0"
			e.Symbol = "+1"
			e.Removed = boolPtr(false)
		}), true},
		{"reacted requires removed", mutate(func(e *Event) {
			e.Kind = "reacted"
			e.Content = nil
			e.Target = "origin-0"
			e.Symbol = "+1"
		}), false},
		{"reacted requires symbol", mutate(func(e *Event) {
			e.Kind = "reacted"
			e.Content = nil
			e.Target = "origin-0"
			e.Removed = boolPtr(true)
		}), false},

		{"ui_action valid", mutate(func(e *Event) {
			e.Kind = "ui_action"
			e.Content = nil
			e.Action = &Action{
				SurfaceID: "s1", ComponentID: "c1", ActionName: "a1", ResponderID: "r1",
			}
		}), true},
		{"ui_action requires action", mutate(func(e *Event) {
			e.Kind = "ui_action"
			e.Content = nil
		}), false},
		{"ui_action empty action member", mutate(func(e *Event) {
			e.Kind = "ui_action"
			e.Content = nil
			e.Action = &Action{SurfaceID: "s1", ComponentID: "", ActionName: "a1", ResponderID: "r1"}
		}), false},
		{"ui_action forbids target", mutate(func(e *Event) {
			e.Kind = "ui_action"
			e.Content = nil
			e.Target = "origin-0"
			e.Action = &Action{SurfaceID: "s1", ComponentID: "c1", ActionName: "a1", ResponderID: "r1"}
		}), false},

		{"unknown kind", mutate(func(e *Event) { e.Kind = "shouted" }), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEvent(&tc.ev)
			if tc.ok && err != nil {
				t.Fatalf("valid event rejected: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("invalid event accepted")
			}
		})
	}
}

func TestHelloParamsValidate(t *testing.T) {
	base := testHelloParams()
	mutate := func(f func(*HelloParams)) HelloParams {
		p := base
		f(&p)
		return p
	}
	if err := base.validate(); err != nil {
		t.Fatalf("valid params rejected: %v", err)
	}
	bad := []HelloParams{
		mutate(func(p *HelloParams) { p.KindID = "" }),
		mutate(func(p *HelloParams) { p.InstanceID = "not-a-uuid" }),
		mutate(func(p *HelloParams) { p.ConfigDigest = "abc" }),
		mutate(func(p *HelloParams) { p.ConfigDigest = strings.ToUpper(testDigest) }),
		mutate(func(p *HelloParams) { p.OriginScope = "global" }),
		mutate(func(p *HelloParams) { p.AddressForm = "" }),
	}
	for i, p := range bad {
		if err := p.validate(); err == nil {
			t.Fatalf("bad params %d accepted", i)
		}
	}
}

func TestValidateActivityTable(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := []struct {
		name string
		f    activityFrame
		ok   bool
	}{
		{"started with kind", activityFrame{M: "activity", Address: "p", ActivityID: "a", State: "started", Kind: str("turn")}, true},
		{"started background with label", activityFrame{M: "activity", Address: "p", ActivityID: "a", State: "started", Kind: str("background"), Label: str("thinking")}, true},
		// label is not in the spec's nonempty list: an empty label on
		// started counts as label-absent, not a violation.
		{"started empty label treated as absent", activityFrame{M: "activity", Address: "p", ActivityID: "a", State: "started", Kind: str("turn"), Label: str("")}, true},
		{"started without kind", activityFrame{M: "activity", Address: "p", ActivityID: "a", State: "started"}, false},
		{"started bad kind", activityFrame{M: "activity", Address: "p", ActivityID: "a", State: "started", Kind: str("foreground")}, false},
		{"progress with label", activityFrame{M: "activity", Address: "p", ActivityID: "a", State: "progress", Label: str("half")}, true},
		{"progress without label", activityFrame{M: "activity", Address: "p", ActivityID: "a", State: "progress"}, false},
		{"progress with kind", activityFrame{M: "activity", Address: "p", ActivityID: "a", State: "progress", Kind: str("turn"), Label: str("half")}, false},
		{"ended bare", activityFrame{M: "activity", Address: "p", ActivityID: "a", State: "ended"}, true},
		{"ended with label", activityFrame{M: "activity", Address: "p", ActivityID: "a", State: "ended", Label: str("done")}, false},
		{"unknown state", activityFrame{M: "activity", Address: "p", ActivityID: "a", State: "paused"}, false},
		{"empty address", activityFrame{M: "activity", ActivityID: "a", State: "ended"}, false},
		{"empty activity id", activityFrame{M: "activity", Address: "p", State: "ended"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateActivity(&tc.f)
			if tc.ok && err != nil {
				t.Fatalf("valid activity rejected: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("invalid activity accepted")
			}
		})
	}
}

func TestValueGrammarHelpers(t *testing.T) {
	if !isCanonicalUUID(testBindingA) {
		t.Fatal("canonical UUID rejected")
	}
	for _, s := range []string{"", strings.ToUpper(testBindingA), testBindingA + "0",
		"0190a1b2c3d47e5f8a6b0000000000aa", "0190a1b2-c3d4-7e5f-8a6b-00000000zzaa"} {
		if isCanonicalUUID(s) {
			t.Fatalf("non-canonical UUID %q accepted", s)
		}
	}
	if !isLowerHexDigest(testDigest) {
		t.Fatal("valid digest rejected")
	}
	for _, s := range []string{"", testDigest[:63], strings.ToUpper(testDigest)} {
		if isLowerHexDigest(s) {
			t.Fatalf("bad digest %q accepted", s)
		}
	}
	for _, s := range []string{"AAECAw==", "AA=="} {
		if !isStdPaddedBase64(s) {
			t.Fatalf("valid padded base64 %q rejected", s)
		}
	}
	// Canonical RFC 4648 standard padded form only: nonempty, no
	// whitespace/newlines, zero trailing padding bits, byte-exact
	// round trip.
	for _, s := range []string{"", "AAECAw", "!!", "AAECAw= =",
		"AAECAw==\n", "AAEC Aw==", "AB=="} {
		if isStdPaddedBase64(s) {
			t.Fatalf("non-canonical base64 %q accepted", s)
		}
	}
}

func TestEventFrameFieldOrder(t *testing.T) {
	ev := validSaid()
	b, err := json.Marshal(eventFrame{
		ID: "1", M: "event", Kind: ev.Kind, Address: ev.Address, BindingID: ev.BindingID,
		Author: ev.Author, Content: ev.Content, Origin: ev.Origin,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := keyOrder(t, b)
	want := []string{"id", "m", "kind", "address", "binding_id", "author", "content", "origin"}
	if len(got) != len(want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("keys = %v, want %v", got, want)
		}
	}
}

func TestWireErrorSerializesExplicitNulls(t *testing.T) {
	b, err := json.Marshal(WireError{Code: "unauthorized"})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `{"code":"unauthorized","at":null,"detail":null}` {
		t.Fatalf("wire error bytes = %s", got)
	}
}
