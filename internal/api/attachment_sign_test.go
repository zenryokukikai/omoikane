package api

// Tests for expiring signed attachment URLs (issue #104 slice G4).
// The signature is an ADDITIONAL grant: every negative case must fall
// through to the normal auth path (401 for anonymous callers, never a
// distinct error that would reveal whether the attachment exists).

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/config"
	"github.com/zenryokukikai/omoikane/internal/enrich"
	"github.com/zenryokukikai/omoikane/internal/store"
)

// testServerSigning is testServer plus a signing key ("" = feature
// off) and returns the Handler so tests can call SignAttachmentURL.
func testServerSigning(t *testing.T, key string) (base, tok string, st *store.Store, h *Handler) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.CreateUser(context.Background(),
		&store.User{ID: "admin", Name: "admin", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	tok, err = st.CreateToken(context.Background(), "admin", "test",
		[]string{"read", "write", "admin"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h = &Handler{
		Store:       st,
		Enricher:    enrich.New("", "", "", "", logger),
		SecretsMode: config.SecretsEnforce,
		Logger:      logger,
	}
	if key != "" {
		h.URLSigningKey = []byte(key)
	}
	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(Recoverer(logger))
	r.Use(Audit(st, logger))
	h.Mount(r)

	srv := httptest.NewServer(r)
	t.Cleanup(func() {
		srv.Close()
		_ = st.Close()
	})
	return srv.URL, tok, st, h
}

// uploadSignFixture uploads one attachment and returns its id.
func uploadSignFixture(t *testing.T, base, tok string, payload []byte) string {
	t.Helper()
	status, body := uploadAttachment(t, base, tok, map[string]string{
		"project_id": "demo",
		"role":       "chart",
		"caption":    "signed url fixture",
	}, "file", "chart.png", "image/png", payload)
	if status != http.StatusCreated {
		t.Fatalf("upload: %d %s", status, body)
	}
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &a); err != nil || a.ID == "" {
		t.Fatalf("decode upload: %v %s", err, body)
	}
	return a.ID
}

// getAnon fetches url with NO Authorization header.
func getAnon(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestSignedURLServesWithoutAuth(t *testing.T) {
	base, tok, st, h := testServerSigning(t, "test-signing-key")
	seedAttachmentAPIFixture(t, st)
	payload := []byte("PNG\x89SIGNED")
	id := uploadSignFixture(t, base, tok, payload)

	path, err := h.SignAttachmentURL(id, 0) // 0 → DefaultSignedURLTTL
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !strings.HasPrefix(path, "/v1/attachments/"+id+"/content?exp=") {
		t.Fatalf("unexpected path shape: %s", path)
	}

	resp := getAnon(t, base+path)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("signed GET: %d %s", resp.StatusCode, body)
	}
	if string(body) != string(payload) {
		t.Errorf("bytes mismatch: got %q", body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "private") {
		t.Errorf("Cache-Control = %q, want private", cc)
	}
	if xo := resp.Header.Get("X-Content-Type-Options"); xo != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", xo)
	}
}

func TestSignedURLExpiredFailsClosed(t *testing.T) {
	base, tok, st, h := testServerSigning(t, "test-signing-key")
	seedAttachmentAPIFixture(t, st)
	id := uploadSignFixture(t, base, tok, []byte("expired-bytes"))

	exp := time.Now().Add(-1 * time.Minute).Unix() // correctly signed, but past
	sig := hex.EncodeToString(attachmentSig(h.URLSigningKey, id, exp))
	url := fmt.Sprintf("%s/v1/attachments/%s/content?exp=%d&sig=%s", base, id, exp, sig)

	resp := getAnon(t, url)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired sig: %d, want 401 (fall through to auth)", resp.StatusCode)
	}
}

func TestSignedURLTamperedFailsClosed(t *testing.T) {
	base, tok, st, h := testServerSigning(t, "test-signing-key")
	seedAttachmentAPIFixture(t, st)
	id := uploadSignFixture(t, base, tok, []byte("tamper-bytes"))

	path, err := h.SignAttachmentURL(id, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Flip the last hex nibble of the signature.
	last := path[len(path)-1]
	flip := byte('0')
	if last == '0' {
		flip = '1'
	}
	tampered := path[:len(path)-1] + string(flip)

	resp := getAnon(t, base+tampered)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tampered sig: %d, want 401", resp.StatusCode)
	}

	// Tampering exp (extending lifetime) must also fail: the exp is
	// bound into the MAC.
	exp2 := time.Now().Add(24 * time.Hour).Unix()
	sigOld := path[strings.Index(path, "sig=")+4:]
	url2 := fmt.Sprintf("%s/v1/attachments/%s/content?exp=%d&sig=%s", base, id, exp2, sigOld)
	resp2 := getAnon(t, url2)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("exp-tampered sig: %d, want 401", resp2.StatusCode)
	}
}

