package dashboard

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/opencrab"
	"github.com/zenryokukikai/omoikane/internal/store"
)

// fakeProvisioner records Provision calls and returns a scripted error.
type fakeProvisioner struct {
	calls []opencrab.ProvisionParams
	err   error
}

func (f *fakeProvisioner) Provision(_ context.Context, p opencrab.ProvisionParams) error {
	f.calls = append(f.calls, p)
	return f.err
}

// mountLibrarian is mountAuthed + an injected provisioner (feature on).
func mountLibrarian(t *testing.T, fake *fakeProvisioner) (*httptest.Server, *store.Store, string) {
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
	h.Librarian = fake
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, s, tok
}

// postFormBody is postForm + response body (for banner assertions).
func postFormBody(t *testing.T, srv *httptest.Server, path, token string, fields map[string]string) (int, string) {
	t.Helper()
	form := url.Values{}
	for k, v := range fields {
		form.Set(k, v)
	}
	u := srv.URL + path
	if token != "" {
		u += "?token=" + url.QueryEscape(token)
	}
	req, err := http.NewRequest(http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// Feature off (no provisioner wired): the page does not exist at all,
// and the header carries no link to it.
func TestMyLibrarianDisabled(t *testing.T) {
	srv, _, tok := mountAuthed(t) // h.Librarian stays nil
	if code, _ := get(t, srv, "/my/librarian", tok); code != 404 {
		t.Fatalf("GET disabled: want 404, got %d", code)
	}
	if code, _ := postFormBody(t, srv, "/my/librarian", tok,
		map[string]string{"name": "x"}); code != 404 {
		t.Fatalf("POST disabled: want 404, got %d", code)
	}
	_, home := get(t, srv, "/", tok)
	if strings.Contains(string(home), "個人司書") {
		t.Fatal("header must not link /my/librarian when disabled")
	}
}

func TestMyLibrarianPageAndHeaderLink(t *testing.T) {
	srv, _, tok := mountLibrarian(t, &fakeProvisioner{})
	code, body := get(t, srv, "/my/librarian", tok)
	if code != 200 {
		t.Fatalf("GET: want 200, got %d", code)
	}
	bs := string(body)
	if !strings.Contains(bs, "司書を作る") || !strings.Contains(bs, `name="persona"`) {
		t.Fatalf("form not rendered:\n%s", bs)
	}
	_, home := get(t, srv, "/", tok)
	if !strings.Contains(string(home), "🤖 個人司書") {
		t.Fatal("header link missing when enabled")
	}
}

// Save → provision → row; the first save mints a token, the second
// reuses it (KBToken empty on re-provision).
func TestMyLibrarianSaveAndTokenIdempotency(t *testing.T) {
	fake := &fakeProvisioner{}
	srv, st, tok := mountLibrarian(t, fake)
	ctx := context.Background()

	code, _ := postFormBody(t, srv, "/my/librarian", tok,
		map[string]string{"name": "しおり", "persona": "丁寧で簡潔。"})
	if code != http.StatusSeeOther {
		t.Fatalf("save: want 303, got %d", code)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("provision calls: %d", len(fake.calls))
	}
	p := fake.calls[0]
	if p.AgentID != "plib-alice" || p.Name != "しおり" || p.Persona != "丁寧で簡潔。" ||
		p.UserName != "Alice" {
		t.Fatalf("params: %+v", p)
	}
	if p.KBToken == "" {
		t.Fatal("first save must mint a kb token")
	}
	ul, err := st.GetUserLibrarian(ctx, "alice")
	if err != nil || ul.Name != "しおり" || ul.AgentID != "plib-alice" {
		t.Fatalf("row: %+v err=%v", ul, err)
	}
	if has, _ := st.HasAPIToken(ctx, "alice", "personal-librarian"); !has {
		t.Fatal("token row should exist after save")
	}

	// Second save: no new token, existing config updated.
	code, _ = postFormBody(t, srv, "/my/librarian", tok,
		map[string]string{"name": "新しおり", "persona": "元気"})
	if code != http.StatusSeeOther {
		t.Fatalf("re-save: want 303, got %d", code)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("provision calls: %d", len(fake.calls))
	}
	if fake.calls[1].KBToken != "" {
		t.Fatal("re-save must NOT mint a second token")
	}
	ul, _ = st.GetUserLibrarian(ctx, "alice")
	if ul.Name != "新しおり" || ul.Persona != "元気" {
		t.Fatalf("updated row: %+v", ul)
	}

	// The settings page reflects the saved config + success banner.
	_, body := get(t, srv, "/my/librarian?saved=1", tok)
	bs := string(body)
	if !strings.Contains(bs, "新しおり") || !strings.Contains(bs, "保存しました") {
		t.Fatalf("page after save:\n%s", bs)
	}
}

// Provision failure: error banner, no row, and the freshly minted token
// is revoked so the next save mints (and ships) a new one.
func TestMyLibrarianProvisionFailure(t *testing.T) {
	fake := &fakeProvisioner{err: errors.New("workspace write (PUT .kb.curlrc): HTTP 502")}
	srv, st, tok := mountLibrarian(t, fake)
	ctx := context.Background()

	code, body := postFormBody(t, srv, "/my/librarian", tok,
		map[string]string{"name": "しおり", "persona": "p"})
	if code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", code)
	}
	if !strings.Contains(body, "敷設に失敗しました") || !strings.Contains(body, "PUT .kb.curlrc") {
		t.Fatalf("error banner missing:\n%s", body)
	}
	// Typed input is echoed back.
	if !strings.Contains(body, "しおり") {
		t.Fatal("form echo missing")
	}
	if _, err := st.GetUserLibrarian(ctx, "alice"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("row must not be saved on failure, got %v", err)
	}
	if has, _ := st.HasAPIToken(ctx, "alice", "personal-librarian"); has {
		t.Fatal("token must be revoked when provisioning fails")
	}

	// Recovery: the runtime comes back → save succeeds and mints again.
	fake.err = nil
	if code, _ := postFormBody(t, srv, "/my/librarian", tok,
		map[string]string{"name": "しおり"}); code != http.StatusSeeOther {
		t.Fatalf("recovery save: want 303, got %d", code)
	}
	if fake.calls[len(fake.calls)-1].KBToken == "" {
		t.Fatal("recovery save must mint a fresh token")
	}
}

func TestMyLibrarianValidation(t *testing.T) {
	fake := &fakeProvisioner{}
	srv, _, tok := mountLibrarian(t, fake)

	for name, fields := range map[string]map[string]string{
		"empty name":   {"name": "   "},
		"long name":    {"name": strings.Repeat("あ", 51)},
		"long persona": {"name": "ok", "persona": strings.Repeat("あ", 2001)},
	} {
		code, _ := postFormBody(t, srv, "/my/librarian", tok, fields)
		if code != http.StatusBadRequest {
			t.Fatalf("%s: want 400, got %d", name, code)
		}
	}
	// Boundary values pass.
	code, _ := postFormBody(t, srv, "/my/librarian", tok, map[string]string{
		"name": strings.Repeat("あ", 50), "persona": strings.Repeat("あ", 2000)})
	if code != http.StatusSeeOther {
		t.Fatalf("boundary: want 303, got %d", code)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("validation failures must not reach the provisioner: %d", len(fake.calls))
	}
}

func TestMyLibrarianRequiresAuth(t *testing.T) {
	srv, _, _ := mountLibrarian(t, &fakeProvisioner{})
	resp, err := http.Get(srv.URL + "/my/librarian")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusFound {
		t.Fatalf("want 401/302 without auth, got %d", resp.StatusCode)
	}
}
