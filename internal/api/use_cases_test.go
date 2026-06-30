package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// TestUseCaseEndToEnd walks the full use-case lifecycle through HTTP:
// upsert → list → link entry → get with entries → reverse list → unlink → 404 on missing.
func TestUseCaseEndToEnd(t *testing.T) {
	base, tok, st := testServer(t)
	ctx := context.Background()
	if err := st.CreateProject(ctx, &store.Project{ID: "kb", Name: "KB"}); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateEntry(ctx, &store.Entry{
		ProjectID: "kb", Type: "trap", Title: "Mouth weak",
		Body: "weak mouth articulation", Status: "ACTIVE",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1) Upsert (create) — slug derived from name_en.
	s, raw := doJSON(t, http.MethodPost, base+"/v1/use_cases", tok, map[string]any{
		"name_ja":         "口の動きが弱い",
		"name_en":         "Weak mouth articulation",
		"description_ja":  "発話時の口の開きが小さい",
		"description_en":  "Mouth opens too little when speaking",
		"domain":          "lipsync",
	}, nil)
	if s != 200 {
		t.Fatalf("upsert: %d %s", s, raw)
	}
	var created struct {
		ID     string `json:"id"`
		Slug   string `json:"slug"`
		NameJA string `json:"name_ja"`
		NameEN string `json:"name_en"`
	}
	json.Unmarshal(raw, &created)
	if created.Slug != "weak-mouth-articulation" || created.ID == "" {
		t.Fatalf("created: %+v", created)
	}
	if created.NameJA != "口の動きが弱い" {
		t.Fatalf("name_ja round-trip: %q", created.NameJA)
	}

	// 2) Upsert again with same name_en → same id (idempotent).
	s, raw = doJSON(t, http.MethodPost, base+"/v1/use_cases", tok, map[string]any{
		"name_ja": "口の開きが弱い", "name_en": "Weak mouth articulation", "domain": "lipsync",
	}, nil)
	if s != 200 {
		t.Fatalf("re-upsert: %d %s", s, raw)
	}
	var second struct {
		ID string `json:"id"`
	}
	json.Unmarshal(raw, &second)
	if second.ID != created.ID {
		t.Fatalf("re-upsert created a new id: %s vs %s", second.ID, created.ID)
	}

	// 3) List use cases.
	s, raw = doJSON(t, http.MethodGet, base+"/v1/use_cases", tok, nil, nil)
	if s != 200 {
		t.Fatalf("list: %d %s", s, raw)
	}
	var listed struct {
		Total    int `json:"total"`
		UseCases []struct {
			ID         string `json:"id"`
			EntryCount int    `json:"entry_count"`
		} `json:"use_cases"`
	}
	json.Unmarshal(raw, &listed)
	if listed.Total != 1 || len(listed.UseCases) != 1 {
		t.Fatalf("list: total=%d len=%d", listed.Total, len(listed.UseCases))
	}
	if listed.UseCases[0].EntryCount != 0 {
		t.Fatalf("entry_count before link: %d", listed.UseCases[0].EntryCount)
	}

	// 4) Link the entry.
	s, raw = doJSON(t, http.MethodPost, base+"/v1/use_cases/"+created.ID+"/entries",
		tok, map[string]any{"entry_id": id, "source": "test"}, nil)
	if s != 200 {
		t.Fatalf("link: %d %s", s, raw)
	}

	// 5) Get by id — includes entries.
	s, raw = doJSON(t, http.MethodGet, base+"/v1/use_cases/"+created.ID, tok, nil, nil)
	if s != 200 {
		t.Fatalf("get by id: %d %s", s, raw)
	}
	var got struct {
		UseCase      map[string]any   `json:"use_case"`
		Entries      []map[string]any `json:"entries"`
		EntriesTotal int              `json:"entries_total"`
	}
	json.Unmarshal(raw, &got)
	if got.EntriesTotal != 1 || len(got.Entries) != 1 {
		t.Fatalf("get: entries=%d total=%d", len(got.Entries), got.EntriesTotal)
	}

	// 6) Get by slug works too.
	s, _ = doJSON(t, http.MethodGet, base+"/v1/use_cases/weak-mouth-articulation",
		tok, nil, nil)
	if s != 200 {
		t.Fatalf("get by slug: %d", s)
	}

	// 7) Reverse — list use cases an entry belongs to.
	s, raw = doJSON(t, http.MethodGet, base+"/v1/entries/"+id+"/use_cases", tok, nil, nil)
	if s != 200 {
		t.Fatalf("reverse: %d %s", s, raw)
	}
	var rev struct {
		UseCases []map[string]any `json:"use_cases"`
	}
	json.Unmarshal(raw, &rev)
	if len(rev.UseCases) != 1 {
		t.Fatalf("reverse list: %+v", rev)
	}

	// 8) Unlink.
	s, _ = doJSON(t, http.MethodDelete,
		base+"/v1/use_cases/"+created.ID+"/entries/"+id, tok, nil, nil)
	if s != 204 {
		t.Fatalf("unlink: %d", s)
	}

	// 9) Get on non-existent slug → 404.
	s, _ = doJSON(t, http.MethodGet, base+"/v1/use_cases/no-such-thing", tok, nil, nil)
	if s != 404 {
		t.Fatalf("404 expected for missing slug, got %d", s)
	}
}

// TestUseCaseRejectsMissingNames covers the upsert validation path.
func TestUseCaseRejectsMissingNames(t *testing.T) {
	base, tok, _ := testServer(t)
	s, raw := doJSON(t, http.MethodPost, base+"/v1/use_cases", tok,
		map[string]any{"name_en": "only en"}, nil)
	if s != 400 {
		t.Fatalf("expected 400, got %d %s", s, raw)
	}
}

func TestUseCaseTreeAPI(t *testing.T) {
	base, tok, st := testServer(t)
	ctx := context.Background()
	if err := st.CreateProject(ctx, &store.Project{ID: "kb", Name: "KB"}); err != nil { t.Fatal(err) }

	// Three top-level UseCases via API.
	for _, n := range []struct{ ja, en string }{
		{"葉A","Leaf A"},{"葉B","Leaf B"},{"葉C","Leaf C"},
	} {
		s, raw := doJSON(t, "POST", base+"/v1/use_cases", tok, map[string]any{
			"name_ja": n.ja, "name_en": n.en,
		}, nil)
		if s != 200 { t.Fatalf("upsert %s: %d %s", n.en, s, raw) }
	}

	// level=top → 3 rows.
	s, raw := doJSON(t, "GET", base+"/v1/use_cases?level=top", tok, nil, nil)
	if s != 200 { t.Fatalf("list level=top: %d %s", s, raw) }
	var listed struct {
		Total int `json:"total"`
		UseCases []struct{
			ID, Slug string
			ChildCount int `json:"child_count"`
			ParentID string `json:"parent_id"`
		} `json:"use_cases"`
	}
	json.Unmarshal(raw, &listed)
	if listed.Total != 3 { t.Fatalf("top initial total: %d", listed.Total) }
	for _, r := range listed.UseCases {
		if r.ParentID != "" { t.Errorf("expected empty parent_id, got %q", r.ParentID) }
		if r.ChildCount != 0 { t.Errorf("expected ChildCount=0, got %d", r.ChildCount) }
	}

	// Stack a meta above Leaf A and Leaf B (parent_id on upsert).
	leafA := listed.UseCases[2] // alphabetical-ish via test seed; just pick one
	leafB := listed.UseCases[1]
	// Create meta first.
	s, raw = doJSON(t, "POST", base+"/v1/use_cases", tok, map[string]any{
		"name_ja":"メタAB","name_en":"Meta AB",
	}, nil)
	if s != 200 { t.Fatalf("create meta: %d %s", s, raw) }
	var metaRes struct{ ID string `json:"id"` }
	json.Unmarshal(raw, &metaRes)
	// Re-upsert leaves with parent_id (same slug → server updates parent_id).
	for _, leaf := range []struct{ slug, en string }{
		{leafA.Slug, "Leaf "+leafA.Slug[len(leafA.Slug)-1:]},
		{leafB.Slug, "Leaf "+leafB.Slug[len(leafB.Slug)-1:]},
	} {
		s, raw = doJSON(t, "POST", base+"/v1/use_cases", tok, map[string]any{
			"slug": leaf.slug, "name_ja":"葉(再)", "name_en": leaf.en,
			"parent_id": metaRes.ID,
		}, nil)
		if s != 200 { t.Fatalf("repoint %s: %d %s", leaf.slug, s, raw) }
	}

	// level=top now returns meta + leafC.
	s, raw = doJSON(t, "GET", base+"/v1/use_cases?level=top", tok, nil, nil)
	json.Unmarshal(raw, &listed)
	if listed.Total != 2 { t.Fatalf("after stack: top total %d", listed.Total) }
	gotMetaWithChildren := false
	for _, r := range listed.UseCases {
		if r.ID == metaRes.ID && r.ChildCount == 2 { gotMetaWithChildren = true }
	}
	if !gotMetaWithChildren { t.Errorf("meta should be top-level with ChildCount=2: %+v", listed.UseCases) }

	// GET meta — includes children[].
	s, raw = doJSON(t, "GET", base+"/v1/use_cases/"+metaRes.ID, tok, nil, nil)
	if s != 200 { t.Fatalf("get meta: %d %s", s, raw) }
	var detail struct{
		UseCase  map[string]any   `json:"use_case"`
		Parent   any              `json:"parent"`
		Children []map[string]any `json:"children"`
	}
	json.Unmarshal(raw, &detail)
	if detail.Parent != nil { t.Errorf("meta parent should be nil, got %v", detail.Parent) }
	if len(detail.Children) != 2 { t.Errorf("meta children: want 2, got %d", len(detail.Children)) }

	// ?parent_id= drilldown.
	s, raw = doJSON(t, "GET", base+"/v1/use_cases?parent_id="+metaRes.ID, tok, nil, nil)
	json.Unmarshal(raw, &listed)
	if listed.Total != 2 { t.Fatalf("drilldown count: want 2, got %d", listed.Total) }

	// POST /parent re-parents; empty parent_id un-roots back to top level.
	// (upsert cannot un-root — it preserves parent on empty — so this
	// endpoint is the only way back to the top.)
	s, raw = doJSON(t, "POST", base+"/v1/use_cases/"+leafA.Slug+"/parent", tok,
		map[string]any{"parent_id": ""}, nil)
	if s != 200 { t.Fatalf("un-root: %d %s", s, raw) }
	s, raw = doJSON(t, "GET", base+"/v1/use_cases?level=top", tok, nil, nil)
	json.Unmarshal(raw, &listed)
	// meta + leafC + un-rooted leafA = 3 at top level now.
	if listed.Total != 3 { t.Fatalf("after un-root: top total %d, want 3", listed.Total) }
}

// TestUseCaseLimitClamps over-limit doesn't get silently dropped to 30.
// The store used to do `if limit > 200 { limit = 30 }` which left callers
// reading fewer rows than they asked for. After the fix we cap at 200
// instead, so an "I want lots" caller gets the maximum, not the default.
func TestUseCaseLimitClamps(t *testing.T) {
	base, tok, st := testServer(t)
	ctx := context.Background()
	if err := st.CreateProject(ctx, &store.Project{ID: "kb", Name: "KB"}); err != nil { t.Fatal(err) }
	// Create 5 use cases; ?limit=500 must return all 5, not get reset to a
	// default that would still cover this size — the real protection is the
	// echoed `limit` field, which we assert is now 200 (the cap), not 30.
	for i := 0; i < 5; i++ {
		s, raw := doJSON(t, "POST", base+"/v1/use_cases", tok, map[string]any{
			"name_ja": "葉", "name_en": "Leaf " + string(rune('A'+i)),
		}, nil)
		if s != 200 { t.Fatalf("upsert leaf %d: %d %s", i, s, raw) }
	}
	s, raw := doJSON(t, "GET", base+"/v1/use_cases?limit=500", tok, nil, nil)
	if s != 200 { t.Fatalf("list: %d %s", s, raw) }
	var got struct {
		Total    int `json:"total"`
		Limit    int `json:"limit"`
		UseCases []map[string]any `json:"use_cases"`
	}
	json.Unmarshal(raw, &got)
	if got.Total != 5 { t.Fatalf("total: want 5, got %d", got.Total) }
	if len(got.UseCases) != 5 {
		t.Fatalf("got %d rows for ?limit=500 of 5 total; silent truncation regression?", len(got.UseCases))
	}
	if got.Limit != 200 {
		t.Errorf("echoed limit should be clamped to 200 (the cap), got %d", got.Limit)
	}
}
