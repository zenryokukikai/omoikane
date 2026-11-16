package dashboard

// /my/librarian save flow × external gate (protocol 3, opencrab
// D-1099). There is no admin plane: the save flow only mints a stable
// gate instance id for the librarian (the core creates the instance on
// the gate's first hello). The REAL opencrab.GateProvisioner runs here;
// only the runtime provisioner is faked.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/gate"
	"github.com/zenryokukikai/omoikane/internal/opencrab"
	"github.com/zenryokukikai/omoikane/internal/store"
)

// mountLibrarianGate is mountLibrarian with the gate feature on.
func mountLibrarianGate(t *testing.T) (*httptest.Server, *store.Store, string) {
	t.Helper()
	s := newDashStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, &store.User{
		ID: "alice", Name: "Alice", Role: "admin", Email: "alice@x.com",
	}); err != nil {
		t.Fatal(err)
	}
	tok, err := s.CreateToken(ctx, "alice", "test",
		[]string{"read", "write", "admin"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(s, false)
	if err != nil {
		t.Fatal(err)
	}
	h.Librarian = &fakeProvisioner{}
	h.Gate = opencrab.GateProvisioner{}
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, s, tok
}

// Gate feature off (h.Gate == nil): save succeeds and no gate state
// appears.
func TestMyLibrarianSaveGateOff(t *testing.T) {
	srv, s, tok := mountLibrarian(t, &fakeProvisioner{})
	code, _ := postFormBody(t, srv, "/my/librarian", tok,
		map[string]string{"name": "Lib"})
	if code != http.StatusSeeOther {
		t.Fatalf("save: HTTP %d, want 303", code)
	}
	ul, err := s.GetUserLibrarian(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if ul.GateInstanceID != "" {
		t.Errorf("gate_instance_id = %q, want empty (feature off)", ul.GateInstanceID)
	}
}

// Gate on: the first save mints a canonical UUID instance id and
// persists it; a second save keeps the same id (the core keys the
// instance — and the librarian's sessions — by it).
func TestMyLibrarianSaveGateRegistersInstance(t *testing.T) {
	srv, s, tok := mountLibrarianGate(t)
	code, body := postFormBody(t, srv, "/my/librarian", tok,
		map[string]string{"name": "Lib"})
	if code != http.StatusSeeOther {
		t.Fatalf("save: HTTP %d, want 303 — body: %s", code, body)
	}
	ul, err := s.GetUserLibrarian(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !gate.IsCanonicalUUID(ul.GateInstanceID) {
		t.Fatalf("gate_instance_id = %q, want a canonical UUID", ul.GateInstanceID)
	}

	code, body = postFormBody(t, srv, "/my/librarian", tok,
		map[string]string{"name": "Lib renamed"})
	if code != http.StatusSeeOther {
		t.Fatalf("second save: HTTP %d, want 303 — body: %s", code, body)
	}
	ul2, err := s.GetUserLibrarian(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if ul2.GateInstanceID != ul.GateInstanceID {
		t.Errorf("gate_instance_id changed across saves: %q → %q", ul.GateInstanceID, ul2.GateInstanceID)
	}
}
