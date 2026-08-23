package gate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---- UUIDv7 ---------------------------------------------------------

func TestNewUUIDv7(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := NewUUIDv7()
		if !isCanonicalUUID(id) {
			t.Fatalf("not canonical: %q", id)
		}
		if !isUUIDv7(id) {
			t.Fatalf("not v7/variant: %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id: %q", id)
		}
		seen[id] = true
	}
}

func TestIsUUIDv7Rejects(t *testing.T) {
	for _, s := range []string{
		"",
		"0189d6f0-1a2b-4c3d-8e4f-556677889900", // version 4
		"0189d6f0-1a2b-7c3d-ce4f-556677889900", // variant 110x
		"0189D6F0-1A2B-7C3D-8E4F-556677889900", // uppercase
		"0189d6f0-1a2b-7c3d-8e4f-55667788990",  // short
	} {
		if isUUIDv7(s) {
			t.Errorf("isUUIDv7(%q) = true, want false", s)
		}
	}
}

// ---- test scaffolding -----------------------------------------------

type adminCall struct {
	Method string
	Path   string
	Body   string
	Auth   string
	CType  string
}

// fakeAdmin records every request and plays scripted responses in
// order (the last response repeats).
type fakeAdmin struct {
	t     *testing.T
	calls []adminCall
	// responses to play, in order: status + body.
	statuses []int
	bodies   []string
}

func (f *fakeAdmin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, adminCall{
		Method: r.Method,
		Path:   r.URL.Path,
		Body:   string(body),
		Auth:   r.Header.Get("Authorization"),
		CType:  r.Header.Get("Content-Type"),
	})
	i := len(f.calls) - 1
	if i >= len(f.statuses) {
		i = len(f.statuses) - 1
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(f.statuses[i])
	_, _ = io.WriteString(w, f.bodies[i])
}

func newFakeAdmin(t *testing.T, statuses []int, bodies []string) (*fakeAdmin, *AdminClient) {
	t.Helper()
	f := &fakeAdmin{t: t, statuses: statuses, bodies: bodies}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, NewAdminClient(srv.URL, "op-token")
}

const testSchemaBody = `{"schema_id":"s1","role":"instance_config","format":"json-schema-2020-12","document_b64":"e30=","document_digest":"` +
	"44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a" + `","created_at":1}`

// ---- PUT idempotency + headers --------------------------------------

func TestAdminPutSchemaIdempotent(t *testing.T) {
	f, c := newFakeAdmin(t,
		[]int{201, 200},
		[]string{testSchemaBody, testSchemaBody})

	req := SchemaPut{Role: "instance_config", Format: "json-schema-2020-12", DocumentB64: "e30="}
	s1, created, err := c.PutSchema(context.Background(), "s1", req)
	if err != nil {
		t.Fatalf("first PUT: %v", err)
	}
	if !created {
		t.Error("first PUT: created = false, want true (201)")
	}
	if s1.SchemaID != "s1" || s1.DocumentDigest == "" || s1.CreatedAt != 1 {
		t.Errorf("schema DTO mismatch: %+v", s1)
	}

	s2, created, err := c.PutSchema(context.Background(), "s1", req)
	if err != nil {
		t.Fatalf("second PUT: %v", err)
	}
	if created {
		t.Error("second PUT: created = true, want false (200 byte-equivalent)")
	}
	if s2.SchemaID != "s1" {
		t.Errorf("second schema DTO mismatch: %+v", s2)
	}

	for i, call := range f.calls {
		if call.Method != http.MethodPut || call.Path != "/api/gate-schemas/s1" {
			t.Errorf("call %d: %s %s", i, call.Method, call.Path)
		}
		if call.Auth != "Bearer op-token" {
			t.Errorf("call %d: Authorization = %q", i, call.Auth)
		}
		if call.CType != "application/json; charset=utf-8" {
			t.Errorf("call %d: Content-Type = %q", i, call.CType)
		}
		if call.Body != `{"role":"instance_config","format":"json-schema-2020-12","document_b64":"e30="}` {
			t.Errorf("call %d body = %s", i, call.Body)
		}
	}
}

// ---- exact request body shapes (DTO field order) --------------------

