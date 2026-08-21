package dashboard

// Dashboard space-leak matrix (issue #60, Phase 1 slice 5).
//
// Same philosophy as internal/api/space_leak_test.go, applied to the
// HTML surface: plant a restricted-space entry (plus aggregates, an
// attachment, a talk thread — every derived artefact carrying a unique
// marker), then walk every dashboard page as a NON-member and assert
// that neither the marker nor the restricted ids appear in a single
// byte of the rendered HTML. The member-token pair test asserts the
// same pages DO show the content to someone inside the space, so a
// fail-closed bug that blanks the dashboard for everyone cannot pass
// either.
//
// CONVENTION (enforced by review): every new dashboard page that can
// carry entry-derived text gets a row in dashLeakRows below.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// postForm submits an x-www-form-urlencoded POST with the token in the
// query string (the dashboard's form-auth path) and returns the status.
func postForm(t *testing.T, srv *httptest.Server, path, token string, fields map[string]string) int {
	t.Helper()
	form := url.Values{}
	for k, v := range fields {
		form.Set(k, v)
	}
	u := srv.URL + path
	if token != "" {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		u += sep + "token=" + url.QueryEscape(token)
	}
	req, err := http.NewRequest(http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Don't follow the post-action redirect: the caller asserts on the
	// immediate status (303 on success, 4xx on refusal).
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

const dashLeakMarker = "XYZZYLEAK"

type dashLeakFixture struct {
	st          *store.Store
	memberTok   string
	outsiderTok string
	adminTok    string

	spaceID      string
	secretID     string
	internalID   string // internal-space entry related to the secret one
	situationID  string
	clusterID    string
	nodeID       string
	useCaseID    string
	attachmentID string
	talkThreadID string
}

func newDashLeakFixture(t *testing.T) (*dashLeakFixture, *httptest.Server) {
	t.Helper()
	st := newDashStore(t)
	ctx := context.Background() // setup path: unrestricted

	if err := st.CreateUser(ctx, &store.User{ID: "root", Name: "root", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	adminTok, err := st.CreateToken(ctx, "root", "root", []string{"read", "write", "admin"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"u-member", "u-outsider"} {
		if err := st.CreateUser(ctx, &store.User{ID: u, Name: u, Role: "member"}); err != nil {
			t.Fatal(err)
		}
	}
	memberTok, err := st.CreateToken(ctx, "u-member", "m", []string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	outsiderTok, err := st.CreateToken(ctx, "u-outsider", "o", []string{"read", "write"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	sp, err := st.CreateSpace(ctx, "secret-space")
	if err != nil {
		t.Fatal(err)
	}
	g, err := st.CreateGroup(ctx, "secret-group")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddGroupMember(ctx, g.ID, "u-member"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSpaceACL(ctx, sp.ID, g.ID, store.SpaceRoleMember); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProject(ctx, &store.Project{ID: "p-leak", Name: "leak project"}); err != nil {
		t.Fatal(err)
	}

	secretID, err := st.CreateEntry(ctx, &store.Entry{
		ProjectID: "p-leak", Type: "trap",
		Title:   dashLeakMarker + " secret title",
		Symptom: dashLeakMarker + " secret symptom",
		Body:    dashLeakMarker + " secret body",
		Status:  "ACTIVE",
		SpaceID: sp.ID,
		Tags:    []string{"leakmarker-tag"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Restricted attachment, referenced from the INTERNAL entry's body:
	// the entry page's markdown unfurl must not resolve it for outsiders
	// (the renderContent ctx-threading path).
	att, err := st.CreateAttachment(ctx, store.CreateAttachmentParams{
		ProjectID: "p-leak", Mime: "image/png", Filename: "secret.png",
		Role: "chart", Caption: dashLeakMarker + " attachment caption",
		UploadedBy: "root", SpaceID: sp.ID,
		Content: strings.NewReader("png-bytes"), MaxBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}

	internalID, err := st.CreateEntry(ctx, &store.Entry{
		ProjectID: "p-leak", Type: "lesson",
		Title:  "plain internal title",
		Body:   "plain internal body\n\n![](attached:" + att.ID + ")",
		Status: "ACTIVE",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRelation(ctx, &store.Relation{
		FromID: internalID, ToID: secretID, RelType: "see_also",
	}); err != nil {
		t.Fatal(err)
	}

	// Reverse index → /lookup.
	if err := st.ReplaceSymptoms(ctx, secretID,
		[]string{dashLeakMarker + " indexed symptom phrase"}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceTriggers(ctx, secretID,
		[]store.IndexedTrigger{{Phrase: dashLeakMarker + " indexed trigger phrase"}}, "test"); err != nil {
		t.Fatal(err)
	}

	// Aggregates pinned to the restricted space (single-space contract).
	sitID, err := st.CreateSituation(ctx, &store.Situation{
		Description: dashLeakMarker + " situation heading", SpaceID: sp.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LinkEntryToSituation(ctx, sitID, secretID, 1.0, ""); err != nil {
		t.Fatal(err)
	}
	clusterID, err := st.CreateCluster(ctx, &store.IncidentCluster{
		ProjectID: "p-leak", Title: dashLeakMarker + " cluster title",
		Summary: dashLeakMarker + " cluster summary", SpaceID: sp.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddClusterMember(ctx, clusterID, secretID, 1.0, "test"); err != nil {
		t.Fatal(err)
	}
	nodeID, err := st.CreateHierarchyNode(ctx, &store.HierarchyNode{
		ProjectID: "p-leak", Name: dashLeakMarker + " node name", SpaceID: sp.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AttachEntryToNode(ctx, nodeID, secretID, 1.0, "test"); err != nil {
		t.Fatal(err)
	}
	uc, err := st.UpsertUseCase(ctx, &store.UseCase{
		NameJA: dashLeakMarker + " ユースケース", NameEN: "leak matrix usecase",
		SpaceID: sp.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LinkUseCaseEntry(ctx, uc.ID, secretID, "test"); err != nil {
		t.Fatal(err)
	}

	// Review queue: three misleading cases push the entry into it.
	for i := 0; i < 3; i++ {
		if _, err := st.CreateCase(ctx, &store.UsageCase{
			EntryID: secretID, ProjectID: "p-leak", Result: "misleading",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Comment on the secret entry.
	if _, err := st.CreateComment(ctx, secretID, "root",
		dashLeakMarker+" comment body", "", nil); err != nil {
		t.Fatal(err)
	}

	// BOTH users bookmarked the entry store-side — the outsider's
	// /bookmarks must hide the row even though it exists.
	for _, u := range []string{"u-member", "u-outsider"} {
		if err := st.AddBookmark(ctx, u, secretID); err != nil {
			t.Fatal(err)
		}
	}

	// u-member's personal talk thread.
	talkID, err := st.OpenThread(ctx, &store.ChatThread{
		Title: dashLeakMarker + " talk thread", Intent: "talk", CreatedBy: "u-member",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PostChatMessage(ctx, &store.ChatMessage{
		ThreadID: talkID, AuthorRole: "human", AuthorUserID: "u-member",
		Content: dashLeakMarker + " talk message",
	}); err != nil {
		t.Fatal(err)
	}

	srv := mount(t, st, false)
	return &dashLeakFixture{
		st: st, memberTok: memberTok, outsiderTok: outsiderTok, adminTok: adminTok,
		spaceID: sp.ID, secretID: secretID, internalID: internalID,
		situationID: sitID, clusterID: clusterID, nodeID: nodeID,
		useCaseID: uc.ID, attachmentID: att.ID,
		talkThreadID: talkID,
	}, srv
}

// dashLeakRow is one dashboard page of the matrix.
type dashLeakRow struct {
	name string
	path string // may contain {secret}/{internal}/{situation}/{cluster}/{node}/{usecase}/{talkthread}

	outsiderStatus int  // asserted when non-zero (404 for direct-addressed pages)
	memberSees     bool // member-pair asserts the marker appears
	memberStatus   int  // member-pair expected status (default 200)
	// altMarker: restricted-derived bytes that are neither the marker
	// nor an id (tags are lower-cased on write).
	altMarker string
	// queryEcho: the page legitimately echoes the request's own ?q=
	// back into the HTML (search box value, pagination links), so the
	// bare marker check would trip on the viewer's own input. These
	// rows skip the bare-marker assertion and check the restricted
	// CONTENT strings instead — ids stay asserted as always.
	queryEcho bool
	// echoAbsent overrides restrictedContent() for a queryEcho row
	// whose own query contains one of the content strings (the lookup
	// page: the outsider TYPES the phrase, so its echo is not a leak —
	// the leak would be the matching entry's title/id in the results).
	echoAbsent []string
	// memberWant overrides the member-pair's expected string. Needed on
	// queryEcho rows, where the bare marker would be satisfied by the
	// member's own echoed query without any result rendered.
	memberWant string
}

// restrictedContent is the restricted free text minus the bare marker:
// what a query-echoing page must still never show.
func restrictedContent() []string {
	return []string{"secret title", "secret body", "secret symptom",
		"indexed symptom phrase", "comment body"}
}

func dashLeakRows() []dashLeakRow {
	return []dashLeakRow{
		{name: "home", path: "/", outsiderStatus: 200, memberSees: true},
		{name: "entries list", path: "/entries?limit=500", outsiderStatus: 200, memberSees: true},
		{name: "entries list q-filter", path: "/entries?q=" + dashLeakMarker, outsiderStatus: 200,
			memberSees: true, queryEcho: true, memberWant: "secret title"},
		{name: "entry page", path: "/entries/{secret}", outsiderStatus: 404, memberSees: true},
		{name: "entry history", path: "/entries/{secret}/history", outsiderStatus: 404, memberSees: true},
		{name: "internal entry page (relations + attachment unfurl)", path: "/entries/{internal}", outsiderStatus: 200, memberSees: true},
		{name: "search", path: "/search?q=" + dashLeakMarker, outsiderStatus: 200,
			memberSees: true, queryEcho: true, memberWant: "secret title"},
		{name: "project page", path: "/projects/p-leak", outsiderStatus: 200, memberSees: true},
		{name: "journal", path: "/journal", outsiderStatus: 200},
		{name: "review queue", path: "/review-queue", outsiderStatus: 200, memberSees: true},
		{name: "clusters list", path: "/clusters", outsiderStatus: 200, memberSees: true},
		{name: "cluster page", path: "/clusters/{cluster}", outsiderStatus: 404, memberSees: true},
		{name: "situations list", path: "/situations", outsiderStatus: 200, memberSees: true},
		{name: "situation page", path: "/situations/{situation}", outsiderStatus: 404, memberSees: true},
		{name: "browse roots", path: "/browse", outsiderStatus: 200, memberSees: true},
		{name: "browse node page", path: "/browse/{node}", outsiderStatus: 404, memberSees: true},
		{name: "index by tag", path: "/index", outsiderStatus: 200, altMarker: "leakmarker-tag"},
		{name: "index by hierarchy", path: "/index?group_by=hierarchy", outsiderStatus: 200, memberSees: true},
		{name: "index by recent", path: "/index?group_by=recent", outsiderStatus: 200},
		{name: "lookup use_case browse", path: "/lookup", outsiderStatus: 200, memberSees: true},
		{name: "lookup symptom", path: "/lookup?mode=symptom&q=" + dashLeakMarker + "+indexed+symptom+phrase",
			outsiderStatus: 200, memberSees: true, queryEcho: true,
			echoAbsent: []string{"secret title", "secret body", "secret symptom"},
			memberWant: "secret title"},
		{name: "use_case page", path: "/use_cases/{usecase}", outsiderStatus: 404, memberSees: true},
		{name: "chat threads (talk excluded for everyone)", path: "/chat", outsiderStatus: 200},
		{name: "chat talk thread (talk lives on /talk only)", path: "/chat/{talkthread}",
			outsiderStatus: 404, memberStatus: 404},
		{name: "talk thread", path: "/talk/{talkthread}", outsiderStatus: 404, memberSees: true},
		{name: "talk thread frag tail", path: "/talk/{talkthread}?frag=tail",
			outsiderStatus: 404, memberSees: true},
		{name: "bookmarks (row planted for both users)", path: "/bookmarks", outsiderStatus: 200, memberSees: true},
	}
}

func (f *dashLeakFixture) expand(p string) string {
	return strings.NewReplacer(
		"{secret}", f.secretID,
		"{internal}", f.internalID,
		"{situation}", f.situationID,
		"{cluster}", f.clusterID,
		"{node}", f.nodeID,
		"{usecase}", f.useCaseID,
		"{talkthread}", f.talkThreadID,
	).Replace(p)
}

// restrictedIDs are the restricted-world ids that must never reach a
// non-member's page — an id in the HTML is an existence oracle even
// without content. The attachment id is deliberately NOT here: the
// internal entry's author wrote `attached:<id>` into a body every
// viewer may read, so the id is authored visible content; what must
// not resolve is the attachment's caption/content (covered by the
// marker check on that row).
func (f *dashLeakFixture) restrictedIDs() []string {
	return []string{
		f.secretID, f.situationID, f.clusterID, f.nodeID,
		f.useCaseID, f.talkThreadID,
	}
}

// TestDashboardSpaceLeakMatrix: not one byte of the restricted world
// (marker, entry id, aggregate ids) may reach a non-member's HTML, and
// direct-addressed pages answer 404 — never 403 (existence oracle).
func TestDashboardSpaceLeakMatrix(t *testing.T) {
	f, srv := newDashLeakFixture(t)
	for _, row := range dashLeakRows() {
		t.Run(row.name, func(t *testing.T) {
			code, body := get(t, srv, f.expand(row.path), f.outsiderTok)
			if row.outsiderStatus != 0 && code != row.outsiderStatus {
				t.Errorf("outsider status = %d, want %d", code, row.outsiderStatus)
			}
			bs := string(body)
			leaks := f.restrictedIDs()
			switch {
			case row.queryEcho && row.echoAbsent != nil:
				leaks = append(leaks, row.echoAbsent...)
			case row.queryEcho:
				leaks = append(leaks, restrictedContent()...)
			default:
				leaks = append(leaks, dashLeakMarker)
			}
			for _, leak := range leaks {
				if strings.Contains(bs, leak) {
					t.Errorf("restricted byte %q leaked into outsider HTML", leak)
				}
			}
			if row.altMarker != "" && strings.Contains(bs, row.altMarker) {
				t.Errorf("restricted-derived %q leaked into outsider HTML", row.altMarker)
			}
		})
	}
}

// TestDashboardSpaceMemberPair: the same pages DO surface the
// restricted content for a member of the space — a fail-closed bug
// that blanks the dashboard for everyone cannot pass both tests.
func TestDashboardSpaceMemberPair(t *testing.T) {
	f, srv := newDashLeakFixture(t)
	for _, row := range dashLeakRows() {
		if !row.memberSees && row.memberStatus == 0 {
			continue
		}
		t.Run(row.name, func(t *testing.T) {
			code, body := get(t, srv, f.expand(row.path), f.memberTok)
			want := row.memberStatus
			if want == 0 {
				want = 200
			}
			if code != want {
				t.Fatalf("member status = %d, want %d", code, want)
			}
			want2 := row.memberWant
			if want2 == "" {
				want2 = dashLeakMarker
			}
			if row.memberSees && !strings.Contains(string(body), want2) {
				t.Errorf("member does not see the restricted content (%q)", want2)
			}
		})
	}
}

// TestDashboardAdminUnrestricted pins the admin contract: the admin
// scope sees every space on the dashboard too.
func TestDashboardAdminUnrestricted(t *testing.T) {
	f, srv := newDashLeakFixture(t)
	code, body := get(t, srv, "/entries/"+f.secretID, f.adminTok)
	if code != 200 || !strings.Contains(string(body), dashLeakMarker) {
		t.Fatalf("admin cannot see the restricted entry: code=%d", code)
	}
}

// TestDashboardHiddenWikiLinkRendersBroken: an internal-space entry
// referencing a hidden entry via [[id]] must render the reference as a
// broken (dead) link for outsiders — a live link would be an existence
// oracle — while members get the normal anchor. Exercises the
// renderContent ctx threading + EntriesExist space filter directly.
func TestDashboardHiddenWikiLinkRendersBroken(t *testing.T) {
	f, srv := newDashLeakFixture(t)
	ctx := context.Background()
	refID, err := f.st.CreateEntry(ctx, &store.Entry{
		ProjectID: "p-leak", Type: "lesson",
		Title: "internal entry with a wiki ref", Status: "ACTIVE",
		Body: "see [[" + f.secretID + "|the doc]] for details",
	})
	if err != nil {
		t.Fatal(err)
	}
	code, body := get(t, srv, "/entries/"+refID, f.outsiderTok)
	if code != 200 {
		t.Fatalf("outsider on internal entry: %d", code)
	}
	bs := string(body)
	if !strings.Contains(bs, "wiki-broken") {
		t.Errorf("hidden wiki ref did not render as broken for the outsider")
	}
	if strings.Contains(bs, `href="/entries/`+f.secretID) {
		t.Errorf("hidden wiki ref rendered as a live link (existence oracle)")
	}
	code, body = get(t, srv, "/entries/"+refID, f.memberTok)
	if code != 200 || !strings.Contains(string(body), `href="/entries/`+f.secretID) {
		t.Errorf("member's wiki ref should be a live link (code=%d)", code)
	}
}

// TestDashboardChatWriteRefusesTalkThreads: the /chat write surface
// mirrors its read pages — posting into or closing a talk thread
// answers 404 for everyone (talk lives on /talk; its owner posts via
// the API).
func TestDashboardChatWriteRefusesTalkThreads(t *testing.T) {
	f, srv := newDashLeakFixture(t)
	for _, sub := range []string{"post", "close"} {
		code := postForm(t, srv, "/chat/"+f.talkThreadID+"/"+sub, f.outsiderTok,
			map[string]string{"content": "intruding", "summary": "x"})
		if code != 404 {
			t.Errorf("%s into talk thread: code=%d, want 404", sub, code)
		}
	}
	msgs, err := f.st.ListChatMessages(context.Background(), f.talkThreadID, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if strings.Contains(m.Content, "intruding") {
			t.Fatalf("outsider message reached the talk thread")
		}
	}
}
