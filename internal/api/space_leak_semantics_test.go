package api

// Space write-semantics tests that go beyond the pure visibility matrix
// (fixture + runner in space_leak_test.go): creation into spaces, the
// member/owner positive paths, the aggregate single-space contract, the
// attachment upload contract, and reprocess silent-exclusion counts.

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

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

// TestThreadRelatedEntriesVisibility (issue #103): related_entries is a
// JSON array of entry ids (skill.md) and every id is validated like any
// other entry reference — a member may link a granted-space entry, an
// outsider's invisible id and a nonexistent id produce byte-identical
// 404s (no existence oracle), and message-level related_entries follow
// the same contract as thread-level ones.
func TestThreadRelatedEntriesVisibility(t *testing.T) {
	f := newLeakFixture(t)
	threadsURL := f.base + "/v1/librarian/threads"
	chatURL := f.base + "/v1/librarian/chat"

	t.Run("member links a granted-space entry", func(t *testing.T) {
		s, raw := doJSON(t, "POST", threadsURL, f.memberTok, map[string]any{
			"title": "member thread", "related_entries": `["` + f.secretID + `"]`,
		}, nil)
		if s != 201 {
			t.Fatalf("member open: %d %s", s, raw)
		}
		s, raw = doJSON(t, "POST", chatURL, f.memberTok, map[string]any{
			"thread_id": f.coordThreadID, "author_role": "human",
			"content": "member message", "related_entries": `["` + f.secretID + `"]`,
		}, nil)
		if s != 201 {
			t.Fatalf("member chat post: %d %s", s, raw)
		}
	})

	t.Run("invisible == nonexistent (byte-identical 404)", func(t *testing.T) {
		sHidden, rawHidden := doJSON(t, "POST", threadsURL, f.outsiderTok, map[string]any{
			"title": "probe", "related_entries": `["` + f.secretID + `"]`,
		}, nil)
		sMissing, rawMissing := doJSON(t, "POST", threadsURL, f.outsiderTok, map[string]any{
			"title": "probe", "related_entries": `["T-NOSUCH"]`,
		}, nil)
		if sHidden != 404 || sMissing != 404 {
			t.Fatalf("outsider open: hidden=%d missing=%d", sHidden, sMissing)
		}
		if string(rawHidden) != string(rawMissing) {
			t.Errorf("404 bodies differ (existence oracle):\nhidden:  %s\nmissing: %s",
				rawHidden, rawMissing)
		}
	})

	t.Run("nonexistent id is 404 for the member too", func(t *testing.T) {
		if s, raw := doJSON(t, "POST", threadsURL, f.memberTok, map[string]any{
			"title": "probe", "related_entries": `["T-NOSUCH"]`,
		}, nil); s != 404 {
			t.Errorf("member open with missing id: %d %s", s, raw)
		}
		if s, raw := doJSON(t, "POST", chatURL, f.memberTok, map[string]any{
			"thread_id": f.coordThreadID, "author_role": "human",
			"content": "m", "related_entries": `["T-NOSUCH"]`,
		}, nil); s != 404 {
			t.Errorf("member chat post with missing id: %d %s", s, raw)
		}
	})

	t.Run("non-JSON related_entries is rejected", func(t *testing.T) {
		if s, raw := doJSON(t, "POST", threadsURL, f.memberTok, map[string]any{
			"title": "probe", "related_entries": "not-json",
		}, nil); s != 400 {
			t.Errorf("member open with junk related_entries: %d %s", s, raw)
		}
	})

	// Cap: each id costs a visibility lookup, so the array length is
	// bounded — an oversized array is rejected before any lookup runs.
	t.Run("oversized related_entries is rejected", func(t *testing.T) {
		ids := make([]string, 51)
		for i := range ids {
			ids[i] = "T-DEADBEEF"
		}
		enc, _ := json.Marshal(ids)
		if s, raw := doJSON(t, "POST", threadsURL, f.memberTok, map[string]any{
			"title": "probe", "related_entries": string(enc),
		}, nil); s != 400 {
			t.Errorf("member open with 51 related entries: %d %s", s, raw)
		}
	})
}
