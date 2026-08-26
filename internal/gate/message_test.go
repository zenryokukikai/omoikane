// Field-grammar tests: said/hello validation and the shared value
// grammars, all client-side (nothing here touches a wire).
package gate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateSaidTable(t *testing.T) {
	valid := func() Said { return said(testBindingA, "origin-1") }
	mutate := func(f func(*Said)) Said {
		s := valid()
		f(&s)
		return s
	}
	cases := []struct {
		name string
		s    Said
		ok   bool
	}{
		{"minimal text", valid(), true},
		{"attachment only", mutate(func(s *Said) {
			s.Text = ""
			s.Attachments = []Attachment{{Kind: "image", URL: "https://example.invalid/a.png"}}
		}), true},
		{"text and attachment", mutate(func(s *Said) {
			s.Attachments = []Attachment{{Kind: "image", URL: "https://example.invalid/a.png"}}
		}), true},
		{"no text no attachments", mutate(func(s *Said) { s.Text = "" }), false},
		{"empty origin", mutate(func(s *Said) { s.Origin = "" }), false},
		{"empty author id", mutate(func(s *Said) { s.AuthorID = "" }), false},
		{"bad binding uuid", mutate(func(s *Said) { s.BindingID = "NOT-A-UUID" }), false},
		{"uppercase binding uuid", mutate(func(s *Said) {
			s.BindingID = strings.ToUpper(testBindingA)
		}), false},
		{"attachment http url", mutate(func(s *Said) {
			s.Attachments = []Attachment{{Kind: "image", URL: "http://example.invalid/a.png"}}
		}), false},
		{"attachment relative url", mutate(func(s *Said) {
			s.Attachments = []Attachment{{Kind: "image", URL: "/a.png"}}
		}), false},
		{"attachment unknown kind", mutate(func(s *Said) {
			s.Attachments = []Attachment{{Kind: "video", URL: "https://example.invalid/a.mp4"}}
		}), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSaid(&tc.s)
			if tc.ok && err != nil {
				t.Fatalf("valid said rejected: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("invalid said accepted")
			}
		})
	}
}

func TestHelloParamsValidate(t *testing.T) {
	ok := testHelloParams()
	if err := ok.validate(); err != nil {
		t.Fatalf("valid params rejected: %v", err)
	}
	cases := map[string]HelloParams{
		"bad instance uuid": {InstanceID: "nope", Revision: 1, ConfigDigest: testDigest},
		"zero revision":     {InstanceID: testInstanceID, Revision: 0, ConfigDigest: testDigest},
		"short digest":      {InstanceID: testInstanceID, Revision: 1, ConfigDigest: "abc"},
		"uppercase digest":  {InstanceID: testInstanceID, Revision: 1, ConfigDigest: strings.ToUpper(testDigest)},
	}
	for name, p := range cases {
		if err := p.validate(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

// TestErrFrameDetailAlwaysPresent: detail is a required response member
// (ErrDetail = string|null) — a nil detail serializes as explicit null,
// never as an absent member.
func TestErrFrameDetailAlwaysPresent(t *testing.T) {
	b, err := json.Marshal(errFrame{ID: "1", M: "err", Code: "bind_failed", Detail: nil})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `{"id":"1","m":"err","code":"bind_failed","detail":null}` {
		t.Fatalf("err frame = %s", got)
	}
	d := "why"
	b, _ = json.Marshal(errFrame{ID: "1", M: "err", Code: "external_rejected", Detail: &d})
	if got := string(b); got != `{"id":"1","m":"err","code":"external_rejected","detail":"why"}` {
		t.Fatalf("err frame = %s", got)
	}
}

func TestValidateRequestID(t *testing.T) {
	if err := validateRequestID(""); err == nil {
		t.Fatal("empty id accepted")
	}
	if err := validateRequestID(strings.Repeat("x", 129)); err == nil {
		t.Fatal("129-byte id accepted")
	}
	if err := validateRequestID(strings.Repeat("x", 128)); err != nil {
		t.Fatalf("128-byte id rejected: %v", err)
	}
}

func TestGrammarHelpers(t *testing.T) {
	if !IsCanonicalUUID(testBindingA) || IsCanonicalUUID(strings.ToUpper(testBindingA)) ||
		IsCanonicalUUID("0190a1b2c3d47e5f8a6b0000000000aa") {
		t.Fatal("IsCanonicalUUID misbehaves")
	}
	// V3 §2: UUID version is unrestricted — a v4 id is canonical too.
	if !IsCanonicalUUID("123e4567-e89b-42d3-a456-426614174000") {
		t.Fatal("v4 UUID rejected")
	}
	if !isLowerHexDigest(testDigest) || isLowerHexDigest(testDigest[:63]) ||
		isLowerHexDigest(strings.ToUpper(testDigest)) {
		t.Fatal("isLowerHexDigest misbehaves")
	}
	if !isStdPaddedBase64("AAECAw==") || isStdPaddedBase64("AAECAw") ||
		isStdPaddedBase64("") || isStdPaddedBase64("AAECAw==\n") {
		t.Fatal("isStdPaddedBase64 misbehaves")
	}
	if !isAbsoluteHTTPSURL("https://example.invalid/a.png") ||
		isAbsoluteHTTPSURL("http://example.invalid/a.png") ||
		isAbsoluteHTTPSURL("/a.png") || isAbsoluteHTTPSURL("https://") {
		t.Fatal("isAbsoluteHTTPSURL misbehaves")
	}
}
