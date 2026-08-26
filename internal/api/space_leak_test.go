package api

// Space read-ACL leak matrix (issue #60, Phase 1 slice 2).
//
// CONVENTION (enforced by review): every entry-carrying read or write
// route added to the API MUST get a row in leakMatrixRows below. The
// fixture plants a restricted-space entry whose title/body/symptom (and
// every derived artefact: index phrases, case trigger_query, comment
// body, progress notes) contain the unique marker string. The matrix
// then hits each route with a NON-member token and asserts that neither
// the marker nor the entry id appears in a single byte of the response —
// body OR headers. The member-token pair test asserts the same routes DO
// surface the entry for someone inside the space, so a fail-closed bug
// that hides everything from everyone cannot pass either.
//
// Slice 3 (aggregates + attachments) is covered below: aggregates are
// single-space (migration 032) — a restricted situation / cluster /
// browse node / use_case / attachment is planted in the fixture, and
// /index, /findings/{id}/correlate, /librarian/backlog/reprocess get
// rows too. /clusters/rebuild is admin-only and clusters the internal
// space exclusively (store-enforced), so it has no non-member row.
//
// Slice 4 (chat + events) is covered below and in
// space_leak_slice4_test.go: an intent=talk thread owned by u-member
// (title + message carry the marker) gates threads list / messages /
// close / chat post / include_chat search; librarian_tasks carry the
// space of the entry an open-work claim minted them from (migration
// 033) and gate list / claim / complete; SSE (comment.created +
// chat.message + chat.status) and webhook space_scope delivery are
// asserted event-by-event in the slice-4 file. Agent users
// (users.role=agent) may read/write foreign talk threads — the /talk
// responder path — pinned by TestTalkAgentException.
//
// Slice 5 (dashboard + admin UI) is covered in
// internal/dashboard/space_leak_page_test.go: the dashboard installs
// the SAME ResolveVisibleSpaces middleware, and its own page-level
// matrix (outsider/member pair) walks every content-carrying page.
// The /v1/admin/spaces|groups management routes added in slice 5 are
// admin-scope-only (RequireScope gates them wholesale, asserted in
// admin_spaces_test.go) and serve org-wide metadata — they belong to
// the "all-visible metadata by design" class below, not to this
// matrix.
//
// FILE LAYOUT (issue #99 item 3) — the case TABLE is split by resource
// domain; this file keeps the fixture + runner and concatenates every
// domain table into ONE run, so the invariant stays global:
//
//   space_leak_entries_test.go     leakCasesEntries    entries + descendants,
//                                  search/lookups/reflect, feedback, cases,
//                                  relations, open work, per-user projections
//   space_leak_aggregates_test.go  leakCasesAggregates situations, clusters,
//                                  browse, /index, use_cases, attachments,
//                                  findings, backlog reprocess
//   space_leak_threads_test.go     leakCasesThreads    chat threads, chat
//                                  search, librarian tasks
//   space_leak_semantics_test.go   write-semantics tests beyond the matrix
//                                  (entry/aggregate creation into spaces,
//                                  member harmless writes, attachment upload,
//                                  backlog reprocess counts)
//
// NOT COVERED routes live in the machine-checked ledger `leakNotCovered`
// in space_leak_guard_test.go, each with its reason (the classes: public
// endpoints, all-visible metadata by design — a v2 residual — and
// admin-scope ops). TestSpaceLeakMatrixCompleteness walks the chi route
// tree and FAILS on any registered route that has neither a matrix row
// nor a ledger entry — coverage is no longer verified by eyeball.
//
// A deliberate residual of the "names are metadata" class, pinned here
// because it is a semantic (not a route): use_case slugs are a GLOBAL
// namespace — upserting a name_en that slugifies onto a hidden
// use_case's slug 409s; neutral naming is the operating rule for
// restricted spaces.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

const leakMarker = "XYZZYLEAK"

