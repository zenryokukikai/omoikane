package store

import (
	"errors"
	"time"
)

// Sentinel errors the API layer switches on.
var (
	ErrNotFound        = errors.New("store: not found")
	ErrAlreadyExists   = errors.New("store: already exists")
	ErrInvalidInput    = errors.New("store: invalid input")
	ErrVersionMismatch = errors.New("store: optimistic-lock version mismatch")
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
	Scope               string     `json:"scope,omitempty"`    // raw JSON
	Metadata            string     `json:"metadata,omitempty"` // raw JSON
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
	Scope               *string
	Metadata            *string
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
	Limit             int
	Offset            int
}

// User and APIToken are managed by the admin CLI; Phase 1 has no UI for them.
type User struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

type APIToken struct {
	TokenHash  string     `json:"-"`
	UserID     string     `json:"user_id,omitempty"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
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
