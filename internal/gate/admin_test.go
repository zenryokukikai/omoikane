// Admin-plane client tests against a scripted fake admin server: the
// V3 6-operation surface, exact request member sets, the
// {"error":{code,detail}} envelope, and the status/code contract.
package gate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewUUIDv7(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		id := NewUUIDv7()
		if !IsCanonicalUUID(id) {
			t.Fatalf("NewUUIDv7() = %q, not canonical", id)
		}
		if id[14] != '7' {
			t.Fatalf("NewUUIDv7() = %q, version nibble %c", id, id[14])
		}
		switch id[19] {
		case '8', '9', 'a', 'b':
		default:
			t.Fatalf("NewUUIDv7() = %q, variant nibble %c", id, id[19])
		}
		if seen[id] {
			t.Fatalf("NewUUIDv7() repeated %q", id)
		}
		seen[id] = true
	}
}

// fakeAdmin records requests and answers from a script.
type fakeAdmin struct {
	mu     sync.Mutex
	hits   []adminHit
	status int
	body   string
}

type adminHit struct {
	Method, Path, Body, Auth, ContentType string
}

func (f *fakeAdmin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.hits = append(f.hits, adminHit{
		Method: r.Method, Path: r.URL.Path, Body: string(body),
		Auth: r.Header.Get("Authorization"), ContentType: r.Header.Get("Content-Type"),
	})
	status, out := f.status, f.body
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, out)
}

func (f *fakeAdmin) last(t *testing.T) adminHit {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.hits) == 0 {
		t.Fatal("no admin request arrived")
	}
	return f.hits[len(f.hits)-1]
}

func newFakeAdmin(t *testing.T, status int, body string) (*fakeAdmin, *AdminClient) {
	t.Helper()
	f := &fakeAdmin{status: status, body: body}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, NewAdminClient(srv.URL, "op-token")
}

const (
	adminInstanceID = "0190a1b2-c3d4-7e5f-8a6b-000000000010"
	adminBindingID  = "0190a1b2-c3d4-7e5f-8a6b-000000000020"
	instanceJSON    = `{"instance_id":"` + adminInstanceID + `","kind_id":"omoikane-talk",` +
		`"subject_id":42,"revision":1,"enabled":true,"config_b64":"e30=",` +
		`"config_digest":"` + "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a" + `",` +
		`"created_at":1,"updated_at":1,"deleted_at":null}`
	bindingJSON = `{"binding_id":"` + adminBindingID + `","instance_id":"` + adminInstanceID + `",` +
		`"address":"thread-0000aaaa","created_at":1,"closed_at":null}`
)

func adminCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestInstancePutRequestShape pins operation 2: exact member set
// {kind_id, subject_id, enabled, config_b64}, Bearer auth, and the
// created flag on 201.
func TestInstancePutRequestShape(t *testing.T) {
	f, c := newFakeAdmin(t, http.StatusCreated, instanceJSON)
	inst, created, err := c.PutInstance(adminCtx(t), adminInstanceID, InstancePut{
		KindID: "omoikane-talk", SubjectID: 42, Enabled: true, ConfigB64: "e30=",
	})
	if err != nil {
		t.Fatalf("PutInstance: %v", err)
	}
	if !created || inst.InstanceID != adminInstanceID || inst.SubjectID != 42 ||
		inst.Revision != 1 || inst.DeletedAt != nil {
		t.Fatalf("PutInstance = (%+v, %v)", inst, created)
	}
	hit := f.last(t)
	if hit.Method != http.MethodPut || hit.Path != "/api/gate-instances/"+adminInstanceID {
		t.Fatalf("request = %s %s", hit.Method, hit.Path)
	}
	if hit.Auth != "Bearer op-token" {
		t.Fatalf("Authorization = %q", hit.Auth)
	}
	sameMembers(t, []byte(hit.Body), "kind_id", "subject_id", "enabled", "config_b64")
	var req map[string]any
	if err := json.Unmarshal([]byte(hit.Body), &req); err != nil {
		t.Fatal(err)
	}
	if req["kind_id"] != "omoikane-talk" || req["subject_id"] != float64(42) ||
		req["enabled"] != true || req["config_b64"] != "e30=" {
		t.Fatalf("PUT body = %s", hit.Body)
	}
}