func TestAdminRequestBodyShapes(t *testing.T) {
	id := NewUUIDv7()
	bid := NewUUIDv7()

	t.Run("kind put nulls in order", func(t *testing.T) {
		f, c := newFakeAdmin(t, []int{201}, []string{`{"kind_id":"k"}`})
		form, cfg, cu := "^thread-[0-9a-f]{8}$", "cfg", "none"
		if _, _, err := c.PutKind(context.Background(), "omoikane-talk", KindPut{
			ProtocolMajor:          2,
			OriginScope:            "instance",
			IngressDiscovery:       "prebound",
			AddressForm:            &form,
			ConfigSchemaID:         &cfg,
			SecretManifestSchemaID: nil,
			CatchUpMode:            &cu,
		}); err != nil {
			t.Fatal(err)
		}
		want := `{"protocol_major":2,"origin_scope":"instance","ingress_discovery":"prebound",` +
			`"address_form":"^thread-[0-9a-f]{8}$","config_schema_id":"cfg",` +
			`"binding_metadata_schema_id":null,"secret_manifest_schema_id":null,` +
			`"catch_up_mode":"none","cursor_schema_id":null}`
		if got := f.calls[0].Body; got != want {
			t.Errorf("kind body\n got %s\nwant %s", got, want)
		}
	})

	t.Run("instance put", func(t *testing.T) {
		f, c := newFakeAdmin(t, []int{201}, []string{`{"instance_id":"` + id + `"}`})
		if _, _, err := c.PutInstance(context.Background(), id, InstancePut{
			KindID: "omoikane-talk", Label: "plib-alice", SubjectID: 42,
			Enabled: true, ConfigB64: "e30=",
		}); err != nil {
			t.Fatal(err)
		}
		want := `{"kind_id":"omoikane-talk","label":"plib-alice","subject_id":42,"enabled":true,"config_b64":"e30="}`
		if got := f.calls[0].Body; got != want {
			t.Errorf("instance body\n got %s\nwant %s", got, want)
		}
		if f.calls[0].Path != "/api/gate-instances/"+id {
			t.Errorf("path = %s", f.calls[0].Path)
		}
	})

	t.Run("revision post", func(t *testing.T) {
		f, c := newFakeAdmin(t, []int{201},
			[]string{`{"instance_id":"` + id + `","revision":2,"config_digest":"` + strings.Repeat("a", 64) + `","enabled":true}`})
		rev, err := c.PostRevision(context.Background(), id, RevisionPost{
			ExpectedActiveRevision: 1, Enabled: true, ConfigB64: "e30=",
		})
		if err != nil {
			t.Fatal(err)
		}
		if rev.Revision != 2 || !rev.Enabled {
			t.Errorf("revision result: %+v", rev)
		}
		want := `{"expected_active_revision":1,"enabled":true,"config_b64":"e30="}`
		if got := f.calls[0].Body; got != want {
			t.Errorf("revision body\n got %s\nwant %s", got, want)
		}
		if f.calls[0].Method != http.MethodPost || f.calls[0].Path != "/api/gate-instances/"+id+"/revisions" {
			t.Errorf("call: %s %s", f.calls[0].Method, f.calls[0].Path)
		}
	})

	t.Run("binding put null members present", func(t *testing.T) {
		f, c := newFakeAdmin(t, []int{201}, []string{`{"binding_id":"` + bid + `"}`})
		if _, _, err := c.PutBinding(context.Background(), bid, BindingPut{
			InstanceID: id, Address: "thread-0a1b2c3d",
			Label: nil, BindingMetadataB64: "e30=", CatchUpStart: nil,
		}); err != nil {
			t.Fatal(err)
		}
		want := `{"instance_id":"` + id + `","address":"thread-0a1b2c3d","label":null,` +
			`"binding_metadata_b64":"e30=","catch_up_start":null}`
		if got := f.calls[0].Body; got != want {
			t.Errorf("binding body\n got %s\nwant %s", got, want)
		}
	})
}

// ---- error envelope mapping -----------------------------------------

func TestAdminErrorEnvelope(t *testing.T) {
	id := NewUUIDv7()
	_, c := newFakeAdmin(t, []int{409},
		[]string{`{"error":{"code":"instance_conflict","at":null,"detail":null}}`})
	_, _, err := c.PutInstance(context.Background(), id, InstancePut{
		KindID: "k", Label: "l", SubjectID: 1, Enabled: true, ConfigB64: "e30=",
	})
	var ae *AdminError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want *AdminError", err)
	}
	if ae.Status != 409 || ae.Code != "instance_conflict" || ae.At != nil || ae.Detail != nil {
		t.Errorf("AdminError = %+v", ae)
	}
}

func TestAdminErrorEnvelopeWithAtDetail(t *testing.T) {
	_, c := newFakeAdmin(t, []int{422},
		[]string{`{"error":{"code":"schema_validation_failed","at":"config_b64","detail":"nope"}}`})
	_, err := c.GetSchema(context.Background(), "s1")
	var ae *AdminError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want *AdminError", err)
	}
	if ae.At == nil || *ae.At != "config_b64" || ae.Detail == nil || *ae.Detail != "nope" {
		t.Errorf("AdminError = %+v", ae)
	}
	if !strings.Contains(ae.Error(), "schema_validation_failed") {
		t.Errorf("Error() = %q", ae.Error())
	}
}

func TestAdminErrorContractViolations(t *testing.T) {
	t.Run("non-envelope body", func(t *testing.T) {
		_, c := newFakeAdmin(t, []int{500}, []string{`internal server error`})
		_, err := c.GetKind(context.Background(), "k")
		var ae *AdminError
		if err == nil || errors.As(err, &ae) {
			t.Fatalf("err = %v, want a non-AdminError failure", err)
		}
	})
	t.Run("status/code mismatch", func(t *testing.T) {
		// instance_conflict is a 409-only code — arriving on 400 is
		// out of contract and must not be surfaced as an AdminError.
		_, c := newFakeAdmin(t, []int{400},
			[]string{`{"error":{"code":"instance_conflict","at":null,"detail":null}}`})
		_, err := c.GetKind(context.Background(), "k")
		var ae *AdminError
		if err == nil || errors.As(err, &ae) {
			t.Fatalf("err = %v, want a non-AdminError failure", err)
		}
		if !strings.Contains(err.Error(), "out-of-contract") {
			t.Errorf("err = %v", err)
		}
	})
}

