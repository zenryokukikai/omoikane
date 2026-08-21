package dashboard

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// mountAuthed creates alice as role=admin → /members should be visible.
func TestMembersPageVisibleForAdmin(t *testing.T) {
	srv, _, tok := mountAuthed(t)
	resp, _ := http.Get(srv.URL + "/members?token=" + tok)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{
		"Members",
		"Invite a new member",
		"Current members",
		"Alice", // bootstrap admin
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("missing %q on /members for admin", want)
		}
	}
}

// Non-admin sees an explanatory banner, not the management UI.
func TestMembersPageBlockedForNonAdmin(t *testing.T) {
	srv, st, _ := mountAuthed(t)
	ctx := context.Background()
	_ = st.CreateUser(ctx, &store.User{ID: "bob", Name: "Bob", Role: "member"})
	memberTok, _ := st.CreateToken(ctx, "bob", "test",
		[]string{"read", "write"}, nil)

	resp, _ := http.Get(srv.URL + "/members?token=" + memberTok)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Only admins") {
		t.Errorf("non-admin should see explanatory banner; got: %s", string(body)[:500])
	}
	if strings.Contains(string(body), "Invite a new member") {
		t.Error("non-admin should NOT see the invite form")
	}
}

func TestMembersInviteForm(t *testing.T) {
	srv, st, tok := mountAuthed(t)
	form := url.Values{}
	form.Set("email", "guest@example.com")
	form.Set("role", "member")
	form.Set("note", "qa contact")
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/members/invite?token="+tok, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", resp.StatusCode)
	}

	invs, _ := st.ListMemberInvitations(context.Background(), "")
	if len(invs) != 1 || invs[0].TargetEmail != "guest@example.com" {
		t.Fatalf("invite not persisted: %+v", invs)
	}
}

// postMembersInvite submits the invite form with the given token and
// returns the immediate status (redirects not followed).
func postMembersInvite(t *testing.T, srvURL, tok, email string) int {
	t.Helper()
	form := url.Values{}
	form.Set("email", email)
	form.Set("role", "admin")
	req, err := http.NewRequest(http.MethodPost,
		srvURL+"/members/invite?token="+tok, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// The /members admin gate is the token's ADMIN SCOPE — the one admin
// contract (issue #60 design v2), matching the API's PATCH
// /v1/admin/users/{id}/role. Two directions pinned here:
//
//   - a role=admin user on a token narrowed to read,write is refused:
//     tokens can only narrow authority, and a narrowed token must not
//     be re-widened by consulting users.role (that was the slice-5
//     third-party-review finding — the form gate was weaker than the
//     equivalent API route);
//   - a role=member user WITH the admin scope passes: the admin scope
//     is minted only by the admin CLI (kb-server admin-token) or by
//     the OAuth session of a role=admin user, so possessing it IS
//     admin authority; users.role is provisioning/display metadata.
func TestMembersInviteScopeContract(t *testing.T) {
	srv, st, _ := mountAuthed(t)
	ctx := context.Background()

	// No admin scope → 403, regardless of role.
	_ = st.CreateUser(ctx, &store.User{ID: "bob", Name: "Bob", Role: "member"})
	plainTok, _ := st.CreateToken(ctx, "bob", "t", []string{"read", "write"}, nil)
	if code := postMembersInvite(t, srv.URL, plainTok, "trojan@example.com"); code != http.StatusForbidden {
		t.Fatalf("member role, no admin scope: %d, want 403", code)
	}

	// role=admin but a NARROWED token → still 403 (no re-widening).
	_ = st.CreateUser(ctx, &store.User{ID: "root2", Name: "Root2", Role: "admin"})
	narrowTok, _ := st.CreateToken(ctx, "root2", "t", []string{"read", "write"}, nil)
	if code := postMembersInvite(t, srv.URL, narrowTok, "trojan2@example.com"); code != http.StatusForbidden {
		t.Fatalf("admin role on narrowed token: %d, want 403", code)
	}
	if invs, _ := st.ListMemberInvitations(ctx, ""); len(invs) != 0 {
		t.Fatalf("invitation leaked past the scope gate: %+v", invs)
	}

	// role=member WITH admin scope → passes (scope is the authority).
	scopedTok, _ := st.CreateToken(ctx, "bob", "t2", []string{"read", "write", "admin"}, nil)
	if code := postMembersInvite(t, srv.URL, scopedTok, "ok@example.com"); code != http.StatusSeeOther {
		t.Fatalf("member role with admin scope: %d, want 303", code)
	}
	invs, _ := st.ListMemberInvitations(ctx, "")
	if len(invs) != 1 || invs[0].TargetEmail != "ok@example.com" {
		t.Fatalf("admin-scope invite not persisted: %+v", invs)
	}
}

// Claim page (public, no auth) renders the invitation details.
func TestMemberClaimPagePublic(t *testing.T) {
	srv, st, _ := mountAuthed(t) // alice is the only admin/user
	ctx := context.Background()
	inv, _ := st.CreateMemberInvitation(ctx, "alice", "newcomer@example.com", "admin", "ops lead")

	// No token — public access.
	resp, _ := http.Get(srv.URL + "/members/claim/" + inv.Code)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 (public), got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{
		"newcomer@example.com",
		"admin",   // role badge
		"Sign in with Google",
		"Alice",   // inviter
		"ops lead", // note
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("missing %q on claim page", want)
		}
	}
}

func TestMemberClaimPageNotFound(t *testing.T) {
	srv, _, _ := mountAuthed(t)
	resp, _ := http.Get(srv.URL + "/members/claim/deadbeef")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "invitation not found") {
		t.Errorf("missing error message; got: %s", string(body)[:300])
	}
}

func TestMembersRoleChangeForm(t *testing.T) {
	srv, st, tok := mountAuthed(t)
	ctx := context.Background()
	_ = st.CreateUser(ctx, &store.User{ID: "bob", Name: "Bob", Role: "member"})

	form := url.Values{}
	form.Set("role", "admin")
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/members/bob/role?token="+tok, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", resp.StatusCode)
	}
	u, _ := st.GetUser(ctx, "bob")
	if u.Role != "admin" {
		t.Errorf("bob not promoted: %s", u.Role)
	}
}
