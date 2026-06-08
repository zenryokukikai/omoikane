package store

import (
	"encoding/json"
	"errors"
	"time"
)

// Sentinel errors the API layer switches on.
var (
	ErrNotFound        = errors.New("store: not found")
	ErrAlreadyExists   = errors.New("store: already exists")
	ErrInvalidInput    = errors.New("store: invalid input")
	ErrVersionMismatch = errors.New("store: optimistic-lock version mismatch")
	// ErrCorrupt is returned when a row's referenced backing data is
	// missing or unreadable (e.g. attachment row exists but the blob
	// file is gone). Should be rare — surfaces filesystem damage or
	// out-of-band deletion.
	ErrCorrupt = errors.New("store: corrupt (row exists but backing data is missing)")
)

// EntryType — v0.6 includes librarian_meta and external_finding for
// forward compatibility with Phase 5+ (the schema accepts them from day 1
// so future migrations don't need to retroactively widen a CHECK).
type EntryType string

const (
	TypeTrap            EntryType = "trap"
	TypeDecision        EntryType = "decision"
	TypeDesign          EntryType = "design"
	TypeLesson          EntryType = "lesson"
	TypeIncident        EntryType = "incident"
	TypeLibrarianMeta   EntryType = "librarian_meta"
	TypeExternalFinding EntryType = "external_finding"
)

func ValidEntryType(t string) bool {
	switch EntryType(t) {
	case TypeTrap, TypeDecision, TypeDesign, TypeLesson, TypeIncident,
		TypeLibrarianMeta, TypeExternalFinding:
		return true
	}
	return false
}

type Status string

const (
	StatusDraft         Status = "DRAFT"
	StatusInvestigating Status = "INVESTIGATING"
	StatusActive        Status = "ACTIVE"
	StatusSuperseded    Status = "SUPERSEDED"
	StatusArchived      Status = "ARCHIVED"
	StatusDuplicate     Status = "DUPLICATE"
	StatusResolved      Status = "RESOLVED"
)

func ValidStatus(s string) bool {
	switch Status(s) {
	case StatusDraft, StatusInvestigating, StatusActive, StatusSuperseded,
		StatusArchived, StatusDuplicate, StatusResolved:
		return true
	}
	return false
}

// Project mirrors the projects row. Metadata is kept as raw JSON text so
// the store doesn't need to know its shape.
type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	Metadata    string    `json:"metadata,omitempty"`
}