// TestInstancePutIdempotent: byte-equivalent existing answers 200 and
// created=false.
func TestInstancePutIdempotent(t *testing.T) {
	_, c := newFakeAdmin(t, http.StatusOK, instanceJSON)
	_, created, err := c.PutInstance(adminCtx(t), adminInstanceID, InstancePut{
		KindID: "omoikane-talk", SubjectID: 42, Enabled: true, ConfigB64: "e30=",
	})
	if err != nil || created {
		t.Fatalf("PutInstance = (created=%v, %v), want (false, nil)", created, err)
	}
}

func TestInstanceGetAndDelete(t *testing.T) {
	f, c := newFakeAdmin(t, http.StatusOK, instanceJSON)
	inst, err := c.GetInstance(adminCtx(t), adminInstanceID)
	if err != nil || inst.KindID != "omoikane-talk" {
		t.Fatalf("GetInstance = (%+v, %v)", inst, err)
	}
	hit := f.last(t)
	if hit.Method != http.MethodGet || hit.Body != "" || hit.ContentType != "" {
		t.Fatalf("GET carried a body: %+v", hit)
	}

	f.mu.Lock()
	f.body = `{"instance_id":"` + adminInstanceID + `","deleted":true}`
	f.mu.Unlock()
	del, err := c.DeleteInstance(adminCtx(t), adminInstanceID)
	if err != nil || !del.Deleted || del.InstanceID != adminInstanceID {
		t.Fatalf("DeleteInstance = (%+v, %v)", del, err)
	}
	hit = f.last(t)
	if hit.Method != http.MethodDelete || hit.Body != "" {
		t.Fatalf("DELETE carried a body: %+v", hit)
	}
}

// TestRevisionPostRequestShape pins operation 4: exact member set
// {expected_revision, enabled, config_b64} and the success body.
func TestRevisionPostRequestShape(t *testing.T) {
	f, c := newFakeAdmin(t, http.StatusCreated,
		`{"instance_id":"`+adminInstanceID+`","revision":2,"enabled":true,`+
			`"config_digest":"44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"}`)
	rev, err := c.PostRevision(adminCtx(t), adminInstanceID, RevisionPost{
		ExpectedRevision: 1, Enabled: true, ConfigB64: "e30=",
	})
	if err != nil || rev.Revision != 2 {
		t.Fatalf("PostRevision = (%+v, %v)", rev, err)
	}
	hit := f.last(t)
	if hit.Method != http.MethodPost || hit.Path != "/api/gate-instances/"+adminInstanceID+"/revisions" {
		t.Fatalf("request = %s %s", hit.Method, hit.Path)
	}
	sameMembers(t, []byte(hit.Body), "expected_revision", "enabled", "config_b64")
}

// TestBindingPutRequestShape pins operation 5: exact member set
// {instance_id, address} — label, metadata, catch-up start are gone.
func TestBindingPutRequestShape(t *testing.T) {
	f, c := newFakeAdmin(t, http.StatusCreated, bindingJSON)
	b, created, err := c.PutBinding(adminCtx(t), adminBindingID, BindingPut{
		InstanceID: adminInstanceID, Address: "thread-0000aaaa",
	})
	if err != nil || !created || b.BindingID != adminBindingID || b.Address != "thread-0000aaaa" {
		t.Fatalf("PutBinding = (%+v, %v, %v)", b, created, err)
	}
	hit := f.last(t)
	if hit.Path != "/api/gate-bindings/"+adminBindingID {
		t.Fatalf("path = %s", hit.Path)
	}
	sameMembers(t, []byte(hit.Body), "instance_id", "address")
}