// ---- delete / result bodies -----------------------------------------

func TestAdminDeleteOps(t *testing.T) {
	id, bid := NewUUIDv7(), NewUUIDv7()

	f, c := newFakeAdmin(t,
		[]int{200, 200},
		[]string{
			`{"instance_id":"` + id + `","deleted":true,"revision":2}`,
			`{"binding_id":"` + bid + `","closed":true}`,
		})
	di, err := c.DeleteInstance(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !di.Deleted || di.Revision != 2 {
		t.Errorf("delete instance: %+v", di)
	}
	db, err := c.DeleteBinding(context.Background(), bid)
	if err != nil {
		t.Fatal(err)
	}
	if !db.Closed || db.BindingID != bid {
		t.Errorf("delete binding: %+v", db)
	}
	if f.calls[0].Method != http.MethodDelete || f.calls[1].Method != http.MethodDelete {
		t.Errorf("methods: %s %s", f.calls[0].Method, f.calls[1].Method)
	}
	if f.calls[0].Body != "" {
		t.Errorf("DELETE sent a body: %q", f.calls[0].Body)
	}
}

// ---- client-side grammar checks -------------------------------------

func TestAdminGrammarChecks(t *testing.T) {
	f, c := newFakeAdmin(t, []int{200}, []string{`{}`})
	ctx := context.Background()
	v4 := "0189d6f0-1a2b-4c3d-8e4f-556677889900" // version 4, not 7
	v7 := NewUUIDv7()

	cases := []struct {
		name string
		call func() error
	}{
		{"instance put non-v7 id", func() error {
			_, _, err := c.PutInstance(ctx, v4, InstancePut{ConfigB64: "e30="})
			return err
		}},
		{"instance put bad base64", func() error {
			_, _, err := c.PutInstance(ctx, v7, InstancePut{ConfigB64: "not base64!"})
			return err
		}},
		{"binding put non-v7 id", func() error {
			_, _, err := c.PutBinding(ctx, v4, BindingPut{InstanceID: v7, BindingMetadataB64: "e30="})
			return err
		}},
		{"binding put supplied cursor missing", func() error {
			_, _, err := c.PutBinding(ctx, v7, BindingPut{
				InstanceID: v7, BindingMetadataB64: "e30=",
				CatchUpStart: &CatchUpStart{Mode: "supplied"},
			})
			return err
		}},
		{"get binding malformed uuid", func() error {
			_, err := c.GetBinding(ctx, "not-a-uuid")
			return err
		}},
		{"get instance uppercase uuid", func() error {
			_, err := c.GetInstance(ctx, strings.ToUpper(v7))
			return err
		}},
		{"schema put bad base64", func() error {
			_, _, err := c.PutSchema(ctx, "s1", SchemaPut{DocumentB64: "%%"})
			return err
		}},
		{"empty schema id", func() error {
			_, err := c.GetSchema(ctx, "")
			return err
		}},
	}
	for _, tc := range cases {
		if err := tc.call(); err == nil {
			t.Errorf("%s: no error", tc.name)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("grammar violations reached the wire: %d calls", len(f.calls))
	}
}

// ---- response DTO round trip ----------------------------------------

func TestAdminBindingResponseDecode(t *testing.T) {
	bid, iid := NewUUIDv7(), NewUUIDv7()
	body := `{"binding_id":"` + bid + `","instance_id":"` + iid + `",` +
		`"address":"thread-0a1b2c3d","label":null,"binding_metadata_b64":"e30=",` +
		`"purposes":["inbound","outbound"],"catch_up_start":null,` +
		`"place_public_key":"pk","subject_id":42,"closed_at":null,` +
		`"close_reason":null,"cursor_digest":null}`
	_, c := newFakeAdmin(t, []int{200}, []string{body})
	b, err := c.GetBinding(context.Background(), bid)
	if err != nil {
		t.Fatal(err)
	}
	if b.BindingID != bid || b.InstanceID != iid || b.SubjectID != 42 {
		t.Errorf("binding: %+v", b)
	}
	if len(b.Purposes) != 2 || b.Purposes[0] != "inbound" || b.Purposes[1] != "outbound" {
		t.Errorf("purposes: %v", b.Purposes)
	}
	if b.Label != nil || b.ClosedAt != nil || b.CloseReason != nil || b.CursorDigest != nil {
		t.Errorf("null members not null: %+v", b)
	}
	// Round-trip: the DTO re-serializes in spec member order.
	out, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != body {
		t.Errorf("re-marshal\n got %s\nwant %s", out, body)
	}
}