// Entry mirrors the entries row plus joined tags.
type Entry struct {
	ID                  string     `json:"id"`
	ProjectID           string     `json:"project_id"`
	Type                string     `json:"type"`
	Title               string     `json:"title"`
	Status              string     `json:"status"`
	Symptom             string     `json:"symptom,omitempty"`
	RootCause           string     `json:"root_cause,omitempty"`
	Resolution          string     `json:"resolution,omitempty"`
	Prohibited          string     `json:"prohibited,omitempty"`
	AttemptedApproaches string     `json:"attempted_approaches,omitempty"`
	ObservedBehavior    string     `json:"observed_behavior,omitempty"`
	Hypotheses          string     `json:"hypotheses,omitempty"`
	Body                string     `json:"body"`
	BodyFormat          string     `json:"body_format"`
	// Scope and Metadata are stored as TEXT in the entries table but
	// carried on the wire as raw JSON values, not JSON-encoded
	// strings. json.RawMessage makes a posted `metadata: {"k":"v"}`
	// come back on read as `metadata: {"k":"v"}` (the object). Prior
	// to migration these were `string`, which forced API responses
	// to wrap stored JSON in another layer of escaping
	// (e.g. `"\"{\\\"k\\\":\\\"v\\\"}\""`).
	Scope               json.RawMessage `json:"scope,omitempty"`
	Metadata            json.RawMessage `json:"metadata,omitempty"`
	ValidFrom           time.Time  `json:"valid_from"`
	ValidTo             *time.Time `json:"valid_to,omitempty"`
	SupersededBy        string     `json:"superseded_by,omitempty"`
	InvalidationReason  string     `json:"invalidation_reason,omitempty"`
	EnrichmentVersion   int        `json:"enrichment_version"`
	EnrichmentAt        *time.Time `json:"enrichment_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
	CreatedBy           string     `json:"created_by,omitempty"`
	CreatedByRole       string     `json:"created_by_role,omitempty"`
	Version             int        `json:"version"`
	Tags                []string   `json:"tags"`
}

// EntryPatch — non-nil pointer = update; non-nil empty string = clear field.
type EntryPatch struct {
	Title               *string
	Status              *string
	Symptom             *string
	RootCause           *string
	Resolution          *string
	Prohibited          *string
	AttemptedApproaches *string
	ObservedBehavior    *string
	Hypotheses          *string
	Body                *string
	BodyFormat          *string
	Scope               *json.RawMessage
	Metadata            *json.RawMessage
	Tags                *[]string

	// Audit
	ChangedBy     string
	ChangedByRole string
	ChangeSummary string

	// OCC
	ExpectedVersion int // 0 = caller skipped OCC (only allowed for admin paths)
}

// EntryFilter narrows list/search queries.
type EntryFilter struct {
	ProjectID         string
	Type              string
	Status            string
	Tag               string
	Query             string
	IncludeSuperseded bool
	// Uncategorized restricts to entries with NO use_case link — the
	// indexer's real work-feed. Without it a "newest-first" feed keeps
	// surfacing already-categorised entries and never drains the backlog.
	Uncategorized bool
	// OldestFirst flips the default newest-first ordering. Draining a
	// backlog FIFO (oldest unindexed first) makes coverage monotonic.
	OldestFirst bool
	// NotProgressedByRole excludes entries that already have a
	// librarian_progress row for this role. Lets a role's work-feed skip
	// what it already decided on — e.g. the indexer records "skipped a
	// record" progress, and that entry stops re-appearing in its feed even
	// though it never got a use_case link.
	NotProgressedByRole string
	Limit               int
	Offset              int
}

// User is the human (or service principal) behind a token. From Phase A
// it carries OAuth identity fields; password fields are present but unset
// until Phase B activates them.
type User struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Role            string     `json:"role"`
	CreatedAt       time.Time  `json:"created_at"`
	Email           string     `json:"email,omitempty"`
	GoogleSub       string     `json:"-"`                            // never marshal — internal identity
	AvatarURL       string     `json:"avatar_url,omitempty"`
	LastLoginAt     *time.Time `json:"last_login_at,omitempty"`
	EmailVerifiedAt *time.Time `json:"email_verified_at,omitempty"`
	ParentUserID    string     `json:"parent_user_id,omitempty"` // for agent users — the human who owns them
	Description     string     `json:"description,omitempty"`
	// LibrarianRole is non-empty only for agent users that hold a
	// dedicated librarian seat (cataloger, curator, …). Set at invite
	// redemption when the invite carried a librarian_role. Drives both
	// authorisation (token gets the `librarian` scope) and the role-
	// consistency check on POST /v1/librarian/instances.
	LibrarianRole   string     `json:"librarian_role,omitempty"`
	// password_hash deliberately omitted from the struct — never read into
	// app memory unless the password-verification code path needs it
	// (Phase B). Keeping it out reduces accidental leak surface.
}

// The canonical list of librarian roles lives in librarians.go alongside
// the existing ValidLibrarianRole(r string) bool predicate, which we
// reuse so the two paths can't drift. LibrarianRoleSlice (defined there)
// gives the same list as a sorted []string for error-response echoes.

type APIToken struct {
	TokenHash  string     `json:"-"`
	UserID     string     `json:"user_id,omitempty"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	TokenType  string     `json:"token_type"` // "api" (long-lived) | "session" (browser)
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// AuditEvent is what middleware emits to the audit_log table on every
// write request (POST/PATCH/DELETE under /v1, excluding /v1/health).
type AuditEvent struct {
	Timestamp    time.Time
	RequestID    string
	UserID       string
	TokenName    string
	Method       string
	Path         string
	BodySummary  string
	ClientType   string
	ClientIP     string
	StatusCode   int
	DurationMs   int64
}
