// Framing tests: size cap, duplicate-member rejection, strict decode.
package gate

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestFrameReaderSplitsLines(t *testing.T) {
	r := newFrameReader(strings.NewReader("{\"a\":1}\n{\"b\":2}\n"))
	f1, err := r.next()
	if err != nil || string(f1) != `{"a":1}` {
		t.Fatalf("frame 1 = %q, %v", f1, err)
	}
	f2, err := r.next()
	if err != nil || string(f2) != `{"b":2}` {
		t.Fatalf("frame 2 = %q, %v", f2, err)
	}
}

func TestFrameReaderSizeCap(t *testing.T) {
	// Exactly MaxFrameBytes including LF is legal.
	body := `{"a":"` + strings.Repeat("x", MaxFrameBytes-9) + `"}`
	if len(body)+1 != MaxFrameBytes {
		t.Fatalf("test setup: frame is %d bytes incl LF, want %d", len(body)+1, MaxFrameBytes)
	}
	r := newFrameReader(strings.NewReader(body + "\n"))
	got, err := r.next()
	if err != nil {
		t.Fatalf("frame at exactly the cap: %v", err)
	}
	if len(got) != MaxFrameBytes-1 {
		t.Fatalf("frame length = %d", len(got))
	}

	// One byte over the cap is rejected.
	r = newFrameReader(strings.NewReader(body + "x\n"))
	if _, err := r.next(); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversized frame error = %v, want ErrFrameTooLarge", err)
	}
}

func TestFrameWriterSizeCap(t *testing.T) {
	var buf bytes.Buffer
	w := &frameWriter{w: &buf}
	big := strings.Repeat("x", MaxFrameBytes)
	if err := w.writeFrame(map[string]string{"a": big}); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversized write error = %v, want ErrFrameTooLarge", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("oversized write leaked %d bytes to the wire", buf.Len())
	}
	if err := w.writeFrame(map[string]string{"a": "b"}); err != nil {
		t.Fatalf("small write: %v", err)
	}
	if got := buf.String(); got != "{\"a\":\"b\"}\n" {
		t.Fatalf("wire bytes = %q", got)
	}
}

func TestValidateFrameShape(t *testing.T) {
	cases := []struct {
		name string
		data string
		err  error
	}{
		{"object ok", `{"a":1,"b":{"c":[1,2,{"d":null}]}}`, nil},
		{"duplicate top-level", `{"a":1,"a":2}`, errDuplicateMember},
		{"duplicate nested", `{"a":{"b":1,"b":2}}`, errDuplicateMember},
		{"duplicate in array element", `{"a":[{"b":1,"b":2}]}`, errDuplicateMember},
		{"not an object", `[1,2]`, errNotObject},
		{"scalar", `42`, errNotObject},
		{"trailing data", `{"a":1}{"b":2}`, errTrailingData},
		{"invalid utf8", "{\"a\":\"\xff\"}", errInvalidUTF8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFrameShape([]byte(tc.data))
			if tc.err == nil {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
		})
	}
}

func TestValidateFrameShapeMalformedJSON(t *testing.T) {
	for _, data := range []string{`{"a":`, `{`, ``, `{"a" 1}`} {
		if err := validateFrameShape([]byte(data)); err == nil {
			t.Fatalf("malformed %q accepted", data)
		}
	}
}

// V3 §3.1: unrecognized members are IGNORED (the old strict rejection
// is gone); required-member presence is checked against the raw frame.
func TestDecodeFrameIgnoresUnknownMembers(t *testing.T) {
	var f bindFrame
	members, err := decodeFrame([]byte(`{"id":"1","m":"bind","binding_id":"b","address":"a","extra":true}`), &f)
	if err != nil {
		t.Fatalf("frame with unknown member rejected: %v", err)
	}
	if f.ID != "1" || f.BindingID != "b" || f.Address != "a" {
		t.Fatalf("decoded = %+v", f)
	}
	if err := requireMembers(members, "id", "m", "binding_id", "address"); err != nil {
		t.Fatalf("required members reported missing: %v", err)
	}
	if err := requireMembers(members, "payload"); err == nil {
		t.Fatal("missing member not reported")
	}
	// Wrong member type is still a decode error.
	if _, err := decodeFrame([]byte(`{"id":7}`), &f); err == nil {
		t.Fatal("wrong-typed member accepted")
	}
}
