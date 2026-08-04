package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// Star → listed; star again → idempotent; unstar → gone. Bookmarks are
// strictly per-user.
func TestBookmarkLifecycle(t *testing.T) {
	base, adminTok, st := testServer(t)
	ctx := context.Background()
	if err := st.CreateProject(ctx, &store.Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	eid, err := st.CreateEntry(ctx, &store.Entry{ProjectID: "p", Type: "design", Title: "T", Body: "B"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(ctx, &store.User{ID: "u2", Name: "u2", Role: "member"}); err != nil {
		t.Fatal(err)
	}
	u2Tok, err := st.CreateToken(ctx, "u2", "u2", []string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	do := func(method, path, tok string) (int, map[string]any) {
		req, _ := http.NewRequest(method, base+path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// Star twice (idempotent), listed once.
	for i := 0; i < 2; i++ {
		if code, _ := do("PUT", "/v1/entries/"+eid+"/bookmark", adminTok); code != 200 {
			t.Fatalf("star: %d", code)
		}
	}
	code, lst := do("GET", "/v1/me/bookmarks", adminTok)
	if code != 200 || int(lst["total"].(float64)) != 1 {
		t.Fatalf("list after star: code=%d total=%v", code, lst["total"])
	}
	bm := lst["bookmarks"].([]any)[0].(map[string]any)
	if bm["entry_id"] != eid || bm["entry_title"] != "T" {
		t.Fatalf("bookmark payload: %v", bm)
	}

	// Another user sees nothing.
	if _, lst := do("GET", "/v1/me/bookmarks", u2Tok); int(lst["total"].(float64)) != 0 {
		t.Fatalf("u2 sees someone else's bookmarks")
	}

	// Unknown entry → 404.
	if code, _ := do("PUT", "/v1/entries/T-NOPE/bookmark", adminTok); code != http.StatusNotFound {
		t.Fatalf("star unknown: %d", code)
	}

	// Unstar (twice — second is a no-op) → empty list.
	for i := 0; i < 2; i++ {
		if code, _ := do("DELETE", "/v1/entries/"+eid+"/bookmark", adminTok); code != 200 {
			t.Fatalf("unstar: %d", code)
		}
	}
	if _, lst := do("GET", "/v1/me/bookmarks", adminTok); int(lst["total"].(float64)) != 0 {
		t.Fatalf("list after unstar: %v", lst["total"])
	}
}