func TestBindingDelete(t *testing.T) {
	f, c := newFakeAdmin(t, http.StatusOK, `{"binding_id":"`+adminBindingID+`","closed":true}`)
	closed, err := c.DeleteBinding(adminCtx(t), adminBindingID)
	if err != nil || !closed.Closed {
		t.Fatalf("DeleteBinding = (%+v, %v)", closed, err)
	}
	hit := f.last(t)
	if hit.Method != http.MethodDelete || hit.Body != "" {
		t.Fatalf("DELETE carried a body: %+v", hit)
	}
}

// TestAdminErrorEnvelope: the V3 envelope is {"error":{code,detail}}
// (no at member) and decodes into AdminError.
func TestAdminErrorEnvelope(t *testing.T) {
	_, c := newFakeAdmin(t, http.StatusConflict,
		`{"error":{"code":"instance_active","detail":"live connection"}}`)
	_, err := c.DeleteInstance(adminCtx(t), adminInstanceID)
	var ae *AdminError
	if !errors.As(err, &ae) || ae.Status != 409 || ae.Code != "instance_active" ||
		ae.Detail == nil || *ae.Detail != "live connection" {
		t.Fatalf("err = %v, want AdminError instance_active", err)
	}
}

func TestAdminErrorNullDetail(t *testing.T) {
	_, c := newFakeAdmin(t, http.StatusUnauthorized,
		`{"error":{"code":"unauthorized","detail":null}}`)
	_, err := c.GetInstance(adminCtx(t), adminInstanceID)
	var ae *AdminError
	if !errors.As(err, &ae) || ae.Code != "unauthorized" || ae.Detail != nil {
		t.Fatalf("err = %v, want AdminError unauthorized with null detail", err)
	}
}

// TestAdminErrorContractViolations: a non-envelope body, an unknown
// code, or a code on the wrong status all fail loudly (not as
// AdminError).
func TestAdminErrorContractViolations(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"non-envelope", http.StatusBadRequest, `oops`},
		{"unknown code", http.StatusBadRequest, `{"error":{"code":"kind_unknown","detail":null}}`},
		{"code on wrong status", http.StatusBadRequest, `{"error":{"code":"instance_active","detail":null}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, c := newFakeAdmin(t, tc.status, tc.body)
			_, err := c.GetInstance(adminCtx(t), adminInstanceID)
			var ae *AdminError
			if err == nil || errors.As(err, &ae) {
				t.Fatalf("err = %v, want a loud non-AdminError failure", err)
			}
		})
	}
}

// TestAdminGrammarChecks: client-side refusals happen before any wire
// I/O.
func TestAdminGrammarChecks(t *testing.T) {
	f, c := newFakeAdmin(t, http.StatusOK, instanceJSON)
	ctx := adminCtx(t)
	fails := []func() error{
		func() error { _, err := c.GetInstance(ctx, "NOT-A-UUID"); return err },
		func() error {
			_, _, err := c.PutInstance(ctx, adminInstanceID, InstancePut{KindID: "", SubjectID: 1, ConfigB64: "e30="})
			return err
		},
		func() error {
			_, _, err := c.PutInstance(ctx, adminInstanceID, InstancePut{KindID: "k", SubjectID: 0, ConfigB64: "e30="})
			return err
		},
		func() error {
			_, _, err := c.PutInstance(ctx, adminInstanceID, InstancePut{KindID: "k", SubjectID: 1, ConfigB64: "not base64!"})
			return err
		},
		func() error {
			_, err := c.PostRevision(ctx, adminInstanceID, RevisionPost{ExpectedRevision: 0, ConfigB64: "e30="})
			return err
		},
		func() error {
			_, _, err := c.PutBinding(ctx, adminBindingID, BindingPut{InstanceID: "nope", Address: "a"})
			return err
		},
		func() error {
			_, _, err := c.PutBinding(ctx, adminBindingID, BindingPut{InstanceID: adminInstanceID, Address: ""})
			return err
		},
		func() error { _, err := c.DeleteBinding(ctx, strings.ToUpper(adminBindingID)); return err },
	}
	for i, fn := range fails {
		if err := fn(); err == nil {
			t.Fatalf("case %d: invalid input accepted", i)
		}
	}
	f.mu.Lock()
	n := len(f.hits)
	f.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d requests reached the wire before validation", n)
	}
}
