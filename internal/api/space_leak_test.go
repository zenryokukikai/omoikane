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
// NOT YET COVERED — kept in sync with the /v1 route table in api.go
// (the third-party review of slice 2 caught four routes that a vague
// "aggregates etc." list had left dangling; keep this list EXPLICIT):
//
//   slice 5: every dashboard page (this slice already excludes
//     intent=talk from /chat + /chat/{id})
//   all-visible metadata by design (v2 residual risk): /users, /projects,
//     /librarian/instances, /librarian/directives, /admin/* ops;
//     /librarian/quartet(+/decide) and /librarian/findings list/record —
//       coordination artefacts with no entry linkage in their payloads
//       (quartet: topic + librarian role names; finding: external
//       source excerpt); their entry-touching edge (correlate) IS gated.
//     use_case slugs are a GLOBAL namespace: upserting a name_en that
//       slugifies onto a hidden use_case's slug 409s — a deliberate
//       residual of the "names are metadata" class (neutral naming is
//       the operating rule for restricted spaces).
//
// Add each route's rows here in the slice that brings its enforcement.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"

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

func leakMatrixRows() []leakRow {
	asOf := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	return []leakRow{
		// ---- entries + descendants ----
		{name: "entries list", method: "GET", path: "/v1/entries?limit=500", outsiderStatus: 200, memberSees: true},
		{name: "entries list q-filter", method: "GET", path: "/v1/entries?q=" + leakMarker, outsiderStatus: 200, memberSees: true},
		{name: "entry get", method: "GET", path: "/v1/entries/{secret}", outsiderStatus: 404, memberSees: true},
		{name: "entry get as_of", method: "GET", path: "/v1/entries/{secret}?as_of=" + asOf, outsiderStatus: 404, memberSees: true},
		{name: "entry history", method: "GET", path: "/v1/entries/{secret}/history", outsiderStatus: 404, memberSees: true},
		{name: "entry relations", method: "GET", path: "/v1/entries/{secret}/relations?direction=both", outsiderStatus: 404},
		{name: "relations from internal entry", method: "GET", path: "/v1/entries/{internal}/relations?direction=both", outsiderStatus: 200, memberSees: true, idOnly: true},
		{name: "entry summary", method: "GET", path: "/v1/entries/{secret}/summary", outsiderStatus: 404},
		{name: "entry comments", method: "GET", path: "/v1/entries/{secret}/comments", outsiderStatus: 404, memberSees: true},
		{name: "entry cases", method: "GET", path: "/v1/entries/{secret}/cases", outsiderStatus: 404, memberSees: true},
		{name: "entry use_cases", method: "GET", path: "/v1/entries/{secret}/use_cases", outsiderStatus: 404},
		{name: "entry engagement", method: "GET", path: "/v1/entries/{secret}/engagement", outsiderStatus: 404, memberSees: true, idOnly: true},
		{name: "entry signals", method: "GET", path: "/v1/entries/{secret}/signals", outsiderStatus: 404, memberSees: true},

		// ---- search (candidate-stage filter; count too) ----
		{name: "search", method: "POST", path: "/v1/search",
			body: map[string]any{"query": leakMarker}, outsiderStatus: 200, memberSees: true},

		// ---- lookups ----
		{name: "lookup by-symptom", method: "POST", path: "/v1/lookup/by-symptom",
			body:           map[string]any{"symptom_description": leakMarker + " indexed symptom phrase"},
			outsiderStatus: 200, memberSees: true},
		{name: "lookup by-trigger", method: "POST", path: "/v1/lookup/by-trigger",
			body:           map[string]any{"trigger_description": leakMarker + " indexed trigger phrase"},
			outsiderStatus: 200, memberSees: true},
		{name: "lookup by-tags", method: "POST", path: "/v1/lookup/by-tags",
			body:           map[string]any{"tags": []string{"leakmarker-tag"}},
			outsiderStatus: 200, memberSees: true},
		{name: "lookup by-situation", method: "POST", path: "/v1/lookup/by-situation",
			body:           map[string]any{"situation_description": "wholly neutral situation description"},
			outsiderStatus: 200, memberSees: true},

		// ---- reflect (silent exclusion oracle) ----
		{name: "reflect", method: "POST", path: "/v1/reflect",
			body:           map[string]any{"entry_ids": []string{"{secretid}"}},
			outsiderStatus: 200, memberSees: true},

		// ---- tiers (entry bodies grouped by usage tier; the 3 planted
		// misleading cases put the secret entry in tier 4) ----
		{name: "tiers", method: "GET", path: "/v1/tiers?tier=4&limit=500", outsiderStatus: 200, memberSees: true},

		// ---- review queue + coordinator triage (misleading-heavy) ----
		{name: "review queue", method: "GET", path: "/v1/review-queue", outsiderStatus: 200, memberSees: true},
		{name: "coordinator triage", method: "GET", path: "/v1/librarian/coordinator/triage",
			outsiderStatus: 200, memberSees: true, idOnly: true},

		// ---- cross-entry comment feed ----
		{name: "recent comments", method: "GET", path: "/v1/comments/recent", outsiderStatus: 200, memberSees: true},

		// ---- librarian backlog (returns a FULL entry; detective has no
		// progress rows, so the member's oldest unprocessed = secret) ----
		{name: "backlog next", method: "GET", path: "/v1/librarian/backlog/next?role=detective",
			outsiderStatus: 200, memberSees: true},

		// ---- per-user projections ----
		{name: "my bookmarks", method: "GET", path: "/v1/me/bookmarks", outsiderStatus: 200, memberSees: true},
		{name: "my review-requests", method: "GET", path: "/v1/me/review-requests", outsiderStatus: 200, memberSees: true},

		// ---- cases + librarian progress ----
		{name: "case get", method: "GET", path: "/v1/cases/{case}", outsiderStatus: 404, memberSees: true},
		{name: "librarian progress", method: "GET", path: "/v1/librarian/progress?role=cataloger", outsiderStatus: 200, memberSees: true},

		// ---- open work ----
		{name: "open_work list", method: "GET", path: "/v1/open_work", outsiderStatus: 200, memberSees: true},

		// ---- writes: 404 for the outsider, never a leaked byte ----
		{name: "entry create in space", method: "POST", path: "/v1/entries",
			body: map[string]any{"project_id": "p-leak", "type": "lesson",
				"title": "outsider write", "body": "outsider write", "space_id": "{spaceid}"},
			outsiderStatus: 404},
		{name: "entry patch", method: "PATCH", path: "/v1/entries/{secret}",
			body: map[string]any{"title": "patched"}, header: map[string]string{"If-Match": "1"},
			outsiderStatus: 404},
		{name: "entry delete", method: "DELETE", path: "/v1/entries/{secret}", outsiderStatus: 404},
		{name: "entry index write", method: "POST", path: "/v1/entries/{secret}/index",
			body: map[string]any{"symptoms": []string{"outsider phrase"}}, outsiderStatus: 404},
		{name: "entry comment post", method: "POST", path: "/v1/entries/{secret}/comments",
			body: map[string]any{"body": "outsider comment"}, outsiderStatus: 404},
		{name: "comment patch", method: "PATCH", path: "/v1/comments/{comment}",
			body: map[string]any{"resolved": true}, outsiderStatus: 404},
		{name: "comment delete", method: "DELETE", path: "/v1/comments/{comment}", outsiderStatus: 404},
		{name: "bookmark put", method: "PUT", path: "/v1/entries/{secret}/bookmark", outsiderStatus: 404},
		{name: "bookmark delete", method: "DELETE", path: "/v1/entries/{secret}/bookmark", outsiderStatus: 404},
		{name: "feedback post", method: "POST", path: "/v1/feedback",
			body: map[string]any{"entry_id": "{secretid}", "signal": "helpful"}, outsiderStatus: 404},
		{name: "case create", method: "POST", path: "/v1/cases",
			body: map[string]any{"entry_id": "{secretid}"}, outsiderStatus: 404},
		{name: "case patch", method: "PATCH", path: "/v1/cases/{case}",
			body: map[string]any{"notes": "outsider note"}, outsiderStatus: 404},
		{name: "relation create", method: "POST", path: "/v1/relations",
			body:           map[string]any{"from_id": "{internalid}", "to_id": "{secretid}", "rel_type": "related"},
			outsiderStatus: 404},
		{name: "relation delete", method: "DELETE",
			path:           "/v1/relations?from_id={internal}&to_id={secret}&rel_type=see_also",
			outsiderStatus: 404},
		// Claim: a hidden entry takes exactly the missing-entry path
		// (409 "not tagged open") so probing cannot distinguish
		// restricted ids from nonexistent ones.
		{name: "open_work claim", method: "POST", path: "/v1/entries/{secret}/claim",
			body: map[string]any{"role": "cataloger", "instance_id": "i-leaktest"}, outsiderStatus: 409},
		{name: "open_work release", method: "POST", path: "/v1/entries/{secret}/release",
			body: map[string]any{"instance_id": "i-leaktest"}, outsiderStatus: 404},
		{name: "open_work mark_merged", method: "POST", path: "/v1/entries/{secret}/mark_merged",
			body: map[string]any{"instance_id": "i-leaktest"}, outsiderStatus: 404},
		{name: "librarian progress post", method: "POST", path: "/v1/librarian/progress",
			body:           map[string]any{"role": "cataloger", "entry_id": "{secretid}", "action": "summarize"},
			outsiderStatus: 404},

		// ================================================================
		// slice 3 — aggregates are single-space (migration 032)
		// ================================================================

		// ---- situations ----
		{name: "situations list", method: "GET", path: "/v1/situations", outsiderStatus: 200, memberSees: true},
		{name: "situation get", method: "GET", path: "/v1/situations/{situation}", outsiderStatus: 404, memberSees: true},
		{name: "situation create in space", method: "POST", path: "/v1/situations",
			body:           map[string]any{"description": "outsider situation", "space_id": "{spaceid}"},
			outsiderStatus: 404},
		{name: "situation add entry", method: "POST", path: "/v1/situations/{situation}/entries",
			body: map[string]any{"entry_id": "{secretid}"}, outsiderStatus: 404},
		{name: "situation remove entry", method: "DELETE",
			path: "/v1/situations/{situation}/entries/{secret}", outsiderStatus: 404},
		{name: "situation delete", method: "DELETE", path: "/v1/situations/{situation}", outsiderStatus: 404},

		// ---- incident clusters ----
		{name: "clusters list", method: "GET", path: "/v1/clusters?limit=500", outsiderStatus: 200, memberSees: true},
		{name: "cluster get", method: "GET", path: "/v1/clusters/{cluster}", outsiderStatus: 404, memberSees: true},
		{name: "cluster create in space", method: "POST", path: "/v1/clusters",
			body:           map[string]any{"title": "outsider cluster", "space_id": "{spaceid}"},
			outsiderStatus: 404},
		{name: "cluster add member", method: "POST", path: "/v1/clusters/{cluster}/members",
			body: map[string]any{"entry_id": "{secretid}"}, outsiderStatus: 404},
		{name: "cluster remove member", method: "DELETE",
			path: "/v1/clusters/{cluster}/members/{secret}", outsiderStatus: 404},
		{name: "cluster promote", method: "POST", path: "/v1/clusters/{cluster}/promote",
			body: map[string]any{"entry_id": "{secretid}"}, outsiderStatus: 404},
		{name: "cluster dismiss", method: "POST", path: "/v1/clusters/{cluster}/dismiss", outsiderStatus: 404},

		// ---- browse (hierarchy nodes) ----
		{name: "browse roots", method: "GET", path: "/v1/browse", outsiderStatus: 200, memberSees: true},
		{name: "browse node", method: "GET", path: "/v1/browse/{node}", outsiderStatus: 404, memberSees: true},
		{name: "browse node entries", method: "GET", path: "/v1/browse/{node}/entries",
			outsiderStatus: 404, memberSees: true},
		{name: "browse create in space", method: "POST", path: "/v1/browse",
			body:           map[string]any{"name": "outsider node", "space_id": "{spaceid}"},
			outsiderStatus: 404},
		{name: "browse attach entry", method: "POST", path: "/v1/browse/{node}/entries",
			body: map[string]any{"entry_id": "{secretid}"}, outsiderStatus: 404},
		{name: "browse detach entry", method: "DELETE",
			path: "/v1/browse/{node}/entries/{secret}", outsiderStatus: 404},
		{name: "browse delete node", method: "DELETE", path: "/v1/browse/{node}", outsiderStatus: 404},

		// ---- /index (cross-cutting groupings over entries + nodes) ----
		{name: "index by tag", method: "GET", path: "/v1/index?group_by=tag&limit=500",
			outsiderStatus: 200, memberSees: true, altMarker: "leakmarker-tag"},
		{name: "index by hierarchy", method: "GET", path: "/v1/index?group_by=hierarchy",
			outsiderStatus: 200, memberSees: true},
		{name: "index by recent", method: "GET", path: "/v1/index?group_by=recent", outsiderStatus: 200},

		// ---- use_cases ----
		{name: "use_cases list", method: "GET", path: "/v1/use_cases?limit=200", outsiderStatus: 200, memberSees: true},
		{name: "use_case get", method: "GET", path: "/v1/use_cases/{usecase}", outsiderStatus: 404, memberSees: true},
		{name: "use_case synthesis", method: "GET", path: "/v1/use_cases/{usecase}/synthesis",
			outsiderStatus: 404, memberSees: true},
		{name: "use_case create under hidden parent", method: "POST", path: "/v1/use_cases",
			body: map[string]any{"name_ja": "アウトサイダー", "name_en": "outsider usecase",
				"parent_id": "{usecaseid}"},
			outsiderStatus: 404},
		{name: "use_case link entry", method: "POST", path: "/v1/use_cases/{usecase}/entries",
			body: map[string]any{"entry_id": "{secretid}"}, outsiderStatus: 404},
		{name: "use_case unlink entry", method: "DELETE",
			path: "/v1/use_cases/{usecase}/entries/{secret}", outsiderStatus: 404},
		{name: "use_case set parent", method: "POST", path: "/v1/use_cases/{usecase}/parent",
			body: map[string]any{"parent_id": ""}, outsiderStatus: 404},
		{name: "use_case delete", method: "DELETE", path: "/v1/use_cases/{usecase}", outsiderStatus: 404},

		// ---- attachments ----
		{name: "attachment get", method: "GET", path: "/v1/attachments/{attachment}",
			outsiderStatus: 404, memberSees: true},
		{name: "attachment content", method: "GET", path: "/v1/attachments/{attachment}/content",
			outsiderStatus: 404, memberSees: true},

		// ---- findings (the entry-touching edge only; the finding row
		// itself is neutral external content — see the header) ----
		{name: "findings list", method: "GET", path: "/v1/librarian/findings?limit=500", outsiderStatus: 200},
		{name: "finding correlate", method: "POST", path: "/v1/librarian/findings/{finding}/correlate",
			body: map[string]any{"entry_id": "{secretid}"}, outsiderStatus: 404},

		// ---- backlog reprocess (silent exclusion, like /reflect) ----
		{name: "backlog reprocess", method: "POST", path: "/v1/librarian/backlog/reprocess",
			body:           map[string]any{"role": "cataloger", "entry_ids": []string{"{secretid}"}},
			outsiderStatus: 200},

		// ================================================================
		// slice 4 — talk threads, chat search, librarian tasks
		// ================================================================

		// ---- threads (intent=talk is owner-only; coordination shared) ----
		{name: "threads list", method: "GET", path: "/v1/librarian/threads?limit=500",
			outsiderStatus: 200, memberSees: true},
		{name: "talk thread messages", method: "GET",
			path: "/v1/librarian/threads/{talkthread}/messages", outsiderStatus: 404, memberSees: true},
		{name: "talk thread close", method: "POST",
			path: "/v1/librarian/threads/{talkthread}/close", outsiderStatus: 404},
		{name: "talk thread chat post", method: "POST", path: "/v1/librarian/chat",
			body: map[string]any{"thread_id": "{talkthreadid}", "author_role": "human",
				"content": "outsider message"},
			outsiderStatus: 404},

		// ---- search with include_chat (chat_results field) ----
		{name: "search include_chat", method: "POST", path: "/v1/search",
			body:           map[string]any{"query": leakMarker, "include_chat": true},
			outsiderStatus: 200, memberSees: true},

		// ---- librarian tasks (space stamped at open-work claim, 033) ----
		{name: "librarian tasks list", method: "GET", path: "/v1/librarian/tasks?limit=500",
			outsiderStatus: 200, memberSees: true},
		{name: "task claim", method: "POST", path: "/v1/librarian/tasks/{task}/claim",
			body: map[string]any{"instance_id": "i-leaktest"}, outsiderStatus: 404},
		{name: "task complete", method: "POST", path: "/v1/librarian/tasks/{task}/complete",
			body: map[string]any{"success": true}, outsiderStatus: 404},
	}
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

// TestSpaceEntryCreate covers POST /v1/entries space_id semantics:
// default 'internal', member can write into a granted space, outsider
// and unknown spaces are indistinguishable 404s.
func TestSpaceEntryCreate(t *testing.T) {
	f := newLeakFixture(t)

	t.Run("default internal", func(t *testing.T) {
		s, raw := doJSON(t, "POST", f.base+"/v1/entries", f.outsiderTok, map[string]any{
			"project_id": "p-leak", "type": "lesson", "title": "t", "body": "b",
		}, nil)
		if s != 201 {
			t.Fatalf("status=%d body=%s", s, raw)
		}
		var got struct {
			SpaceID string `json:"space_id"`
		}
		if err := jsonUnmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.SpaceID != store.SpaceInternal {
			t.Errorf("space_id = %q, want internal", got.SpaceID)
		}
	})

	t.Run("member writes into granted space", func(t *testing.T) {
		s, raw := doJSON(t, "POST", f.base+"/v1/entries", f.memberTok, map[string]any{
			"project_id": "p-leak", "type": "lesson", "title": "in space", "body": "b",
			"space_id": f.spaceID,
		}, nil)
		if s != 201 {
			t.Fatalf("status=%d body=%s", s, raw)
		}
		var got struct {
			SpaceID string `json:"space_id"`
		}
		if err := jsonUnmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.SpaceID != f.spaceID {
			t.Errorf("space_id = %q, want %q", got.SpaceID, f.spaceID)
		}
	})

	t.Run("unknown space is 404 for member too", func(t *testing.T) {
		s, _ := doJSON(t, "POST", f.base+"/v1/entries", f.memberTok, map[string]any{
			"project_id": "p-leak", "type": "lesson", "title": "t", "body": "b",
			"space_id": "sp-doesnotexist",
		}, nil)
		if s != 404 {
			t.Errorf("unknown space: status=%d, want 404", s)
		}
	})

	t.Run("member can write into own personal space", func(t *testing.T) {
		s, _ := doJSON(t, "POST", f.base+"/v1/entries", f.memberTok, map[string]any{
			"project_id": "p-leak", "type": "lesson", "title": "personal", "body": "b",
			"space_id": store.PersonalSpaceID("u-member"),
		}, nil)
		if s != 201 {
			t.Errorf("personal space write: status=%d, want 201", s)
		}
	})

	t.Run("cannot write into someone else's personal space", func(t *testing.T) {
		s, _ := doJSON(t, "POST", f.base+"/v1/entries", f.outsiderTok, map[string]any{
			"project_id": "p-leak", "type": "lesson", "title": "t", "body": "b",
			"space_id": store.PersonalSpaceID("u-member"),
		}, nil)
		if s != 404 {
			t.Errorf("foreign personal space: status=%d, want 404", s)
		}
	})
}

// TestSpaceMemberHarmlessWrites: a member's writes against the
// restricted entry succeed (the visibility gate lets insiders through).
func TestSpaceMemberHarmlessWrites(t *testing.T) {
	f := newLeakFixture(t)
	if s, raw := doJSON(t, "POST", f.base+"/v1/feedback", f.memberTok,
		map[string]any{"entry_id": f.secretID, "signal": "helpful"}, nil); s != 201 {
		t.Errorf("member feedback: status=%d body=%s", s, raw)
	}
	if s, raw := doJSON(t, "POST", f.base+"/v1/entries/"+f.secretID+"/comments", f.memberTok,
		map[string]any{"body": "member comment"}, nil); s != 201 {
		t.Errorf("member comment: status=%d body=%s", s, raw)
	}
	if s, raw := doJSON(t, "POST", f.expand("/v1/librarian/findings/{finding}/correlate"), f.memberTok,
		map[string]any{"entry_id": f.secretID}, nil); s != 204 {
		t.Errorf("member correlate: status=%d body=%s", s, raw)
	}
	// Slice 4: the owner keeps full use of their own talk thread, and a
	// space member can claim / complete a task living in the space.
	if s, raw := doJSON(t, "POST", f.base+"/v1/librarian/chat", f.memberTok,
		map[string]any{"thread_id": f.talkThreadID, "author_role": "human",
			"content": "owner message"}, nil); s != 201 {
		t.Errorf("owner talk post: status=%d body=%s", s, raw)
	}
	if s, raw := doJSON(t, "POST", f.expand("/v1/librarian/tasks/{task}/claim"), f.memberTok,
		map[string]any{"instance_id": "i-member"}, nil); s != 204 {
		t.Errorf("member task claim: status=%d body=%s", s, raw)
	}
	if s, raw := doJSON(t, "POST", f.expand("/v1/librarian/tasks/{task}/complete"), f.memberTok,
		map[string]any{"success": true}, nil); s != 204 {
		t.Errorf("member task complete: status=%d body=%s", s, raw)
	}
	if s, raw := doJSON(t, "POST", f.expand("/v1/librarian/threads/{talkthread}/close"), f.memberTok,
		nil, nil); s != 204 {
		t.Errorf("owner talk close: status=%d body=%s", s, raw)
	}
}

// TestAggregateSingleSpace pins the slice-3 invariant beyond pure
// visibility: an aggregate holds entries from its OWN space only, and
// violations 404 even for a caller who can see both sides.
func TestAggregateSingleSpace(t *testing.T) {
	f := newLeakFixture(t)

	// Positive: member links the restricted entry into a restricted-
	// space aggregate (same space).
	if s, raw := doJSON(t, "POST", f.expand("/v1/situations/{situation}/entries"), f.memberTok,
		map[string]any{"entry_id": f.secretID}, nil); s != 204 {
		t.Errorf("same-space link: status=%d body=%s", s, raw)
	}

	// Cross-space violation: an internal cluster cannot hold the
	// restricted entry, even though the member sees both.
	s, raw := doJSON(t, "POST", f.base+"/v1/clusters", f.memberTok,
		map[string]any{"title": "internal cluster"}, nil)
	if s != 201 {
		t.Fatalf("create internal cluster: %d %s", s, raw)
	}
	var cl struct{ ID string }
	if err := jsonUnmarshal(raw, &cl); err != nil {
		t.Fatal(err)
	}
	if s, _ := doJSON(t, "POST", f.base+"/v1/clusters/"+cl.ID+"/members", f.memberTok,
		map[string]any{"entry_id": f.secretID}, nil); s != 404 {
		t.Errorf("internal cluster + restricted entry: status=%d, want 404", s)
	}
	// ...and the mirror: a restricted cluster cannot hold an internal entry.
	if s, _ := doJSON(t, "POST", f.expand("/v1/clusters/{cluster}/members"), f.memberTok,
		map[string]any{"entry_id": f.internalID}, nil); s != 404 {
		t.Errorf("restricted cluster + internal entry: status=%d, want 404", s)
	}

	// Creation mirrors POST /entries: member creates in a granted space,
	// default is internal, unknown space is 404 for everyone.
	s, raw = doJSON(t, "POST", f.base+"/v1/situations", f.memberTok,
		map[string]any{"description": "situation in space", "space_id": f.spaceID}, nil)
	if s != 201 {
		t.Fatalf("member situation in space: %d %s", s, raw)
	}
	var sit struct {
		SpaceID string `json:"space_id"`
	}
	if err := jsonUnmarshal(raw, &sit); err != nil {
		t.Fatal(err)
	}
	if sit.SpaceID != f.spaceID {
		t.Errorf("situation space_id = %q, want %q", sit.SpaceID, f.spaceID)
	}
	s, raw = doJSON(t, "POST", f.base+"/v1/situations", f.memberTok,
		map[string]any{"description": "defaulted situation"}, nil)
	if s != 201 {
		t.Fatalf("member situation default: %d %s", s, raw)
	}
	if err := jsonUnmarshal(raw, &sit); err != nil {
		t.Fatal(err)
	}
	if sit.SpaceID != store.SpaceInternal {
		t.Errorf("defaulted situation space_id = %q, want internal", sit.SpaceID)
	}
	if s, _ := doJSON(t, "POST", f.base+"/v1/situations", f.memberTok,
		map[string]any{"description": "x", "space_id": "sp-doesnotexist"}, nil); s != 404 {
		t.Errorf("unknown space: status=%d, want 404", s)
	}
}

// postMultipartAttachment uploads one small file with the given form
// fields; returns status + body.
func postMultipartAttachment(t *testing.T, url, tok string, fields map[string]string) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	fw, err := mw.CreateFormFile("file", "upload.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte("attachment body bytes")); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", url, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// TestAttachmentSpaceUpload covers the upload side of the attachment
// space contract: optional space_id form field, default internal,
// hidden space indistinguishable from a missing one.
func TestAttachmentSpaceUpload(t *testing.T) {
	f := newLeakFixture(t)

	s, raw := postMultipartAttachment(t, f.base+"/v1/attachments", f.memberTok, map[string]string{
		"project_id": "p-leak", "role": "log", "caption": "member upload", "space_id": f.spaceID,
	})
	if s != 201 {
		t.Fatalf("member upload into space: %d %s", s, raw)
	}
	var att struct {
		SpaceID string `json:"space_id"`
	}
	if err := jsonUnmarshal(raw, &att); err != nil {
		t.Fatal(err)
	}
	if att.SpaceID != f.spaceID {
		t.Errorf("attachment space_id = %q, want %q", att.SpaceID, f.spaceID)
	}

	s, raw = postMultipartAttachment(t, f.base+"/v1/attachments", f.memberTok, map[string]string{
		"project_id": "p-leak", "role": "log", "caption": "defaulted upload",
	})
	if s != 201 {
		t.Fatalf("member default upload: %d %s", s, raw)
	}
	if err := jsonUnmarshal(raw, &att); err != nil {
		t.Fatal(err)
	}
	if att.SpaceID != store.SpaceInternal {
		t.Errorf("defaulted attachment space_id = %q, want internal", att.SpaceID)
	}

	if s, _ := postMultipartAttachment(t, f.base+"/v1/attachments", f.outsiderTok, map[string]string{
		"project_id": "p-leak", "role": "log", "caption": "outsider upload", "space_id": f.spaceID,
	}); s != 404 {
		t.Errorf("outsider upload into hidden space: status=%d, want 404", s)
	}
}

// TestBacklogReprocessVisibility: /librarian/backlog/reprocess silently
// excludes ids outside the caller's view — the cleared count never
// confirms a hidden entry, and a non-member cannot force re-processing.
func TestBacklogReprocessVisibility(t *testing.T) {
	f := newLeakFixture(t)
	body := map[string]any{"role": "cataloger", "entry_ids": []string{f.secretID}}

	s, raw := doJSON(t, "POST", f.base+"/v1/librarian/backlog/reprocess", f.outsiderTok, body, nil)
	if s != 200 {
		t.Fatalf("outsider reprocess: %d %s", s, raw)
	}
	var out struct {
		Cleared int `json:"cleared"`
	}
	if err := jsonUnmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Cleared != 0 {
		t.Errorf("outsider cleared = %d, want 0 (silent exclusion)", out.Cleared)
	}

	s, raw = doJSON(t, "POST", f.base+"/v1/librarian/backlog/reprocess", f.memberTok, body, nil)
	if s != 200 {
		t.Fatalf("member reprocess: %d %s", s, raw)
	}
	if err := jsonUnmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Cleared != 1 {
		t.Errorf("member cleared = %d, want 1 (the fixture's cataloger progress row)", out.Cleared)
	}
}
