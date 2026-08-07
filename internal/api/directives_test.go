package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// Directive lifecycle: create → list(active) → deactivate → delete,
// with creator-or-admin delete enforcement.
func TestDirectiveLifecycle(t *testing.T) {
	base, adminTok, st := testServer(t)
	ctx := context.Background()
	if err := st.CreateUser(ctx, &store.User{ID: "u2", Name: "u2", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	u2Tok, err := st.CreateToken(ctx, "u2", "u2", []string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	do := func(method, path, tok string, body any) (int, map[string]any) {
		var br = bytes.NewReader(nil)
		if body != nil {
			b, _ := json.Marshal(body)
			br = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, base+path, br)
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// Invalid role rejected.
	if code, _ := do("POST", "/v1/librarian/directives", adminTok,
		map[string]string{"role": "nonsense", "text": "x"}); code != http.StatusBadRequest {
		t.Fatalf("invalid role: %d", code)
	}
	// u2 creates a scout directive.
	code, d := do("POST", "/v1/librarian/directives", u2Tok,
		map[string]string{"role": "scout", "text": "量子化まわりに注目"})
	if code != http.StatusCreated || d["created_by"] != "u2" {
		t.Fatalf("create: %d %v", code, d)
	}
	did := d["id"].(string)

	// Active listing contains it.
	_, lst := do("GET", "/v1/librarian/directives?role=scout&active=1", adminTok, nil)
	if int(lst["total"].(float64)) != 1 {
		t.Fatalf("active list: %v", lst["total"])
	}
	// Deactivate → active listing empty, unfiltered listing keeps it.
	if code, _ := do("PATCH", "/v1/librarian/directives/"+did, adminTok,
		map[string]any{"active": false}); code != 200 {
		t.Fatalf("patch: %d", code)
	}
	if _, lst := do("GET", "/v1/librarian/directives?role=scout&active=1", adminTok, nil); int(lst["total"].(float64)) != 0 {
		t.Fatalf("deactivated still active-listed")
	}
	if _, lst := do("GET", "/v1/librarian/directives?role=scout", adminTok, nil); int(lst["total"].(float64)) != 1 {
		t.Fatalf("unfiltered listing lost the directive")
	}

	// A third user cannot delete u2's directive; admin can.
	if err := st.CreateUser(ctx, &store.User{ID: "u3", Name: "u3", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	u3Tok, _ := st.CreateToken(ctx, "u3", "u3", []string{"read", "write"}, nil)
	if code, _ := do("DELETE", "/v1/librarian/directives/"+did, u3Tok, nil); code != http.StatusForbidden {
		t.Fatalf("non-creator delete: %d", code)
	}
	if code, _ := do("DELETE", "/v1/librarian/directives/"+did, adminTok, nil); code != 200 {
		t.Fatalf("admin delete: %d", code)
	}
}
