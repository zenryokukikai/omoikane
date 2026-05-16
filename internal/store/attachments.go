package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ----------------------------------------------------------------------
// Attachments — file/image evidence as a first-class resource.
//
// See spec X-YCXLOW. The schema enforces caption + role at write
// time; the store layer validates the role against the standard
// vocabulary and writes the blob under <dataDir>/attachments/aa/bb/<hash>
// with a 2-level fanout. Same content (same sha256) deduplicates: the
// row is created fresh but the blob path is reused.
// ----------------------------------------------------------------------

// Attachment is one row of the attachments table (no body bytes).
type Attachment struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	Mime         string    `json:"mime"`
	Filename     string    `json:"filename,omitempty"`
	SizeBytes    int64     `json:"size_bytes"`
	Hash         string    `json:"hash"`
	Role         string    `json:"role"`
	Caption      string    `json:"caption"`
	UploadedBy   string    `json:"uploaded_by"`
	UploadedAt   time.Time `json:"uploaded_at"`
	StoragePath  string    `json:"-"` // internal — never marshalled
}

// validAttachmentRoles is the MVP role vocabulary. New roles require a
// release; we keep it tight on purpose so the agent-readability
// contract (predictable semantics per role) doesn't drift.
var validAttachmentRoles = map[string]bool{
	"chart":           true,
	"screenshot":      true,
	"sample-input":    true,
	"sample-output":   true,
	"before":          true,
	"after":           true,
	"worst-case":      true,
	"best-case":       true,
	"raw-data":        true,
	"log":             true,
	"model-artifact":  true,
	"analysis-script": true,
}

// ValidAttachmentRole reports whether r is in the standard vocabulary.
// Exposed so the API layer can validate before bothering to spool the
// upload to disk.
func ValidAttachmentRole(r string) bool { return validAttachmentRoles[r] }