// leakFixture is everything the matrix rows need to address the
// restricted world.
type leakFixture struct {
	base        string
	memberTok   string
	outsiderTok string
	adminTok    string // testServer's admin token (unrestricted view)
	st          *store.Store

	spaceID    string
	groupID    string // the group granting u-member access to spaceID
	secretID   string // restricted-space entry (carries leakMarker)
	internalID string // internal-space entry with a relation to secretID
	caseID     string // usage case on secretID (trigger_query carries marker)
	commentID  string // comment on secretID (@mentions both users)

	// Slice 3: restricted-space aggregates (single-space contract).
	situationID  string // situation in the space (description carries marker)
	clusterID    string // cluster in the space (title carries marker; secret is a member)
	nodeID       string // ROOT hierarchy node in the space (name carries marker; secret attached)
	useCaseID    string // use_case in the space (name_ja carries marker; secret linked; has synthesis)
	attachmentID string // attachment in the space (caption + content carry marker)
	findingID    string // neutral external finding, correlated to the secret entry

	// Slice 4: chat + tasks.
	talkThreadID  string // u-member's intent=talk thread (title + message carry marker)
	coordThreadID string // shared non-talk thread (neutral; everyone sees it)
	taskID        string // PENDING librarian_task in the space (title carries marker)
}

