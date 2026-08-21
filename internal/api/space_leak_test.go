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
// Routes outside this slice (chat, SSE/webhooks, aggregates such as
// situations/clusters/use_cases/browse/index, attachments, dashboard)
// are covered by their own slices — add their rows here as those slices
// land.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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
	st          *store.Store

	spaceID    string
	secretID   string // restricted-space entry (carries leakMarker)
	internalID string // internal-space entry with a relation to secretID
	caseID     string // usage case on secretID (trigger_query carries marker)
	commentID  string // comment on secretID (@mentions both users)
}

func newLeakFixture(t *testing.T) *leakFixture {
	t.Helper()
	base, _, st := testServer(t)
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
	// the lookup must not return the entry id to a non-member.
	sitID, err := st.CreateSituation(ctx, &store.Situation{
		Description: "wholly neutral situation description",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LinkEntryToSituation(ctx, sitID, secretID, 1.0, ""); err != nil {
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

	return &leakFixture{
		base: base, memberTok: memberTok, outsiderTok: outsiderTok, st: st,
		spaceID: sp.ID, secretID: secretID, internalID: internalID,
		caseID: caseID, commentID: comment.ID,
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
}

func (f *leakFixture) expand(p string) string {
	r := strings.NewReplacer(
		"{secret}", f.secretID,
		"{internal}", f.internalID,
		"{case}", f.caseID,
		"{comment}", f.commentID,
		"{space}", f.spaceID,
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

		// ---- tiers (entry bodies grouped by usage tier) ----
		{name: "tiers", method: "GET", path: "/v1/tiers?tier=3&limit=500", outsiderStatus: 200, memberSees: true},

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
}
