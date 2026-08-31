package dashboard

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
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
	// Issue #137: the owner written into the runtime's trust row is the
	// saving user's kb user id — the same identity the gateway stamps on
	// their messages.
	if p.OwnerID != "alice" {
		t.Fatalf("owner id: %q, want the saving user's kb user id", p.OwnerID)
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

// postMultipart posts a multipart form with optional file field.
func postMultipart(t *testing.T, srv *httptest.Server, path, token string,
	fields map[string]string, fileField, fileName string, fileData []byte) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if fileField != "" {
		fw, err := mw.CreateFormFile(fileField, fileName)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(fileData)
	}
	mw.Close()
	u := srv.URL + path
	if token != "" {
		u += "?token=" + url.QueryEscape(token)
	}
	req, err := http.NewRequest(http.MethodPost, u, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
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

// tinyPNG is a valid 1x1 PNG — http.DetectContentType sniffs it as
// image/png.
var tinyPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0, 0, 0, 0x0d, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1,
	8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89,
	0, 0, 0, 0x0a, 'I', 'D', 'A', 'T',
	0x78, 0x9c, 0x63, 0, 1, 0, 0, 5, 0, 1, 0x0d, 0x0a, 0x2d, 0xb4,
	0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}

// Icon lifecycle (#85): emoji text persists, an uploaded image wins over
// it, its serving route is owner/admin-only, and the clear checkbox
// falls back to the text icon.
func TestMyLibrarianIcon(t *testing.T) {
	fake := &fakeProvisioner{}
	srv, s, tok := mountLibrarian(t, fake)
	ctx := context.Background()

	// Emoji text saves and round-trips.
	if code, _ := postFormBody(t, srv, "/my/librarian", tok,
		map[string]string{"name": "きりん", "icon": "🦒"}); code != http.StatusSeeOther {
		t.Fatalf("save with icon: want 303, got %d", code)
	}
	ul, err := s.GetUserLibrarian(ctx, "alice")
	if err != nil || ul.Icon != "🦒" {
		t.Fatalf("icon round-trip: %+v err=%v", ul, err)
	}
	if ul.IconMime != "" || ul.IconText() != "🦒" {
		t.Fatalf("no image yet: mime=%q text=%q", ul.IconMime, ul.IconText())
	}
	// URL building (dashboard-side, C1: query-token sessions must get a
	// tokened <img> URL or the browser's fetch arrives unauthenticated).
	if got := (pageCtx{}).LibrarianIconURL(ul); got != "" {
		t.Fatalf("no image → no URL, got %q", got)
	}

	// Rejects: over-long text icon, non-image upload.
	if code, _ := postFormBody(t, srv, "/my/librarian", tok,
		map[string]string{"name": "きりん", "icon": strings.Repeat("あ", 9)}); code != http.StatusBadRequest {
		t.Fatalf("long icon: want 400, got %d", code)
	}
	if code, _ := postMultipart(t, srv, "/my/librarian", tok,
		map[string]string{"name": "きりん"}, "icon_file", "x.txt", []byte("not an image")); code != http.StatusBadRequest {
		t.Fatalf("non-image upload: want 400, got %d", code)
	}

	// A real PNG uploads, wins over the emoji, and serves to the owner.
	if code, _ := postMultipart(t, srv, "/my/librarian", tok,
		map[string]string{"name": "きりん", "icon": "🦒"}, "icon_file", "icon.png", tinyPNG); code != http.StatusSeeOther {
		t.Fatalf("png upload: want 303, got %d", code)
	}
	ul, _ = s.GetUserLibrarian(ctx, "alice")
	if ul.IconMime != "image/png" {
		t.Fatalf("image not stored: %+v", ul)
	}
	if got := (pageCtx{Token: "sec ret"}).LibrarianIconURL(ul); !strings.Contains(got, "/librarian-icon/alice?v=") || !strings.Contains(got, "&token=sec+ret") {
		t.Fatalf("icon URL must carry version and escaped token: %q", got)
	}
	get := func(token string) int {
		u := srv.URL + "/librarian-icon/alice"
		if token != "" {
			u += "?token=" + url.QueryEscape(token)
		}
		resp, err := http.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := get(tok); code != http.StatusOK {
		t.Fatalf("owner fetch: want 200, got %d", code)
	}

	// Another (non-admin) user gets the uniform 404; anonymous too.
	if err := s.CreateUser(ctx, &store.User{ID: "bob", Name: "Bob", Email: "bob@x.com"}); err != nil {
		t.Fatal(err)
	}
	bobTok, err := s.CreateToken(ctx, "bob", "t", []string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if code := get(bobTok); code != http.StatusNotFound {
		t.Fatalf("other user fetch: want 404, got %d", code)
	}
	// Anonymous: the auth middleware answers before the handler (401 or
	// a login redirect) — anything but the image is fine.
	if code := get(""); code != http.StatusNotFound && code != http.StatusFound && code != http.StatusUnauthorized {
		t.Fatalf("anonymous fetch: want 401/404/redirect, got %d", code)
	}

	// Clear falls back to the emoji.
	if code, _ := postMultipart(t, srv, "/my/librarian", tok,
		map[string]string{"name": "きりん", "icon": "🦒", "icon_image_clear": "1"}, "", "", nil); code != http.StatusSeeOther {
		t.Fatalf("clear: want 303, got %d", code)
	}
	ul, _ = s.GetUserLibrarian(ctx, "alice")
	if ul.IconMime != "" || ul.IconText() != "🦒" {
		t.Fatalf("clear failed: %+v", ul)
	}
}

// TestMyLibrarianSaveTouchesOnlyOwnConfig (issue #103): the operated
// agent is ALWAYS the session user's own — user_id comes from the auth
// context and the agent id is derived server-side — so one user's save
// can neither read nor overwrite another user's librarian config, even
// when the form smuggles user_id / agent_id fields.
func TestMyLibrarianSaveTouchesOnlyOwnConfig(t *testing.T) {
	fake := &fakeProvisioner{}
	srv, s, aliceTok := mountLibrarian(t, fake)
	ctx := context.Background()

	// Victim: bob already has a configured librarian.
	if err := s.CreateUser(ctx, &store.User{ID: "bob", Name: "Bob", Email: "bob@x.com"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertUserLibrarian(ctx, &store.UserLibrarian{
		UserID: "bob", AgentID: "plib-bob",
		Name: "bobs librarian", Persona: "bob persona", Status: "active",
	}); err != nil {
		t.Fatal(err)
	}

	// Attacker: alice saves, smuggling bob's identifiers into the form.
	code, _ := postFormBody(t, srv, "/my/librarian", aliceTok, map[string]string{
		"name": "alice librarian", "persona": "alice persona",
		"user_id": "bob", "agent_id": "plib-bob",
	})
	if code != http.StatusSeeOther {
		t.Fatalf("save: want 303, got %d", code)
	}
	if len(fake.calls) != 1 || fake.calls[0].AgentID != "plib-alice" {
		t.Fatalf("provision must target the session user's agent, got %+v", fake.calls)
	}

	// Bob's config is untouched; alice got her own row.
	bob, err := s.GetUserLibrarian(ctx, "bob")
	if err != nil || bob.Name != "bobs librarian" || bob.Persona != "bob persona" ||
		bob.AgentID != "plib-bob" {
		t.Fatalf("victim config changed: %+v err=%v", bob, err)
	}
	alice, err := s.GetUserLibrarian(ctx, "alice")
	if err != nil || alice.AgentID != "plib-alice" || alice.Name != "alice librarian" {
		t.Fatalf("attacker's own row wrong: %+v err=%v", alice, err)
	}
}

// Issue #137: the librarian's owner follows the user who saved it. Two
// different users saving through the SAME deployment must each be
// provisioned as the owner of their own librarian — the defect this
// pins is a single deployment-wide owner id, where whoever saved
// second still got the configured operator as owner and lost the
// owner-gated tools on their own librarian.
func TestMyLibrarianOwnerFollowsSavingUser(t *testing.T) {
	fake := &fakeProvisioner{}
	srv, s, aliceTok := mountLibrarian(t, fake) // alice: admin
	ctx := context.Background()

	// A second, ordinary member — the case the deployment-wide owner id
	// got wrong.
	if err := s.CreateUser(ctx, &store.User{
		ID: "u-0f784100", Name: "Bob", Role: "member", Email: "bob@x.com",
	}); err != nil {
		t.Fatal(err)
	}
	bobTok, err := s.CreateToken(ctx, "u-0f784100", "test", []string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ token, wantOwner, name string }{
		{aliceTok, "alice", "しおり"},
		{bobTok, "u-0f784100", "ボブの司書"},
	} {
		if code, _ := postFormBody(t, srv, "/my/librarian", tc.token,
			map[string]string{"name": tc.name, "persona": "p"}); code != http.StatusSeeOther {
			t.Fatalf("save as %s: want 303, got %d", tc.wantOwner, code)
		}
	}
	if len(fake.calls) != 2 {
		t.Fatalf("provision calls: %d", len(fake.calls))
	}
	for i, want := range []string{"alice", "u-0f784100"} {
		if got := fake.calls[i].OwnerID; got != want {
			t.Fatalf("provision %d owner: %q, want %q (owner must be per user, not a deployment constant)", i, got, want)
		}
		if wantAgent := "plib-" + want; fake.calls[i].AgentID != wantAgent {
			t.Fatalf("provision %d agent: %q, want %q", i, fake.calls[i].AgentID, wantAgent)
		}
	}
	if fake.calls[0].OwnerID == fake.calls[1].OwnerID {
		t.Fatal("both librarians got the same owner — the per-user contract is broken")
	}
}

// #126: the header nav names the /talk entry point after the viewer's own
// ACTIVE librarian, read straight from user_librarians. This is display,
// not provisioning, so it must NOT be gated on h.Librarian (the ability to
// create a librarian on the opencrab runtime). This pins the production
// defect where removing OPENCRAB_URL dropped an intact librarian row's
// name and icon back to the default responder in the header.
func TestNavLibrarianShownWithoutRuntime(t *testing.T) {
	srv, st, tok := mountAuthed(t) // alice; h.Librarian stays nil (runtime off)
	ctx := context.Background()

	// An active personal librarian with a name and an uploaded icon.
	if err := st.UpsertUserLibrarian(ctx, &store.UserLibrarian{
		UserID: "alice", AgentID: "plib-alice", Name: "きりん", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserLibrarianIconImage(ctx, "alice", tinyPNG, "image/png"); err != nil {
		t.Fatal(err)
	}

	code, body := get(t, srv, "/", tok)
	bs := string(body)
	if code != 200 {
		t.Fatalf("GET /: code=%d", code)
	}
	// Name shows in the nav, not the default responder fallback.
	if !strings.Contains(bs, "きりん</a>") {
		t.Fatalf("nav should show the personal librarian name with the runtime off:\n%s", bs)
	}
	if strings.Contains(bs, "🎩 "+talkAgentName()) {
		t.Fatal("nav fell back to the default responder despite an active librarian row")
	}
	// The uploaded icon renders (versioned <img>), not the text glyph.
	if !strings.Contains(bs, `class="nav-libicon"`) ||
		!strings.Contains(bs, "/librarian-icon/alice?v=") {
		t.Fatalf("nav should render the librarian's uploaded icon with the runtime off:\n%s", bs)
	}
}

// The fallback path stays intact: a viewer with NO active librarian row
// gets the default responder name in the nav even though the runtime is
// off — the decoupling adds the librarian, it doesn't remove the default.
func TestNavLibrarianFallsBackWithoutRow(t *testing.T) {
	srv, _, tok := mountAuthed(t) // alice; no user_librarians row, runtime off
	code, body := get(t, srv, "/", tok)
	bs := string(body)
	if code != 200 {
		t.Fatalf("GET /: code=%d", code)
	}
	if !strings.Contains(bs, "🎩 "+talkAgentName()) {
		t.Fatalf("nav should show the default responder when there is no librarian row:\n%s", bs)
	}
	if strings.Contains(bs, `class="nav-libicon"`) {
		t.Fatal("nav rendered a librarian icon with no librarian row")
	}
}