func newLeakFixture(t *testing.T) *leakFixture {
	t.Helper()
	base, adminTok, st := testServer(t)
	ctx := context.Background() // no visibility on ctx = unrestricted (setup path)

	// Two ordinary (non-admin) users. CreateUser provisions internal
	// group membership + personal spaces (slice 1 hook).
	for _, u := range []string{"u-member", "u-outsider"} {
		if err := st.CreateUser(ctx, &store.User{ID: u, Name: u, Role: "member"}); err != nil {
			t.Fatalf("create user %s: %v", u, err)
		}
	}
	memberTok, err := st.CreateToken(ctx, "u-member", "member-tok", []string{"read", "write", "librarian"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	outsiderTok, err := st.CreateToken(ctx, "u-outsider", "outsider-tok", []string{"read", "write", "librarian"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Restricted space, granted to a group containing only u-member.
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

	// The restricted entry. Marker in every free-text field the API can
	// surface.
	secret := &store.Entry{
		ProjectID: "p-leak",
		Type:      "trap",
		Title:     leakMarker + " secret title",
		Symptom:   leakMarker + " secret symptom",
		Body:      leakMarker + " secret body",
		Status:    "ACTIVE",
		SpaceID:   sp.ID,
		Tags:      []string{"leakmarker-tag", "open", "skill:cataloger"},
	}
	secretID, err := st.CreateEntry(ctx, secret)
	if err != nil {
		t.Fatalf("create secret entry: %v", err)
	}

	// Ordinary internal entry, linked to the secret one so the relations
	// route has a cross-space edge to hide.
	internal := &store.Entry{
		ProjectID: "p-leak",
		Type:      "lesson",
		Title:     "plain internal title",
		Body:      "plain internal body",
		Status:    "ACTIVE",
	}
	internalID, err := st.CreateEntry(ctx, internal)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRelation(ctx, &store.Relation{
		FromID: internalID, ToID: secretID, RelType: "see_also",
	}); err != nil {
		t.Fatal(err)
	}

	// Reverse-index phrases → lookup/by-symptom + by-trigger candidates.
	if err := st.ReplaceSymptoms(ctx, secretID, []string{leakMarker + " indexed symptom phrase"}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceTriggers(ctx, secretID,
		[]store.IndexedTrigger{{Phrase: leakMarker + " indexed trigger phrase"}}, "test"); err != nil {
		t.Fatal(err)
	}

	// Situation with a NEUTRAL description linked to the secret entry —
	// the lookup must not return the entry id to a non-member. Lives in
	// the restricted space itself: the single-space contract (slice 3)
	// only lets an aggregate hold entries from its own space.
	sitNeutral, err := st.CreateSituation(ctx, &store.Situation{
		Description: "wholly neutral situation description",
		SpaceID:     sp.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LinkEntryToSituation(ctx, sitNeutral, secretID, 1.0, ""); err != nil {
		t.Fatal(err)
	}

	// ---- slice 3 fixtures: aggregates pinned to the restricted space ----

	sitID, err := st.CreateSituation(ctx, &store.Situation{
		Description: leakMarker + " situation heading",
		SpaceID:     sp.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	clusterID, err := st.CreateCluster(ctx, &store.IncidentCluster{
		ProjectID: "p-leak",
		Title:     leakMarker + " cluster title",
		Summary:   leakMarker + " cluster summary",
		SpaceID:   sp.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddClusterMember(ctx, clusterID, secretID, 1.0, "test"); err != nil {
		t.Fatal(err)
	}

	nodeID, err := st.CreateHierarchyNode(ctx, &store.HierarchyNode{
		ProjectID: "p-leak",
		Name:      leakMarker + " node name",
		SpaceID:   sp.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AttachEntryToNode(ctx, nodeID, secretID, 1.0, "test"); err != nil {
		t.Fatal(err)
	}

	uc, err := st.UpsertUseCase(ctx, &store.UseCase{
		NameJA:  leakMarker + " ユースケース",
		NameEN:  "leak matrix usecase",
		SpaceID: sp.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LinkUseCaseEntry(ctx, uc.ID, secretID, "test"); err != nil {
		t.Fatal(err)
	}
	// Its cross-entry synthesis (a librarian_meta entry) lives in the
	// same space — /use_cases/{ref}/synthesis must 404 for outsiders.
	if _, err := st.CreateEntry(ctx, &store.Entry{
		ProjectID: "p-leak",
		Type:      "librarian_meta",
		Title:     leakMarker + " synthesis title",
		Body:      leakMarker + " synthesis body",
		Status:    "ACTIVE",
		SpaceID:   sp.ID,
		Metadata:  json.RawMessage(`{"kind":"use_case_synthesis","use_case_id":"` + uc.ID + `"}`),
	}); err != nil {
		t.Fatal(err)
	}

	att, err := st.CreateAttachment(ctx, store.CreateAttachmentParams{
		ProjectID:  "p-leak",
		Mime:       "text/plain",
		Filename:   "notes.txt",
		Role:       "log",
		Caption:    leakMarker + " attachment caption",
		UploadedBy: "admin",
		SpaceID:    sp.ID,
		Content:    strings.NewReader(leakMarker + " attachment content"),
		MaxBytes:   1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Neutral external finding correlated to the secret entry — the
	// correlation edge must never surface it, and correlating is gated.
	findingID, err := st.RecordFinding(ctx, &store.ExternalFinding{
		AgentLens:   "scout",
		SourceTitle: "wholly neutral finding title",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CorrelateFinding(ctx, findingID, secretID, 1.0); err != nil {
		t.Fatal(err)
	}

	// Usage case whose trigger_query carries the marker.
	caseID, err := st.CreateCase(ctx, &store.UsageCase{
		EntryID: secretID, ProjectID: "p-leak",
		TriggerQuery: leakMarker + " case trigger query",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Three misleading cases push the entry into review_queue,
	// coordinator triage's misleading-heavy list, and tier 4.
	for i := 0; i < 3; i++ {
		if _, err := st.CreateCase(ctx, &store.UsageCase{
			EntryID: secretID, ProjectID: "p-leak", Result: "misleading",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Comment on the secret entry mentioning both users (author=admin so
	// the "written by someone else" review-request predicate holds).
	comment, err := st.CreateComment(ctx, secretID, "admin",
		leakMarker+" comment body", "", []string{"u-member", "u-outsider"})
	if err != nil {
		t.Fatal(err)
	}

	// Librarian progress row whose notes carry the marker.
	if err := st.RecordProgress(ctx, &store.LibrarianProgress{
		Role: "cataloger", EntryID: secretID, Action: "summarize",
		Notes: leakMarker + " progress notes",
	}); err != nil {
		t.Fatal(err)
	}

	// Both users bookmarked the entry (planted store-side; the point is
	// the LIST must not show it to the outsider even when the row exists).
	if err := st.AddBookmark(ctx, "u-member", secretID); err != nil {
		t.Fatal(err)
	}
	if err := st.AddBookmark(ctx, "u-outsider", secretID); err != nil {
		t.Fatal(err)
	}

	// ---- slice 4 fixtures: chat threads + tasks ----

	// u-member's personal talk thread; title and one message carry the
	// marker. The coordination thread is the shared librarian room.
	talkID, err := st.OpenThread(ctx, &store.ChatThread{
		Title: leakMarker + " talk thread", Intent: "talk", CreatedBy: "u-member",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PostChatMessage(ctx, &store.ChatMessage{
		ThreadID: talkID, AuthorRole: "human", AuthorUserID: "u-member",
		Content: leakMarker + " talk message",
	}); err != nil {
		t.Fatal(err)
	}
	coordID, err := st.OpenThread(ctx, &store.ChatThread{
		Title: "coordination thread", Intent: "observation", CreatedBy: "u-member",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PostChatMessage(ctx, &store.ChatMessage{
		ThreadID: coordID, AuthorRole: "human", AuthorUserID: "u-member",
		Content: "coordination shared message",
	}); err != nil {
		t.Fatal(err)
	}

	// librarian_tasks.assigned_to has an FK to librarian_instances —
	// register the instances the claim paths below assign to.
	for _, inst := range []string{"i-fixture", "i-member"} {
		if _, err := st.RegisterLibrarianInstance(ctx, &store.LibrarianInstance{
			InstanceID: inst, Role: "cataloger",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Claim + release the secret entry: the claim mints a task titled
	// "impl: <marker title>" in the entry's space (migration 033); the
	// release restores the `open` tag (the open_work rows above depend
	// on it) while the CANCELLED task keeps carrying the title.
	if _, err := st.ClaimOpenWork(ctx, secretID, "cataloger", "i-fixture", "S"); err != nil {
		t.Fatal(err)
	}
	if err := st.ReleaseOpenWork(ctx, secretID, "i-fixture"); err != nil {
		t.Fatal(err)
	}
	// A PENDING task planted in the space — the claim/complete gate.
	taskID, err := st.EnqueueTask(ctx, &store.LibrarianTask{
		Role: "cataloger", Title: leakMarker + " pending task", SpaceID: sp.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	return &leakFixture{
		base: base, memberTok: memberTok, outsiderTok: outsiderTok,
		adminTok: adminTok, st: st,
		spaceID: sp.ID, groupID: g.ID, secretID: secretID, internalID: internalID,
		caseID: caseID, commentID: comment.ID,
		situationID: sitID, clusterID: clusterID, nodeID: nodeID,
		useCaseID: uc.ID, attachmentID: att.ID, findingID: findingID,
		talkThreadID: talkID, coordThreadID: coordID, taskID: taskID,
	}
}

// leakRow is one route of the matrix.
type leakRow struct {
	name   string
	method string
	path   string // relative, may contain {secret}/{internal}/{case}/{comment}/{space}
	body   any
	header map[string]string

	// outsiderStatus, when non-zero, is additionally asserted (404 for
	// direct-addressed routes; list/search routes return 200-empty).
	outsiderStatus int
	// memberSees: the member-token pair test asserts the marker (or, for
	// idOnly rows, the secret entry id) appears in the response.
	memberSees bool
	// idOnly: member-pair looks for the entry id, not the marker (routes
	// that return ids/edges without entry text).
	idOnly bool
	// altMarker: routes whose restricted-derived bytes are neither the
	// marker nor the entry id (e.g. /index tag buckets — tags are
	// lower-cased on write, so the uppercase marker can't ride a tag).
	// The outsider response must NOT contain it; a memberSees row must.
	altMarker string
}

func (f *leakFixture) expand(p string) string {
	r := strings.NewReplacer(
		"{secret}", f.secretID,
		"{internal}", f.internalID,
		"{case}", f.caseID,
		"{comment}", f.commentID,
		"{space}", f.spaceID,
		"{situation}", f.situationID,
		"{cluster}", f.clusterID,
		"{node}", f.nodeID,
		"{usecase}", f.useCaseID,
		"{attachment}", f.attachmentID,
		"{finding}", f.findingID,
		"{talkthread}", f.talkThreadID,
		"{coordthread}", f.coordThreadID,
		"{task}", f.taskID,
	)
	return f.base + r.Replace(p)
}

// leakCaseTables enumerates every per-domain case table. The runner
// concatenates them into ONE test run (the union — the invariant stays
// global) and the completeness guard in space_leak_guard_test.go checks
// the same union against the chi router's registered routes.
func leakCaseTables() [][]leakRow {
	return [][]leakRow{leakCasesEntries, leakCasesAggregates, leakCasesThreads}
}

func leakMatrixRows() []leakRow {
	var rows []leakRow
	for _, tbl := range leakCaseTables() {
		rows = append(rows, tbl...)
	}
	return rows
}

// expandBody substitutes id placeholders inside JSON body values.
func (f *leakFixture) expandBody(body any) any {
	m, ok := body.(map[string]any)
	if !ok {
		return body
	}
	out := map[string]any{}
	sub := strings.NewReplacer(
		"{secretid}", f.secretID,
		"{internalid}", f.internalID,
		"{spaceid}", f.spaceID,
		"{usecaseid}", f.useCaseID,
		"{talkthreadid}", f.talkThreadID,
		"{coordthreadid}", f.coordThreadID,
	)
	for k, v := range m {
		switch vv := v.(type) {
		case string:
			out[k] = sub.Replace(vv)
		case []string:
			ss := make([]string, len(vv))
			for i, s := range vv {
				ss[i] = sub.Replace(s)
			}
			out[k] = ss
		default:
			out[k] = v
		}
	}
	return out
}

// doRaw is doJSON's sibling that also returns the *http.Response so the
// matrix can scan response HEADERS for leaked bytes.
func doRaw(t *testing.T, method, url, tok string, body any, headers map[string]string) (*http.Response, []byte) {
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
	return resp, raw
}

// TestSpaceLeakMatrix drives every row with the NON-member token: not one
// byte of the restricted entry (marker or id) may appear in body or
// headers, and direct-addressed routes must answer 404 (never 403 — an
// existence oracle).
func TestSpaceLeakMatrix(t *testing.T) {
	f := newLeakFixture(t)
	for _, row := range leakMatrixRows() {
		t.Run(row.name, func(t *testing.T) {
			resp, raw := doRaw(t, row.method, f.expand(row.path), f.outsiderTok,
				f.expandBody(row.body), row.header)
			if row.outsiderStatus != 0 && resp.StatusCode != row.outsiderStatus {
				t.Errorf("outsider status = %d, want %d (body=%s)",
					resp.StatusCode, row.outsiderStatus, string(raw))
			}
			if resp.StatusCode == http.StatusForbidden {
				t.Errorf("403 is an existence oracle; want 404 for hidden resources")
			}
			blob := string(raw)
			if strings.Contains(blob, leakMarker) {
				t.Errorf("marker leaked in body: %s", blob)
			}
			if row.altMarker != "" && strings.Contains(blob, row.altMarker) {
				t.Errorf("restricted-derived %q leaked in body: %s", row.altMarker, blob)
			}
			// The id check applies only when the caller did NOT supply the
			// id in the request: a 404 echoing the id the caller itself
			// sent (path or body) discloses nothing, but an id appearing
			// in a list/search/lookup response the caller addressed
			// WITHOUT the id is an enumeration leak.
			requestCarriesID := strings.Contains(f.expand(row.path), f.secretID)
			if b, ok := f.expandBody(row.body).(map[string]any); ok {
				enc, _ := json.Marshal(b)
				requestCarriesID = requestCarriesID || strings.Contains(string(enc), f.secretID)
			}
			if !requestCarriesID && strings.Contains(blob, f.secretID) {
				t.Errorf("secret entry id leaked in body: %s", blob)
			}
			for k, vs := range resp.Header {
				for _, v := range vs {
					if strings.Contains(v, leakMarker) || strings.Contains(v, f.secretID) {
						t.Errorf("leak in response header %s: %s", k, v)
					}
				}
			}
			if resp.Header.Get("X-Review-Requests") != "" {
				t.Errorf("X-Review-Requests header set for outsider (count includes restricted-space mention)")
			}
		})
	}
}

// TestSpaceMemberVisibility is the pair check: the same routes DO show
// the entry to a member of the space (guards against a fail-closed bug
// that "passes" the leak matrix by hiding everything from everyone).
func TestSpaceMemberVisibility(t *testing.T) {
	f := newLeakFixture(t)
	for _, row := range leakMatrixRows() {
		if !row.memberSees {
			continue
		}
		t.Run(row.name, func(t *testing.T) {
			resp, raw := doRaw(t, row.method, f.expand(row.path), f.memberTok,
				f.expandBody(row.body), row.header)
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				t.Fatalf("member status = %d, body=%s", resp.StatusCode, string(raw))
			}
			want := leakMarker
			if row.idOnly {
				want = f.secretID
			}
			if row.altMarker != "" {
				want = row.altMarker
			}
			if !strings.Contains(string(raw), want) {
				t.Errorf("member should see %q via %s %s; body=%s", want, row.method, row.path, string(raw))
			}
		})
	}

	// The review-request header pair: the member's mention on the
	// restricted entry IS counted for them.
	t.Run("review-request header for member", func(t *testing.T) {
		resp, _ := doRaw(t, "GET", f.base+"/v1/entries?limit=1", f.memberTok, nil, nil)
		if resp.Header.Get("X-Review-Requests") == "" {
			t.Errorf("member should get X-Review-Requests header for the restricted-entry mention")
		}
	})
}
