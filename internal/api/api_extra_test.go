package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestListAndGetProjects(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "a")
	mustCreateProject(t, base, tok, "b")

	s, raw := doJSON(t, http.MethodGet, base+"/v1/projects", tok, nil, nil)
	if s != 200 {
		t.Fatalf("list: %d", s)
	}
	if !bytes.Contains(raw, []byte(`"id":"a"`)) || !bytes.Contains(raw, []byte(`"id":"b"`)) {
		t.Fatalf("missing entries in list: %s", raw)
	}

	s, _ = doJSON(t, http.MethodGet, base+"/v1/projects/a", tok, nil, nil)
	if s != 200 {
		t.Fatalf("get: %d", s)
	}
	s, _ = doJSON(t, http.MethodGet, base+"/v1/projects/missing", tok, nil, nil)
	if s != 404 {
		t.Fatalf("get missing: %d", s)
	}
}

func TestRecovererTurnsPanicInto500(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := chi.NewRouter()
	r.Use(Recoverer(logger))
	r.Get("/boom", func(http.ResponseWriter, *http.Request) {
		panic("nope")
	})
	srv := httptest.NewServer(r)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/boom")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 500 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestLimitBody(t *testing.T) {
	r := chi.NewRouter()
	r.Use(LimitBody(10))
	r.Post("/x", func(w http.ResponseWriter, req *http.Request) {
		_, err := io.ReadAll(req.Body)
		if err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/x", "text/plain", bytes.NewReader([]byte("0123456789ABCDEF")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 413 {
		t.Fatalf("expected 413, got %d", resp.StatusCode)
	}
}

func TestAccessLogPassthrough(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := chi.NewRouter()
	r.Use(AccessLog(logger))
	r.Get("/ok", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("hi"))
	})
	srv := httptest.NewServer(r)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/ok")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestRequestIDFrom(t *testing.T) {
	if got := RequestIDFrom(context.Background()); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestBadJSON(t *testing.T) {
	base, tok, _ := testServer(t)
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/projects", bytes.NewReader([]byte("{not json")))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestInvalidType(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	s, _ := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "bogus", "title": "x", "body": "y",
	}, nil)
	if s != 400 {
		t.Fatalf("expected 400, got %d", s)
	}
}

func TestInvalidAsOf(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	_, raw := doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap", "title": "x", "body": "y",
	}, nil)
	var c map[string]any
	_ = mustUnmarshal(raw, &c, t)
	id := c["id"].(string)
	s, _ := doJSON(t, http.MethodGet, base+"/v1/entries/"+id+"?as_of=not-a-time", tok, nil, nil)
	if s != 400 {
		t.Fatalf("expected 400, got %d", s)
	}
}

func TestSearchHitsAndFilters(t *testing.T) {
	base, tok, _ := testServer(t)
	mustCreateProject(t, base, tok, "kb")
	doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "trap",
		"title": "mask", "body": "rect mask leaks",
	}, nil)
	doJSON(t, http.MethodPost, base+"/v1/entries", tok, map[string]any{
		"project_id": "kb", "type": "decision",
		"title": "Adopt SyncNet", "body": "lipsync evaluation",
	}, nil)

	s, raw := doJSON(t, http.MethodPost, base+"/v1/search", tok, map[string]any{
		"query": `"mask"*`,
		"filters": map[string]any{"type": "trap"},
	}, nil)
	if s != 200 {
		t.Fatalf("search: %d %s", s, raw)
	}
}

func mustUnmarshal(raw []byte, into any, t *testing.T) error {
	t.Helper()
	return jsonUnmarshal(raw, into)
}
