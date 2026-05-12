package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kojira/omoikane/internal/config"
	"github.com/kojira/omoikane/internal/enrich"
	"github.com/kojira/omoikane/internal/store"
)

func testServer(t *testing.T) (base, tok string, st *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.CreateUser(context.Background(),
		&store.User{ID: "admin", Name: "admin", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	tok, err = st.CreateToken(context.Background(), "admin", "test",
		[]string{"read", "write", "admin"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &Handler{
		Store:       st,
		Enricher:    enrich.New("", "", "", "", logger),
		SecretsMode: config.SecretsEnforce,
		Logger:      logger,
	}
	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(Recoverer(logger))
	r.Use(Audit(st, logger))
	h.Mount(r)

	srv := httptest.NewServer(r)
	t.Cleanup(func() {
		srv.Close()
		_ = st.Close()
	})
	return srv.URL, tok, st
}

func doJSON(t *testing.T, method, url, tok string, body any, headers map[string]string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func TestHealthPublic(t *testing.T) {
	base, _, _ := testServer(t)
	s, _ := doJSON(t, http.MethodGet, base+"/v1/health", "", nil, nil)
	if s != 200 {
		t.Fatalf("status=%d", s)
	}
}

func TestRequiresAuth(t *testing.T) {
	base, _, _ := testServer(t)
	s, _ := doJSON(t, http.MethodGet, base+"/v1/projects", "", nil, nil)
	if s != 401 {
		t.Fatalf("status=%d", s)
	}
}

func TestProjectCRUD(t *testing.T) {
	base, tok, _ := testServer(t)
	s, _ := doJSON(t, http.MethodPost, base+"/v1/projects", tok,
		map[string]any{"id": "demo", "name": "Demo"}, nil)
	if s != 201 {
		t.Fatalf("create: %d", s)
	}
	s, _ = doJSON(t, http.MethodPost, base+"/v1/projects", tok,
		map[string]any{"id": "demo", "name": "Demo"}, nil)
	if s != 409 {
		t.Fatalf("dup: %d", s)
	}
}

func TestEntryCreateAndOCC(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")

	s, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	if s != 201 {
		t.Fatalf("create: %d %s", s, raw)
	}
	var created map[string]any
	_ = json.Unmarshal(raw, &created)
	id := created["id"].(string)

	// PATCH without If-Match → 428
	s, _ = doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"status": "ACTIVE"}, nil)
	if s != 428 {
		t.Fatalf("missing If-Match should be 428, got %d", s)
	}
	// PATCH with wrong version → 409
	s, raw = doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"status": "ACTIVE"}, map[string]string{"If-Match": "99"})
	if s != 409 {
		t.Fatalf("wrong If-Match should be 409, got %d body=%s", s, raw)
	}
	// PATCH with correct version → 200
	s, _ = doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"status": "ACTIVE"}, map[string]string{"If-Match": "1"})
	if s != 200 {
		t.Fatalf("good If-Match should be 200, got %d", s)
	}
}

func TestAsOfReturnsHistoricalSnapshot(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "v1", "body": "v1body",
	}, nil)
	var created map[string]any
	_ = json.Unmarshal(raw, &created)
	id := created["id"].(string)

	pivot := time.Now().UTC()
	time.Sleep(50 * time.Millisecond)

	doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"title": "v2"}, map[string]string{"If-Match": "1"})

	s, raw := doJSON(t, http.MethodGet,
		base+"/v1/entries/"+id+"?as_of="+pivot.Format(time.RFC3339Nano), tok, nil, nil)
	if s != 200 {
		t.Fatalf("as_of: %d %s", s, raw)
	}
	var snap map[string]any
	_ = json.Unmarshal(raw, &snap)
	if snap["title"] != "v1" {
		t.Fatalf("as_of snapshot should be v1, got %v", snap["title"])
	}
}

func TestSecretsRejectedIn422(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	s, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap",
		"title": "leak",
		"body":  "the token is ghp_1234567890abcdefghijKLMNOPQRSTUVWXYZ012",
	}, nil)
	if s != 422 {
		t.Fatalf("expected 422, got %d body=%s", s, raw)
	}
	if !bytes.Contains(raw, []byte("SECRETS_DETECTED")) {
		t.Fatalf("expected SECRETS_DETECTED code: %s", raw)
	}
	// Ensure the leaked value did NOT round-trip in the response.
	if bytes.Contains(raw, []byte("ghp_1234567890")) {
		t.Fatalf("response must not echo leaked value: %s", raw)
	}
}

