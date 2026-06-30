package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// uploadAttachment is a small helper that builds the multipart body
// and POSTs it. Returns status, body bytes, decoded attachment.
func uploadAttachment(t *testing.T, base, tok string, fields map[string]string, fileField, filename, contentType string, payload []byte) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if fileField != "" {
		// Build the file part header by hand so we can set Content-Type
		// (mw.CreateFormFile hardcodes application/octet-stream).
		hdr := make(map[string][]string)
		hdr["Content-Disposition"] = []string{`form-data; name="` + fileField +
			`"; filename="` + filename + `"`}
		if contentType != "" {
			hdr["Content-Type"] = []string{contentType}
		}
		w, err := mw.CreatePart(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	_ = mw.Close()

	req, _ := http.NewRequest(http.MethodPost, base+"/v1/attachments", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func seedAttachmentAPIFixture(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateProject(ctx, &store.Project{ID: "demo", Name: "Demo"}); err != nil {
		t.Fatal(err)
	}
}

// Happy path: upload → 201 with row metadata → GET metadata → GET
// content → same bytes back with correct Content-Type.
func TestPostGetAttachmentEndToEnd(t *testing.T) {
	base, tok, st := testServer(t)
	seedAttachmentAPIFixture(t, st)
	payload := []byte("PNG\x89BYTES")

	status, body := uploadAttachment(t, base, tok, map[string]string{
		"project_id": "demo",
		"role":       "chart",
		"caption":    "test chart",
	}, "file", "chart.png", "image/png", payload)
	if status != http.StatusCreated {
		t.Fatalf("upload: %d %s", status, body)
	}
	var a struct {
		ID         string `json:"id"`
		ProjectID  string `json:"project_id"`
		Mime       string `json:"mime"`
		Filename   string `json:"filename"`
		SizeBytes  int64  `json:"size_bytes"`
		Hash       string `json:"hash"`
		Role       string `json:"role"`
		Caption    string `json:"caption"`
		UploadedBy string `json:"uploaded_by"`
	}
	if err := json.Unmarshal(body, &a); err != nil {
		t.Fatalf("decode: %v: %s", err, body)
	}
	if !strings.HasPrefix(a.ID, "a-") || a.Mime != "image/png" || a.Role != "chart" ||
		a.Caption != "test chart" || a.SizeBytes != int64(len(payload)) {
		t.Fatalf("metadata: %+v", a)
	}
	expectedHash := sha256.Sum256(payload)
	if a.Hash != hex.EncodeToString(expectedHash[:]) {
		t.Errorf("hash mismatch")
	}

	// GET metadata
	req, _ := http.NewRequest(http.MethodGet, base+"/v1/attachments/"+a.ID, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("get metadata: %d", resp.StatusCode)
	}

	// GET content
	req, _ = http.NewRequest(http.MethodGet, base+"/v1/attachments/"+a.ID+"/content", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ = http.DefaultClient.Do(req)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("get content: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type: got %q want image/png", got)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, payload) {
		t.Errorf("content roundtrip mismatch")
	}
}

func TestPostAttachmentRejectsBlankCaption(t *testing.T) {
	base, tok, st := testServer(t)
	seedAttachmentAPIFixture(t, st)
	status, _ := uploadAttachment(t, base, tok, map[string]string{
		"project_id": "demo",
		"role":       "chart",
		// caption missing
	}, "file", "x.png", "image/png", []byte("x"))
	if status != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", status)
	}
}

func TestPostAttachmentRejectsUnknownRole(t *testing.T) {
	base, tok, st := testServer(t)
	seedAttachmentAPIFixture(t, st)
	status, body := uploadAttachment(t, base, tok, map[string]string{
		"project_id": "demo",
		"role":       "freeform",
		"caption":    "x",
	}, "file", "x.png", "image/png", []byte("x"))
	if status != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", status)
	}
	if !strings.Contains(string(body), "valid") {
		t.Errorf("error should list valid roles: %s", body)
	}
}

func TestPostAttachmentRejectsMissingProject(t *testing.T) {
	base, tok, _ := testServer(t)
	// no seeding — project "demo" doesn't exist
	status, _ := uploadAttachment(t, base, tok, map[string]string{
		"project_id": "demo",
		"role":       "chart",
		"caption":    "x",
	}, "file", "x.png", "image/png", []byte("x"))
	if status != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", status)
	}
}

// GET /v1/attachments/{id}/content must accept the `?token=` query
// param so dashboard-rendered <img>/<video> src URLs can authenticate
// without a session cookie (the cookie path works too — this is the
// alternative for users on the `?token=` dashboard mode).
func TestGetAttachmentContentAcceptsQueryToken(t *testing.T) {
	base, tok, st := testServer(t)
	seedAttachmentAPIFixture(t, st)
	_, body := uploadAttachment(t, base, tok, map[string]string{
		"project_id": "demo", "role": "chart", "caption": "x",
	}, "file", "x.png", "image/png", []byte("PNG"))
	var a struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &a)

	// Same URL, no Authorization header — only ?token=.
	resp, _ := http.Get(base + "/v1/attachments/" + a.ID + "/content?token=" + tok)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("query-token GET should succeed, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type: got %q", got)
	}
	// metadata endpoint same
	resp2, _ := http.Get(base + "/v1/attachments/" + a.ID + "?token=" + tok)
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("query-token metadata GET should succeed, got %d", resp2.StatusCode)
	}
}

func TestPostAttachmentRequiresAuth(t *testing.T) {
	base, _, st := testServer(t)
	seedAttachmentAPIFixture(t, st)
	status, _ := uploadAttachment(t, base, "", map[string]string{
		"project_id": "demo",
		"role":       "chart",
		"caption":    "x",
	}, "file", "x.png", "image/png", []byte("x"))
	if status != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", status)
	}
}

// Body exceeding the per-route AttachmentMaxBytes cap → 413. We can't
// easily test the cap end-to-end because Handler.AttachmentMaxBytes
// is 0 in the test setup (treated as "use the 50MB default" inside
// the route). To make this test meaningful, we set it to something
// tiny via a fresh server setup.
func TestPostAttachmentEnforcesSizeCap(t *testing.T) {
	// Reuse testServer but tighten the cap before installing.
	// testServer doesn't accept overrides, so we do a fresh build of
	// the test stack inline. Cribbed from api_test.go's testServer.
	base, tok, st := testServerWithAttachmentCap(t, 64)
	seedAttachmentAPIFixture(t, st)
	payload := bytes.Repeat([]byte("x"), 1000) // way over 64-byte cap
	status, _ := uploadAttachment(t, base, tok, map[string]string{
		"project_id": "demo",
		"role":       "chart",
		"caption":    "x",
	}, "file", "x.png", "image/png", payload)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("want 413, got %d", status)
	}
}

// Roles list in the standard vocab should be agent-discoverable via
// the error body when an unknown role is sent. Locks the
// agent-readability contract.
func TestUnknownRoleErrorLeaksVocab(t *testing.T) {
	base, tok, st := testServer(t)
	seedAttachmentAPIFixture(t, st)
	_, body := uploadAttachment(t, base, tok, map[string]string{
		"project_id": "demo",
		"role":       "wrong",
		"caption":    "x",
	}, "file", "x.png", "image/png", []byte("x"))
	for _, want := range []string{"chart", "screenshot", "before", "after"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("vocab role %q not in error body: %s", want, body)
		}
	}
}
