package dashboard

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kojira/omoikane/internal/store"
)

func newDashStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedEntries puts one project + one entry into the store so home/project/
// entry/history pages have content to render.
func seedEntries(t *testing.T, s *store.Store) string {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateProject(ctx, &store.Project{ID: "p", Name: "P", Description: "demo"}); err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateEntry(ctx, &store.Entry{
		ProjectID: "p", Type: "trap",
		Title:               "Mask trap",
		Body:                "rect mask leaks",
		Symptom:             "rectangular artifact",
		RootCause:           "train vs inference mismatch",
		Resolution:          "use landmark mask everywhere",
		Prohibited:          "no cv2.rectangle on target",
		AttemptedApproaches: "tried fp16",
		ObservedBehavior:    "NaN at step 5000",
		Hypotheses:          "attention precision",
		Scope:               `{"frameworks":["pytorch"]}`,
		Metadata:            `{"x":1}`,
		Tags:                []string{"mask", "preprocessing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// PATCH to add a second history version so the history page has 2 rows
	title := "Mask trap v2"
	if _, _, err := s.UpdateEntry(ctx, id, store.EntryPatch{
		Title:           &title,
		ExpectedVersion: 1,
		ChangedBy:       "tester",
		ChangedByRole:   "human",
		ChangeSummary:   "rename",
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func mount(t *testing.T, s *store.Store, open bool) *httptest.Server {
	t.Helper()
	h, err := New(s, open)
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, srv *httptest.Server, path, token string) (int, []byte) {
	t.Helper()
	u := srv.URL + path
	if token != "" {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		u += sep + "token=" + url.QueryEscape(token)
	}
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func issueDashToken(t *testing.T, s *store.Store, scopes []string) string {
	t.Helper()
	if err := s.CreateUser(context.Background(),
		&store.User{ID: "u", Name: "u", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	tok, err := s.CreateToken(context.Background(), "u", "t", scopes, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// brokenFS is an fs.FS that always returns an error from Open. Used to
// exercise the ParseFS error branch in newFromFS.
type brokenFS struct{}

func (brokenFS) Open(string) (fs.File, error) {
	return nil, errBrokenFS
}

var errBrokenFS = io.EOF

func TestNewReturnsErrorOnBrokenFS(t *testing.T) {
	if _, err := newFromFS(newDashStore(t), true, brokenFS{}); err == nil {
		t.Fatal("expected ParseFS error")
	}
}

func TestNewParsesEveryPage(t *testing.T) {
	h, err := New(newDashStore(t), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"home", "project", "entry", "entry_history", "search"} {
		if _, ok := h.pages[name]; !ok {
			t.Errorf("missing page template: %s", name)
		}
	}
}

func TestHomeRendersBothProjectAndEntryTables(t *testing.T) {
	s := newDashStore(t)
	seedEntries(t, s)
	srv := mount(t, s, true)
	code, body := get(t, srv, "/", "")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	for _, want := range []string{"Projects", "Recent entries", "Mask trap v2", "<span class=\"badge\">p</span>"} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("missing %q in body", want)
		}
	}
}

func TestHomeEmptyState(t *testing.T) {
	srv := mount(t, newDashStore(t), true)
	_, body := get(t, srv, "/", "")
	if !bytes.Contains(body, []byte("No projects yet")) {
		t.Fatalf("expected empty-state for projects: %s", body)
	}
	if !bytes.Contains(body, []byte("No entries yet")) {
		t.Fatalf("expected empty-state for entries: %s", body)
	}
}

func TestProjectPage(t *testing.T) {
	s := newDashStore(t)
	seedEntries(t, s)
	srv := mount(t, s, true)
	code, body := get(t, srv, "/projects/p", "")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	if !bytes.Contains(body, []byte("Mask trap v2")) {
		t.Fatalf("entries not on project page: %s", body)
	}
}

func TestProjectPageNotFound(t *testing.T) {
	srv := mount(t, newDashStore(t), true)
	code, _ := get(t, srv, "/projects/missing", "")
	if code != 404 {
		t.Fatalf("status=%d", code)
	}
}

func TestProjectPageEmpty(t *testing.T) {
	s := newDashStore(t)
	_ = s.CreateProject(context.Background(),
		&store.Project{ID: "empty", Name: "Empty"})
	srv := mount(t, s, true)
	_, body := get(t, srv, "/projects/empty", "")
	if !bytes.Contains(body, []byte("No entries in this project yet")) {
		t.Fatalf("expected empty-state, got %s", body)
	}
}

func TestEntryPageRendersAllFields(t *testing.T) {
	s := newDashStore(t)
	id := seedEntries(t, s)
	srv := mount(t, s, true)
	code, body := get(t, srv, "/entries/"+id, "")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	for _, want := range []string{"Symptom", "Root cause", "Resolution",
		"Prohibited", "Attempted approaches", "Observed behavior",
		"Hypotheses", "Scope", "Metadata"} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("missing field %q", want)
		}
	}
}

func TestEntryPageNotFound(t *testing.T) {
	srv := mount(t, newDashStore(t), true)
	code, _ := get(t, srv, "/entries/T-MISSING", "")
	if code != 404 {
		t.Fatalf("status=%d", code)
	}
}

func TestEntryAsOfWithBadFormat(t *testing.T) {
	s := newDashStore(t)
	id := seedEntries(t, s)
	srv := mount(t, s, true)
	code, _ := get(t, srv, "/entries/"+id+"?as_of=not-a-time", "")
	if code != 400 {
		t.Fatalf("status=%d", code)
	}
}

func TestEntryAsOfHistoricalSnapshot(t *testing.T) {
	s := newDashStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, &store.Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	id, _ := s.CreateEntry(ctx, &store.Entry{
		ProjectID: "p", Type: "trap", Title: "v1", Body: "b",
	})
	pivot := time.Now().UTC()
	time.Sleep(20 * time.Millisecond)
	title := "v2"
	if _, _, err := s.UpdateEntry(ctx, id, store.EntryPatch{
		Title: &title, ExpectedVersion: 1, ChangedBy: "t",
	}); err != nil {
		t.Fatal(err)
	}

	srv := mount(t, s, true)
	code, body := get(t, srv, "/entries/"+id+"?as_of="+pivot.Format(time.RFC3339Nano), "")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	if !bytes.Contains(body, []byte("Showing snapshot as of")) {
		t.Fatal("expected as_of banner")
	}
	if !bytes.Contains(body, []byte(">v1<")) {
		t.Fatalf("expected v1 title: %s", body)
	}
}

func TestHistoryPage(t *testing.T) {
	s := newDashStore(t)
	id := seedEntries(t, s)
	srv := mount(t, s, true)
	code, body := get(t, srv, "/entries/"+id+"/history", "")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	// 2 history rows from seedEntries — count <tr> within <tbody>.
	// The template emits `<td>{{.Version}}</td>` so we count "<tr>".
	if got := bytes.Count(body, []byte("<tr>")); got < 3 { // header + 2 rows
		t.Fatalf("expected >=3 <tr> tags (1 thead + 2 rows), got %d in:\n%s", got, body)
	}
	if !bytes.Contains(body, []byte("initial create")) {
		t.Fatalf("expected initial-create row")
	}
	if !bytes.Contains(body, []byte("rename")) {
		t.Fatalf("expected v2 row with summary 'rename'")
	}
}

func TestHistoryPageNotFound(t *testing.T) {
	srv := mount(t, newDashStore(t), true)
	code, _ := get(t, srv, "/entries/T-MISSING/history", "")
	if code != 404 {
		t.Fatalf("status=%d", code)
	}
}

func TestSearchPageWithResults(t *testing.T) {
	s := newDashStore(t)
	seedEntries(t, s)
	srv := mount(t, s, true)
	code, body := get(t, srv, "/search?q=mask", "")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	if !bytes.Contains(body, []byte("Mask trap v2")) {
		t.Fatalf("expected hit, got %s", body)
	}
}

func TestSearchPageEmptyQuery(t *testing.T) {
	srv := mount(t, newDashStore(t), true)
	_, body := get(t, srv, "/search", "")
	if !bytes.Contains(body, []byte("Type a query")) {
		t.Fatalf("expected empty-state: %s", body)
	}
}

func TestSearchPageNoResults(t *testing.T) {
	srv := mount(t, newDashStore(t), true)
	_, body := get(t, srv, "/search?q=nothingmatches", "")
	if !bytes.Contains(body, []byte("No matches")) {
		t.Fatalf("expected no-matches: %s", body)
	}
}

func TestCSSServed(t *testing.T) {
	srv := mount(t, newDashStore(t), true)
	code, body := get(t, srv, "/static/style.css", "")
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	if !bytes.Contains(body, []byte(":root")) {
		t.Fatal("expected CSS body")
	}
}

func TestRenderUnknownPage(t *testing.T) {
	h, _ := New(newDashStore(t), true)
	rec := httptest.NewRecorder()
	h.render(rec, "no-such-page", nil)
	if rec.Code != 500 {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestRenderTemplateError(t *testing.T) {
	// renderCtx is just a struct return; we test render's error branch by
	// passing a data type the template can't index.
	h, _ := New(newDashStore(t), true)
	rec := httptest.NewRecorder()
	h.render(rec, "home", "not a struct") // home template expects pageCtx
	body := rec.Body.String()
	if !strings.Contains(body, "template error") {
		t.Fatalf("expected template-error fallback, got %q", body)
	}
}

func TestTrunc(t *testing.T) {
	if trunc("short", 10) != "short" {
		t.Error("short string should pass through")
	}
	if got := trunc("0123456789ABCDE", 5); got != "01234…" {
		t.Errorf("trunc: %q", got)
	}
}

func TestPrepareFTSQuery(t *testing.T) {
	cases := map[string]string{
		"":                   "",
		" ":                  "",
		"mask preprocessing": `"mask"* "preprocessing"*`,
		// double quotes are FTS5 separators in our split set, so
		// `bad"value` splits into two tokens.
		`bad"value`: `"bad"* "value"*`,
	}
	for in, want := range cases {
		if got := prepareFTSQuery(in); got != want {
			t.Errorf("%q: want %q got %q", in, want, got)
		}
	}
}

// ---- auth-gated paths ----

func TestAuthRequiredWhenOpenFalse(t *testing.T) {
	s := newDashStore(t)
	srv := mount(t, s, false)
	code, _ := get(t, srv, "/", "")
	if code != 401 {
		t.Fatalf("status=%d", code)
	}
}

func TestAuthAcceptedWhenOpenFalse(t *testing.T) {
	s := newDashStore(t)
	tok := issueDashToken(t, s, []string{"read"})
	seedEntries(t, s)
	srv := mount(t, s, false)
	code, body := get(t, srv, "/", tok)
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	if !bytes.Contains(body, []byte("Mask trap v2")) {
		t.Fatalf("not authenticated content: %s", body)
	}
}

// ---- failing-store branches ----

// failingStore is a tiny wrapper around a real store whose connection is
// closed so every query returns an error. Used to hit the 500-error
// branches in dashboard handlers.
func failingStore(t *testing.T) *store.Store {
	t.Helper()
	s := newDashStore(t)
	_ = s.Close()
	return s
}

func TestHomeStoreFailure(t *testing.T) {
	srv := mount(t, failingStore(t), true)
	code, _ := get(t, srv, "/", "")
	if code != 500 {
		t.Fatalf("status=%d", code)
	}
}

func TestProjectStoreFailure(t *testing.T) {
	// Force a non-NotFound path by closing the store after creating the
	// project but before serving — easiest is just to call /projects/p on
	// a closed store and verify the 500 branch in the handler.
	srv := mount(t, failingStore(t), true)
	code, _ := get(t, srv, "/projects/p", "")
	if code != 500 {
		t.Fatalf("status=%d", code)
	}
}

func TestEntryStoreFailure(t *testing.T) {
	srv := mount(t, failingStore(t), true)
	code, _ := get(t, srv, "/entries/T-XYZ", "")
	if code != 500 {
		t.Fatalf("status=%d", code)
	}
}

func TestHistoryStoreFailure(t *testing.T) {
	srv := mount(t, failingStore(t), true)
	code, _ := get(t, srv, "/entries/T-XYZ/history", "")
	if code != 500 {
		t.Fatalf("status=%d", code)
	}
}

func TestSearchStoreFailureSwallowed(t *testing.T) {
	// SearchFTS on a closed store returns a non-ErrInvalidInput error,
	// which the handler maps to 500.
	srv := mount(t, failingStore(t), true)
	code, _ := get(t, srv, "/search?q=mask", "")
	if code != 500 {
		t.Fatalf("status=%d", code)
	}
}

func TestProjectListEntriesFailure(t *testing.T) {
	// A project exists but listing entries fails because the store is
	// closed between operations. We seed first, then close.
	s := newDashStore(t)
	_ = s.CreateProject(context.Background(),
		&store.Project{ID: "p", Name: "P"})
	srv := mount(t, s, true)
	// First request: succeeds.
	if code, _ := get(t, srv, "/projects/p", ""); code != 200 {
		t.Fatalf("seed: %d", code)
	}
	_ = s.Close()
	code, _ := get(t, srv, "/projects/p", "")
	if code != 500 {
		t.Fatalf("status=%d", code)
	}
}

func TestEntryHistoryAsOfNotFound(t *testing.T) {
	// asOf far in the past on a real entry should 404 via GetEntryAsOf.
	s := newDashStore(t)
	id := seedEntries(t, s)
	srv := mount(t, s, true)
	code, _ := get(t, srv, "/entries/"+id+"?as_of=1990-01-01T00:00:00Z", "")
	if code != 404 {
		t.Fatalf("status=%d", code)
	}
}

// dropEntriesTable removes the entries table so ListEntries fails but
// ListProjects (different table) still works. Hits the
// list-projects-success / list-entries-fail branch in home and project.
func dropEntriesTable(t *testing.T, s *store.Store) {
	t.Helper()
	if _, err := s.DB().Exec(`DROP TABLE entries`); err != nil {
		t.Fatal(err)
	}
}

func TestHomeListEntriesFailureBranch(t *testing.T) {
	s := newDashStore(t)
	_ = s.CreateProject(context.Background(),
		&store.Project{ID: "p", Name: "P"})
	dropEntriesTable(t, s)
	srv := mount(t, s, true)
	code, _ := get(t, srv, "/", "")
	if code != 500 {
		t.Fatalf("status=%d", code)
	}
}

func TestProjectListEntriesFailureBranch(t *testing.T) {
	s := newDashStore(t)
	_ = s.CreateProject(context.Background(),
		&store.Project{ID: "p", Name: "P"})
	dropEntriesTable(t, s)
	srv := mount(t, s, true)
	code, _ := get(t, srv, "/projects/p", "")
	if code != 500 {
		t.Fatalf("status=%d", code)
	}
}

func TestHistoryHandlerWhenCurrentMissingButHistoryFound(t *testing.T) {
	// We seed an entry, then drop the entries row directly so EntryHistory
	// no longer finds the entry. This exercises the early-404 path.
	s := newDashStore(t)
	id := seedEntries(t, s)
	if _, err := s.DB().Exec(`DELETE FROM entries WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	srv := mount(t, s, true)
	code, _ := get(t, srv, "/entries/"+id+"/history", "")
	if code != 404 {
		t.Fatalf("status=%d", code)
	}
}