func TestDeleteIsSoftAndIdempotent(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var created map[string]any
	_ = json.Unmarshal(raw, &created)
	id := created["id"].(string)

	s, _ := doJSON(t, http.MethodDelete, base+"/v1/entries/"+id, tok, nil, nil)
	if s != 204 {
		t.Fatalf("delete: %d", s)
	}
	// GET still works, entry is ARCHIVED with valid_to set
	s, raw = doJSON(t, http.MethodGet, base+"/v1/entries/"+id, tok, nil, nil)
	if s != 200 {
		t.Fatalf("get archived: %d %s", s, raw)
	}
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	if got["status"] != "ARCHIVED" {
		t.Fatalf("status=%v", got["status"])
	}
	if got["valid_to"] == nil {
		t.Fatal("valid_to should be set on archived entry")
	}
	// Idempotent
	s, _ = doJSON(t, http.MethodDelete, base+"/v1/entries/"+id, tok, nil, nil)
	if s != 204 {
		t.Fatalf("second delete: %d", s)
	}
}

func TestListPagination(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	for i := 0; i < 5; i++ {
		doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
			"project_id": "kb", "type": "trap",
			"title": "e" + strconv.Itoa(i), "body": "b",
		}, nil)
	}
	s, raw := doJSON(t, http.MethodGet, base+"/v1/entries?limit=2&offset=0", tok, nil, nil)
	if s != 200 {
		t.Fatalf("list: %d", s)
	}
	var out struct {
		Entries    []map[string]any `json:"entries"`
		Pagination map[string]any   `json:"pagination"`
	}
	_ = json.Unmarshal(raw, &out)
	if len(out.Entries) != 2 {
		t.Fatalf("got %d entries", len(out.Entries))
	}
	if int(out.Pagination["total"].(float64)) != 5 {
		t.Fatalf("total=%v", out.Pagination["total"])
	}
	if out.Pagination["has_more"] != true {
		t.Fatalf("has_more=%v", out.Pagination["has_more"])
	}
}

func TestSearchUnsupportedMode(t *testing.T) {
	base, tok, _ := testServer(t)
	// As of Phase 4, mode=reasoning is supported (helpfulness-weighted
	// re-rank). Any other mode is still 501.
	s, _ := doJSON(t, http.MethodPost, base+"/v1/search", tok,
		map[string]any{"query": "x", "mode": "frobnicate"}, nil)
	if s != 501 {
		t.Fatalf("expected 501, got %d", s)
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	base, tok, _ := testServer(t)
	s, _ := doJSON(t, http.MethodPost, base+"/v1/search", tok,
		map[string]any{"query": ""}, nil)
	if s != 400 {
		t.Fatalf("expected 400, got %d", s)
	}
}

func TestForbiddenScope(t *testing.T) {
	base, _, st := testServer(t)
	ro, _ := st.CreateToken(context.Background(), "admin", "ro",
		[]string{"read"}, nil)
	s, _ := doJSON(t, http.MethodPost, base+"/v1/projects", ro,
		map[string]any{"id": "x", "name": "X"}, nil)
	if s != 403 {
		t.Fatalf("expected 403, got %d", s)
	}
}

func TestAuditLogPopulated(t *testing.T) {
	base, tok, st := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)

	var n int
	_ = st.DB().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE method='POST'`).Scan(&n)
	if n < 2 {
		t.Fatalf("expected at least 2 audit rows, got %d", n)
	}
}

func TestHistoryRoundtrip(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var created map[string]any
	_ = json.Unmarshal(raw, &created)
	id := created["id"].(string)
	doJSON(t, http.MethodPatch, base+"/v1/entries/"+id, tok,
		map[string]any{"title": "y"}, map[string]string{"If-Match": "1"})
	s, raw := doJSON(t, http.MethodGet, base+"/v1/entries/"+id+"/history", tok, nil, nil)
	if s != 200 {
		t.Fatalf("history: %d", s)
	}
	var out struct {
		History []map[string]any `json:"history"`
	}
	_ = json.Unmarshal(raw, &out)
	if len(out.History) != 2 {
		t.Fatalf("history len=%d", len(out.History))
	}
}

func mustCreateProject(t *testing.T, base, tok, id string) {
	t.Helper()
	s, raw := doJSON(t, http.MethodPost, base+"/v1/projects", tok,
		map[string]any{"id": id, "name": id}, nil)
	if s != 201 {
		t.Fatalf("create project: %d %s", s, raw)
	}
}

// satisfy fmt import even when test set is trimmed
var _ = fmt.Sprintf
