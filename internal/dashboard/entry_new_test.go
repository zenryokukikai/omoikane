package dashboard

// /entries/new — the human entry-creation form (issue #71).
//
// The page only renders the form; the submission is a JS fetch to the
// existing POST /v1/entries (session cookie), so these tests cover the
// RENDER contract: login gating, the note-default vocabulary, the
// space select's visibility narrowing and the ?space= preset's
// oracle-sealing 404.

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The form renders for a signed-in member: note is the default type,
// the project field defaults to omoikane, and the submission targets
// the API write path (no dashboard POST route).
func TestEntryNewPageRenders(t *testing.T) {
	f, srv := newDashLeakFixture(t)
	code, body := get(t, srv, "/entries/new", f.memberTok)
	if code != 200 {
		t.Fatalf("member /entries/new: code=%d", code)
	}
	bs := string(body)
	for _, want := range []string{
		`<option value="note" selected>`,
		`id="ne-title"`,
		`id="ne-body"`,
		`value="omoikane"`,
		`fetch('/v1/entries'`,
	} {
		if !strings.Contains(bs, want) {
			t.Errorf("/entries/new missing %q", want)
		}
	}
	// The full human-writable vocabulary is offered; librarian output
	// types are not (humans don't hand-write librarian_meta).
	for _, ty := range []string{"trap", "decision", "design", "lesson", "incident"} {
		if !strings.Contains(bs, `<option value="`+ty+`"`) {
			t.Errorf("type select missing %q", ty)
		}
	}
	if strings.Contains(bs, `<option value="librarian_meta"`) {
		t.Errorf("type select must not offer librarian_meta")
	}
}

// Unauthenticated browsers are sent to /login (the standard dashboard
// gate) — the form is for signed-in humans only.
func TestEntryNewRequiresLogin(t *testing.T) {
	_, srv := newDashLeakFixture(t)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/entries/new", nil)
	req.Header.Set("Accept", "text/html")
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 302 to /login, got %d: %s", resp.StatusCode, b)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if loc.Path != "/login" || loc.Query().Get("next") != "/entries/new" {
		t.Errorf("Location = %q, want /login?next=/entries/new", resp.Header.Get("Location"))
	}
}

// The space select mirrors the /entries contract: members see the
// restricted space, outsiders never see its id OR its name, and a
// user-less (internal-only) view gets no select at all.
func TestEntryNewSpaceSelectVisibility(t *testing.T) {
	f, srv := newDashLeakFixture(t)

	_, body := get(t, srv, "/entries/new", f.memberTok)
	bs := string(body)
	for _, want := range []string{`id="ne-space"`, `value="` + f.spaceID + `"`, "secret-space", "個人スペース"} {
		if !strings.Contains(bs, want) {
			t.Errorf("member's space select missing %q", want)
		}
	}

	_, body = get(t, srv, "/entries/new", f.outsiderTok)
	bs = string(body)
	if !strings.Contains(bs, `id="ne-space"`) {
		t.Errorf("outsider (internal + personal = 2 spaces) should still get a select")
	}
	if strings.Contains(bs, f.spaceID) || strings.Contains(bs, "secret-space") {
		t.Errorf("restricted space leaked into a non-member's /entries/new select")
	}

	svcTok, err := f.st.CreateToken(t.Context(), "", "svc", []string{"read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	code, body := get(t, srv, "/entries/new", svcTok)
	if code != 200 {
		t.Fatalf("user-less token /entries/new: code=%d", code)
	}
	if strings.Contains(string(body), `id="ne-space"`) {
		t.Errorf("space select rendered for an internal-only view")
	}
}

// ?space=<id> presets the select (the 「ここに書く」 flow from a space
// filter view); a space outside the viewer's visibility — or a missing
// one — answers 404, indistinguishable by design.
func TestEntryNewSpacePreset(t *testing.T) {
	f, srv := newDashLeakFixture(t)

	code, body := get(t, srv, "/entries/new?space=p-u-member", f.memberTok)
	if code != 200 {
		t.Fatalf("member with ?space=: code=%d", code)
	}
	if !strings.Contains(string(body), `value="p-u-member" selected`) {
		t.Errorf("?space= did not preselect the space option")
	}

	for _, tc := range []struct{ name, space, tok string }{
		{"outsider on restricted space", f.spaceID, f.outsiderTok},
		{"member on nonexistent space", "sp-nope", f.memberTok},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _ := get(t, srv, "/entries/new?space="+tc.space, tc.tok)
			if code != 404 {
				t.Errorf("code=%d, want 404", code)
			}
		})
	}
}

// 導線: the header carries ✏️ 新規 for a signed-in viewer only, and
// the /entries list links to the form carrying the active space filter.
func TestEntryNewNavigation(t *testing.T) {
	f, srv := newDashLeakFixture(t)

	_, body := get(t, srv, "/entries", f.memberTok)
	bs := string(body)
	if !strings.Contains(bs, `href="/entries/new"`) {
		t.Errorf("header/list missing the plain /entries/new link for a signed-in member")
	}

	// Space-filtered list → the link carries the space through.
	_, body = get(t, srv, "/entries?space=p-u-member", f.memberTok)
	if !strings.Contains(string(body), `href="/entries/new?space=p-u-member"`) {
		t.Errorf("space-filtered /entries missing the このスペースに書く link")
	}

	// Open mode without a signed-in user: no Me → no ✏️ link.
	openSrv := mount(t, newDashStore(t), true)
	_, body = get(t, openSrv, "/entries", "")
	if strings.Contains(string(body), "/entries/new") {
		t.Errorf("✏️ link rendered without a signed-in user")
	}
}