// AttachmentRoleVocab returns the standard role vocabulary, sorted.
// Used for error messages and docs so we don't repeat the list.
func AttachmentRoleVocab() []string {
	out := make([]string, 0, len(validAttachmentRoles))
	for k := range validAttachmentRoles {
		out = append(out, k)
	}
	// stable order for deterministic error messages
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// CreateAttachmentParams is the input to CreateAttachment. Content is
// streamed (not buffered into memory) — the store reads from it, hashes
// it, writes to disk, and only then inserts the row.
type CreateAttachmentParams struct {
	ProjectID  string
	Mime       string
	Filename   string // optional, original filename from upload
	Role       string
	Caption    string
	UploadedBy string
	Content    io.Reader
	// MaxBytes caps the upload size. The caller (API layer) is
	// responsible for setting an appropriate limit; passing 0 means
	// no cap (which is dangerous in practice).
	MaxBytes int64
}

// CreateAttachment streams the content to disk, hashes it, and inserts
// a row. Returns the persisted Attachment with id/hash/size populated.
//
// Storage path is <dataDir>/attachments/<hash[:2]>/<hash[2:4]>/<hash>.
// If a file with the same hash already exists, the new upload is
// discarded and the existing blob is reused — same-content uploads
// dedupe naturally. Metadata rows are NOT deduplicated; each upload
// gets its own row (with its own caption / role / id) even when the
// underlying bytes match a previous upload.
func (s *Store) CreateAttachment(ctx context.Context, p CreateAttachmentParams) (*Attachment, error) {
	if p.ProjectID == "" {
		return nil, fmt.Errorf("%w: project_id required", ErrInvalidInput)
	}
	if p.Mime == "" {
		return nil, fmt.Errorf("%w: mime required", ErrInvalidInput)
	}
	if strings.TrimSpace(p.Caption) == "" {
		return nil, fmt.Errorf("%w: caption required (agent-readable description; cannot be blank)", ErrInvalidInput)
	}
	if !ValidAttachmentRole(p.Role) {
		return nil, fmt.Errorf("%w: role must be one of: %s",
			ErrInvalidInput, strings.Join(AttachmentRoleVocab(), ", "))
	}
	if p.UploadedBy == "" {
		return nil, fmt.Errorf("%w: uploaded_by required", ErrInvalidInput)
	}
	if p.Content == nil {
		return nil, fmt.Errorf("%w: content reader required", ErrInvalidInput)
	}
	if p.MaxBytes <= 0 {
		return nil, fmt.Errorf("%w: MaxBytes must be > 0", ErrInvalidInput)
	}

	// 1. Stream into a temp file under dataDir/attachments/tmp, hashing
	//    on the way through. LimitReader enforces the size cap as we
	//    read — we add 1 to the cap so we can detect "exceeded" by
	//    reading one byte past the limit.
	stagingDir := filepath.Join(s.dataDir, "attachments", "tmp")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}
	tmp, err := os.CreateTemp(stagingDir, "upload-*")
	if err != nil {
		return nil, fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	// cleanup on any failure after this point
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	h := sha256.New()
	mw := io.MultiWriter(tmp, h)
	limited := io.LimitReader(p.Content, p.MaxBytes+1)
	n, err := io.Copy(mw, limited)
	if err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("read upload: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close temp: %w", err)
	}
	if n > p.MaxBytes {
		return nil, fmt.Errorf("%w: upload exceeded MaxBytes=%d", ErrInvalidInput, p.MaxBytes)
	}
	if n == 0 {
		return nil, fmt.Errorf("%w: empty upload", ErrInvalidInput)
	}

	// 2. Compute the final storage path from the content hash.
	hash := hex.EncodeToString(h.Sum(nil))
	relPath := filepath.Join("attachments", hash[:2], hash[2:4], hash)
	absPath := filepath.Join(s.dataDir, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir storage: %w", err)
	}

	// 3. If the hash file already exists, the new upload is a duplicate
	//    of an earlier one. Discard the temp; reuse the existing blob.
	//    Otherwise rename temp into place atomically.
	if _, statErr := os.Stat(absPath); statErr == nil {
		// dedupe: defer cleanup will remove the temp
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("stat storage: %w", statErr)
	} else {
		if err := os.Rename(tmpPath, absPath); err != nil {
			return nil, fmt.Errorf("rename to storage: %w", err)
		}
		tmpPath = "" // don't try to remove the now-moved file
	}

	// 4. Mint id and insert row. The row is the source of truth — a
	//    row without a backing file is treated as missing-content, but
	//    a file without a row is just garbage (cleaned up by a janitor
	//    later if we add one).
	id, err := newAttachmentID()
	if err != nil {
		return nil, fmt.Errorf("mint id: %w", err)
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO attachments
		    (id, project_id, mime, filename, size_bytes, hash, role,
		     caption, uploaded_by, uploaded_at, storage_path)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, p.ProjectID, p.Mime, nullable(p.Filename), n, hash, p.Role,
		strings.TrimSpace(p.Caption), p.UploadedBy, now, relPath)
	if err != nil {
		return nil, translateErr(err)
	}

	return s.GetAttachment(ctx, id)
}

// GetAttachment returns the metadata row by id. No content.
func (s *Store) GetAttachment(ctx context.Context, id string) (*Attachment, error) {
	var a Attachment
	var filename *string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, mime, filename, size_bytes, hash, role,
		       caption, uploaded_by, uploaded_at, storage_path
		FROM attachments WHERE id = ?`, id).Scan(
		&a.ID, &a.ProjectID, &a.Mime, &filename, &a.SizeBytes, &a.Hash,
		&a.Role, &a.Caption, &a.UploadedBy, &a.UploadedAt, &a.StoragePath)
	if err != nil {
		return nil, translateErr(err)
	}
	if filename != nil {
		a.Filename = *filename
	}
	return &a, nil
}

// OpenAttachmentContent returns a ReadCloser streaming the blob plus
// the metadata. Caller MUST Close the reader. Returns ErrNotFound if
// the row doesn't exist or ErrCorrupt if the row exists but the file
// is missing on disk.
func (s *Store) OpenAttachmentContent(ctx context.Context, id string) (io.ReadCloser, *Attachment, error) {
	a, err := s.GetAttachment(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(filepath.Join(s.dataDir, a.StoragePath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, a, fmt.Errorf("%w: attachment %s row exists but blob missing at %s",
				ErrCorrupt, a.ID, a.StoragePath)
		}
		return nil, a, fmt.Errorf("open blob: %w", err)
	}
	return f, a, nil
}

// newAttachmentID returns "a-<8 hex>". Matches the pattern of user ids
// ("u-") and chat ids ("msg-"/"thread-").
func newAttachmentID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "a-" + hex.EncodeToString(b[:]), nil
}
