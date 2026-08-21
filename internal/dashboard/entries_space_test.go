package dashboard

// /entries space filter + space UI 導線 (issue #69).
//
// Reuses the space-leak fixture (member / outsider / admin around one
// restricted space) so the filter semantics are asserted against the
// exact same world the leak matrix guards: 視界内は絞り込み、視界外と
// 不存在は 404(存在オラクル封じ)、select は視界内スペースが2つ以上の
// ときだけ現れる。

import (
	"context"
	"strings"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// A member filtering on the restricted space sees ONLY that space's
// entries: the restricted title appears, the internal one does not.
func TestEntriesSpaceFilterMember(t *testing.T) {
	f, srv := newDashLeakFixture(t)
	code, body := get(t, srv, "/entries?space="+f.spaceID, f.memberTok)
	if code != 200 {
		t.Fatalf("member with ?space=: code=%d, want 200", code)
	}
	bs := string(body)
	if !strings.Contains(bs, "secret title") {
		t.Errorf("restricted-space entry missing from its own space filter")
	}
	if strings.Contains(bs, ">plain internal title<") {
		t.Errorf("internal entry leaked into ?space=%s (指定∩視界 violated)", f.spaceID)
	}
}

// 視界外 (non-member) と不存在の space 指定はどちらも 404 — an empty
// 200 page would reveal that the space id exists.
func TestEntriesSpaceFilter404(t *testing.T) {
	f, srv := newDashLeakFixture(t)
	for _, tc := range []struct {
		name, space, tok string
	}{
		{"outsider on restricted space", f.spaceID, f.outsiderTok},
		{"member on nonexistent space", "sp-nope", f.memberTok},
		{"admin on nonexistent space", "sp-nope", f.adminTok},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _ := get(t, srv, "/entries?space="+tc.space, tc.tok)
			if code != 404 {
				t.Errorf("code=%d, want 404", code)
			}
		})
	}
}

// The space select renders for a viewer with 2+ visible spaces, lists
// exactly the visible ones with the agreed display names, and stays
// hidden for a view with fewer than two spaces (user-less token:
// internal only).
func TestEntriesSpaceSelectVisibility(t *testing.T) {
	f, srv := newDashLeakFixture(t)

	// Member: internal + own personal + the restricted space.
	code, body := get(t, srv, "/entries", f.memberTok)
	if code != 200 {
		t.Fatalf("member /entries: code=%d", code)
	}
	bs := string(body)
	if !strings.Contains(bs, `name="space"`) {
		t.Fatalf("space select missing for a member with 3 visible spaces")
	}
	// restricted space keeps its stored name; the viewer's own personal
	// space reads 個人スペース; internal reads internal(全体).
	for _, want := range []string{
		`value="` + f.spaceID + `"`,
		"secret-space",
		"個人スペース",
		"internal(全体)",
	} {
		if !strings.Contains(bs, want) {
			t.Errorf("space select missing %q", want)
		}
	}

	// Outsider: sees internal + own personal, but NEVER the restricted
	// space as an option.
	_, body = get(t, srv, "/entries", f.outsiderTok)
	bs = string(body)
	if !strings.Contains(bs, `name="space"`) {
		t.Errorf("space select missing for outsider (internal + personal = 2 spaces)")
	}
	if strings.Contains(bs, f.spaceID) {
		t.Errorf("restricted space id offered to a non-member in the select")
	}

	// User-less token: visibility = internal only → no select.
	svcTok, err := f.st.CreateToken(context.Background(), "", "svc", []string{"read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	code, body = get(t, srv, "/entries", svcTok)
	if code != 200 {
		t.Fatalf("user-less token /entries: code=%d", code)
	}
	if strings.Contains(string(body), `name="space"`) {
		t.Errorf("space select rendered for an internal-only view")
	}
}

// The ⚙ header menu carries the 個人スペース direct link for a signed-in
// viewer — and not for an anonymous page.
func TestHeaderPersonalSpaceLink(t *testing.T) {
	f, srv := newDashLeakFixture(t)
	_, body := get(t, srv, "/entries", f.memberTok)
	if !strings.Contains(string(body), `href="/entries?space=p-u-member`) {
		t.Errorf("⚙ menu missing the personal-space link for a signed-in member")
	}

	// Open mode without a signed-in user: no Me → no link.
	openSrv := mount(t, newDashStore(t), true)
	_, body = get(t, openSrv, "/entries", "")
	if strings.Contains(string(body), "個人スペース") {
		t.Errorf("personal-space link rendered without a signed-in user")
	}
}

// The entry page shows a space badge with the space NAME for non-internal
// entries, and no badge for internal ones. The member's own personal
// space reads 個人スペース.
func TestEntrySpaceBadge(t *testing.T) {
	f, srv := newDashLeakFixture(t)

	// Restricted-space entry → badge with the stored space name.
	_, body := get(t, srv, "/entries/"+f.secretID, f.memberTok)
	bs := string(body)
	if !strings.Contains(bs, "badge-space") || !strings.Contains(bs, "secret-space") {
		t.Errorf("restricted entry page missing its space badge")
	}

	// Internal entry → no badge.
	_, body = get(t, srv, "/entries/"+f.internalID, f.memberTok)
	if strings.Contains(string(body), "badge-space") {
		t.Errorf("internal entry page must not carry a space badge")
	}

	// Personal-space entry → 個人スペース label.
	ctx := context.Background()
	pid, err := f.st.CreateEntry(ctx, &store.Entry{
		ProjectID: "p-leak", Type: "lesson", Title: "my private note",
		Body: "x", Status: "ACTIVE", SpaceID: store.PersonalSpaceID("u-member"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, body = get(t, srv, "/entries/"+pid, f.memberTok)
	if !strings.Contains(string(body), "個人スペース") {
		t.Errorf("personal-space entry page missing the 個人スペース badge")
	}
}

// The personal-space direct link actually works end to end: an entry in
// the member's personal space is listed under /entries?space=p-<self>
// and hidden from everyone else's filter (404 for the outsider).
func TestEntriesPersonalSpaceFlow(t *testing.T) {
	f, srv := newDashLeakFixture(t)
	ctx := context.Background()
	if _, err := f.st.CreateEntry(ctx, &store.Entry{
		ProjectID: "p-leak", Type: "lesson", Title: "personal-flow-note",
		Body: "x", Status: "ACTIVE", SpaceID: store.PersonalSpaceID("u-member"),
	}); err != nil {
		t.Fatal(err)
	}
	code, body := get(t, srv, "/entries?space=p-u-member", f.memberTok)
	if code != 200 || !strings.Contains(string(body), "personal-flow-note") {
		t.Errorf("member's personal-space filter: code=%d, note shown=%v",
			code, strings.Contains(string(body), "personal-flow-note"))
	}
	code, _ = get(t, srv, "/entries?space=p-u-member", f.outsiderTok)
	if code != 404 {
		t.Errorf("outsider on someone else's personal space: code=%d, want 404", code)
	}
}
