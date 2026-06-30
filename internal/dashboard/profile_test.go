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

// Seed an agent owned by alice, with a description. The profile page
// should render the description, link to the owner, and never leak
// alice's email.
func seedProfileFixture(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateUser(ctx, &store.User{
		ID:           "claude-omoikane-test",
		Name:         "claude-omoikane",
		Role:         "agent",
		ParentUserID: "alice",
		Description:  "I read code from the inside and notice middleware composition patterns.",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProfilePageRendersAgent(t *testing.T) {
	srv, st, tok := mountAuthed(t)
	seedProfileFixture(t, st)
	resp, err := http.Get(srv.URL + "/u/claude-omoikane-test?token=" + tok)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	s := string(body)
	for _, want := range []string{
		"claude-omoikane",                                // name
		"middleware composition patterns",                // description
		`badge badge-role-agent`,                         // role badge
		"Operated by",                                    // parent section
		"Alice",                                          // parent name
		`href="/u/alice`,                                 // link to parent profile
		"on behalf of",                                   // audit-log copy
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in profile page", want)
		}
	}
}

// Privacy contract: when viewer != profile-target, the target's email
// must not appear in the response body. We test this with a separate
// viewer (bob) looking at alice and at alice's agent. The header's own
// email (bob's) is fine — that's showing the viewer their own identity,
// not leaking someone else's.
func TestProfilePageDoesNotLeakOtherUserEmail(t *testing.T) {
	srv, st, _ := mountAuthed(t)
	seedProfileFixture(t, st)
	ctx := context.Background()
	if err := st.CreateUser(ctx, &store.User{
		ID: "bob", Name: "Bob", Role: "member", Email: "bob@y.com",
	}); err != nil {
		t.Fatal(err)
	}
	bobTok, _ := st.CreateToken(ctx, "bob", "test",
		[]string{"read", "write", "admin"}, nil)

	// Bob views alice's profile.
	resp, _ := http.Get(srv.URL + "/u/alice?token=" + bobTok)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "alice@x.com") {
		t.Fatal("alice's email leaked to viewer bob")
	}

	// Bob views alice's agent's profile.
	resp2, _ := http.Get(srv.URL + "/u/claude-omoikane-test?token=" + bobTok)
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)
	if strings.Contains(string(body2), "alice@x.com") {
		t.Fatal("alice's email leaked through agent profile to viewer bob")
	}
}

func TestProfilePageHumanShowsChildAgents(t *testing.T) {
	srv, st, tok := mountAuthed(t)
	seedProfileFixture(t, st) // creates claude-omoikane owned by alice
	resp, _ := http.Get(srv.URL + "/u/alice?token=" + tok)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	s := string(body)
	if !strings.Contains(s, "Agents operated by Alice") {
		t.Error("missing 'Agents operated by' section")
	}
	if !strings.Contains(s, "claude-omoikane") {
		t.Error("alice's agent not listed on her profile")
	}
}

func TestProfilePage404OnUnknown(t *testing.T) {
	srv, _, tok := mountAuthed(t)
	resp, _ := http.Get(srv.URL + "/u/no-such-user?token=" + tok)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	// Even 404 keeps the layout chrome — there should be a banner.
	if !strings.Contains(string(body), "no user with id") {
		t.Errorf("missing error banner: %s", string(body)[:300])
	}
}

// Self profile shows the edit form; other users' profiles don't.
func TestProfileEditFormVisibleOnlyForSelf(t *testing.T) {
	srv, st, tok := mountAuthed(t)
	seedProfileFixture(t, st)

	// Alice viewing her own profile → edit form present
	resp, _ := http.Get(srv.URL + "/u/alice?token=" + tok)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Edit my profile") {
		t.Error("self-profile is missing edit form")
	}

	// Alice viewing claude-omoikane's profile → no edit form
	resp2, _ := http.Get(srv.URL + "/u/claude-omoikane-test?token=" + tok)
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)
	if strings.Contains(string(body2), "Edit my profile") {
		t.Error("edit form leaked into other-user profile view")
	}
}

func TestProfileEditFormPersists(t *testing.T) {
	srv, st, tok := mountAuthed(t)

	form := url.Values{}
	form.Set("name", "Alice the Curator")
	form.Set("description", "I curate the audit project.")
	form.Set("avatar_url", "")
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/u/alice/edit?token="+tok, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", resp.StatusCode)
	}

	// Verify the DB has the new values.
	u, _ := st.GetUser(context.Background(), "alice")
	if u.Name != "Alice the Curator" {
		t.Errorf("name not persisted: %q", u.Name)
	}
	if u.Description != "I curate the audit project." {
		t.Errorf("description not persisted: %q", u.Description)
	}
}

// Bob cannot edit alice's profile.
func TestProfileEditRefusesOtherUser(t *testing.T) {
	srv, st, _ := mountAuthed(t)
	ctx := context.Background()
	_ = st.CreateUser(ctx, &store.User{ID: "bob", Name: "Bob", Role: "member"})
	bobTok, _ := st.CreateToken(ctx, "bob", "test",
		[]string{"read", "write", "admin"}, nil)

	form := url.Values{}
	form.Set("name", "Alice Pwned")
	form.Set("description", "compromised")
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/u/alice/edit?token="+bobTok, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
	// Alice's data must be untouched.
	u, _ := st.GetUser(ctx, "alice")
	if u.Name != "Alice" || strings.Contains(u.Description, "compromised") {
		t.Fatalf("alice was modified by bob: %+v", u)
	}
}
