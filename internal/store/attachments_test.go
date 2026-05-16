//go:build sqlite_fts5

package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedProjectAndUser sets up a project and an uploading user so the
// attachment fixtures don't fail their FK constraints.
func seedAttachmentFixture(t *testing.T) (*Store, string, string) {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, &User{ID: "uploader", Name: "uploader", Role: "agent"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateProject(ctx, &Project{ID: "p", Name: "p"}); err != nil {
		t.Fatal(err)
	}
	return s, "p", "uploader"
}

// Happy path: caption + role + non-empty content → row inserted, blob
// written under <dataDir>/attachments/<aa>/<bb>/<hash>, size/hash
// populated on return, content readable back via OpenAttachmentContent.
func TestCreateAttachmentHappyPath(t *testing.T) {
	s, projectID, userID := seedAttachmentFixture(t)
	ctx := context.Background()

	payload := []byte("hello, world — this is a test png pretending\n")
	a, err := s.CreateAttachment(ctx, CreateAttachmentParams{
		ProjectID:  projectID,
		Mime:       "image/png",
		Filename:   "hello.png",
		Role:       "chart",
		Caption:    "test chart",
		UploadedBy: userID,
		Content:    bytes.NewReader(payload),
		MaxBytes:   1 << 20,
	})
	if err != nil {
		t.Fatalf("CreateAttachment: %v", err)
	}
	if !strings.HasPrefix(a.ID, "a-") {
		t.Errorf("id format: %q", a.ID)
	}
	if a.SizeBytes != int64(len(payload)) {
		t.Errorf("size: got %d want %d", a.SizeBytes, len(payload))
	}
	expectedHash := sha256.Sum256(payload)
	if a.Hash != hex.EncodeToString(expectedHash[:]) {
		t.Errorf("hash mismatch: got %s", a.Hash)
	}
	if a.Mime != "image/png" || a.Filename != "hello.png" || a.Role != "chart" {
		t.Errorf("metadata: %+v", a)
	}

	// blob lives at the expected hash-based path
	expectedPath := filepath.Join("attachments", a.Hash[:2], a.Hash[2:4], a.Hash)
	if a.StoragePath != expectedPath {
		t.Errorf("storage_path: got %q want %q", a.StoragePath, expectedPath)
	}
	full := filepath.Join(s.DataDir(), expectedPath)
	if _, err := os.Stat(full); err != nil {
		t.Errorf("blob missing on disk: %v", err)
	}

	// content roundtrip
	rc, _, err := s.OpenAttachmentContent(ctx, a.ID)
	if err != nil {
		t.Fatalf("OpenAttachmentContent: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, payload) {
		t.Errorf("content roundtrip mismatch")
	}
}

// Caption empty / whitespace-only → rejected. The whole agent-readable
// contract rests on caption being meaningful, so this is a strict
// invariant.
func TestCreateAttachmentRejectsBlankCaption(t *testing.T) {
	s, projectID, userID := seedAttachmentFixture(t)

	for _, caption := range []string{"", "   ", "\n\t  "} {
		_, err := s.CreateAttachment(context.Background(), CreateAttachmentParams{
			ProjectID: projectID, Mime: "image/png", Role: "chart",
			Caption: caption, UploadedBy: userID,
			Content:  bytes.NewReader([]byte("x")), MaxBytes: 1024,
		})
		if !errors.Is(err, ErrInvalidInput) {
			t.Errorf("caption=%q expected ErrInvalidInput, got %v", caption, err)
		}
	}
}

func TestCreateAttachmentRejectsUnknownRole(t *testing.T) {
	s, projectID, userID := seedAttachmentFixture(t)
	_, err := s.CreateAttachment(context.Background(), CreateAttachmentParams{
		ProjectID: projectID, Mime: "image/png", Role: "freeform",
		Caption: "x", UploadedBy: userID,
		Content: bytes.NewReader([]byte("x")), MaxBytes: 1024,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestCreateAttachmentRejectsEmptyBody(t *testing.T) {
	s, projectID, userID := seedAttachmentFixture(t)
	_, err := s.CreateAttachment(context.Background(), CreateAttachmentParams{
		ProjectID: projectID, Mime: "image/png", Role: "chart",
		Caption: "x", UploadedBy: userID,
		Content: bytes.NewReader(nil), MaxBytes: 1024,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

// Upload one byte past MaxBytes → rejected. The reader is wrapped in
// io.LimitReader(MaxBytes+1) so we can detect overflow without
// pre-reading the whole stream.
func TestCreateAttachmentEnforcesMaxBytes(t *testing.T) {
	s, projectID, userID := seedAttachmentFixture(t)
	payload := bytes.Repeat([]byte("x"), 1025) // 1 byte over cap
	_, err := s.CreateAttachment(context.Background(), CreateAttachmentParams{
		ProjectID: projectID, Mime: "image/png", Role: "chart",
		Caption: "x", UploadedBy: userID,
		Content: bytes.NewReader(payload), MaxBytes: 1024,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

// Same content uploaded twice → two distinct rows, ONE blob on disk
// (hash-path dedupe). The second upload's tmp file is cleaned up.
func TestCreateAttachmentDedupesByHash(t *testing.T) {
	s, projectID, userID := seedAttachmentFixture(t)
	ctx := context.Background()
	payload := []byte("same content, two captions")

	a1, err := s.CreateAttachment(ctx, CreateAttachmentParams{
		ProjectID: projectID, Mime: "image/png", Role: "chart",
		Caption: "first caption", UploadedBy: userID,
		Content: bytes.NewReader(payload), MaxBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := s.CreateAttachment(ctx, CreateAttachmentParams{
		ProjectID: projectID, Mime: "image/png", Role: "chart",
		Caption: "second caption", UploadedBy: userID,
		Content: bytes.NewReader(payload), MaxBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if a1.ID == a2.ID {
		t.Fatal("two uploads collided on id")
	}
	if a1.Hash != a2.Hash || a1.StoragePath != a2.StoragePath {
		t.Fatalf("dedupe broken: hashes/paths differ\n  a1: %+v\n  a2: %+v", a1, a2)
	}
	if a1.Caption == a2.Caption {
		t.Fatal("metadata should remain distinct per row")
	}

	// staging dir should not retain leftover temp files
	stagingDir := filepath.Join(s.DataDir(), "attachments", "tmp")
	leftover, _ := os.ReadDir(stagingDir)
	if len(leftover) != 0 {
		names := make([]string, len(leftover))
		for i, e := range leftover {
			names[i] = e.Name()
		}
		t.Errorf("staging not cleaned: %v", names)
	}
}

// Row exists but blob deleted out-of-band → ErrCorrupt, not silent
// success / 404.
func TestOpenAttachmentContentDetectsMissingBlob(t *testing.T) {
	s, projectID, userID := seedAttachmentFixture(t)
	ctx := context.Background()
	a, err := s.CreateAttachment(ctx, CreateAttachmentParams{
		ProjectID: projectID, Mime: "image/png", Role: "chart",
		Caption: "x", UploadedBy: userID,
		Content: bytes.NewReader([]byte("test")), MaxBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(s.DataDir(), a.StoragePath)); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.OpenAttachmentContent(ctx, a.ID)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected ErrCorrupt, got %v", err)
	}
}

func TestGetAttachmentUnknown(t *testing.T) {
	s, _, _ := seedAttachmentFixture(t)
	_, err := s.GetAttachment(context.Background(), "a-deadbeef")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// AttachmentRoleVocab returns the standard list, sorted. Locking the
// order so error messages stay stable across releases.
func TestAttachmentRoleVocabStable(t *testing.T) {
	got := AttachmentRoleVocab()
	want := []string{
		"after", "analysis-script", "before", "best-case", "chart",
		"log", "model-artifact", "raw-data", "sample-input",
		"sample-output", "screenshot", "worst-case",
	}
	if len(got) != len(want) {
		t.Fatalf("vocab size: got %d want %d (%v)", len(got), len(want), got)
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("[%d]: got %q want %q", i, got[i], v)
		}
	}
}