func TestSignedURLDoesNotOpenOtherAttachment(t *testing.T) {
	base, tok, st, h := testServerSigning(t, "test-signing-key")
	seedAttachmentAPIFixture(t, st)
	idA := uploadSignFixture(t, base, tok, []byte("attachment-A"))
	idB := uploadSignFixture(t, base, tok, []byte("attachment-B"))

	pathA, err := h.SignAttachmentURL(idA, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Reuse A's exp+sig query on B's path.
	query := pathA[strings.Index(pathA, "?"):]
	resp := getAnon(t, base+"/v1/attachments/"+idB+"/content"+query)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("A's sig on B: %d, want 401", resp.StatusCode)
	}
}

func TestSignedURLFeatureOff(t *testing.T) {
	base, tok, st, h := testServerSigning(t, "") // no key: feature disabled
	seedAttachmentAPIFixture(t, st)
	id := uploadSignFixture(t, base, tok, []byte("feature-off"))

	// Issuance errors.
	if _, err := h.SignAttachmentURL(id, time.Minute); !errors.Is(err, ErrSigningDisabled) {
		t.Fatalf("sign with no key: err = %v, want ErrSigningDisabled", err)
	}

	// A signature minted with SOME key never grants when the server
	// has none configured.
	exp := time.Now().Add(time.Minute).Unix()
	sig := hex.EncodeToString(attachmentSig([]byte("attacker-key"), id, exp))
	url := fmt.Sprintf("%s/v1/attachments/%s/content?exp=%d&sig=%s", base, id, exp, sig)
	resp := getAnon(t, url)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("feature off: %d, want 401", resp.StatusCode)
	}
}

func TestSignedURLNormalAuthUnaffected(t *testing.T) {
	base, tok, st, h := testServerSigning(t, "test-signing-key")
	seedAttachmentAPIFixture(t, st)
	payload := []byte("authed-bytes")
	id := uploadSignFixture(t, base, tok, payload)

	// Plain authed GET (no sig params) works exactly as before.
	req, _ := http.NewRequest(http.MethodGet, base+"/v1/attachments/"+id+"/content", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != string(payload) {
		t.Fatalf("authed GET: %d %q", resp.StatusCode, body)
	}

	// A bad signature on an AUTHED request falls through to auth and
	// still succeeds — signatures never restrict.
	exp := time.Now().Add(-time.Hour).Unix()
	sig := hex.EncodeToString(attachmentSig(h.URLSigningKey, id, exp))
	req2, _ := http.NewRequest(http.MethodGet,
		fmt.Sprintf("%s/v1/attachments/%s/content?exp=%d&sig=%s", base, id, exp, sig), nil)
	req2.Header.Set("Authorization", "Bearer "+tok)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("authed GET with stale sig: %d, want 200", resp2.StatusCode)
	}

	// Anonymous with no sig params: still 401 (baseline unchanged).
	resp3 := getAnon(t, base+"/v1/attachments/"+id+"/content")
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anon no-sig GET: %d, want 401", resp3.StatusCode)
	}
}
