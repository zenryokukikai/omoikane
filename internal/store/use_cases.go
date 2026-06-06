package store

import (
	"context"
	"encoding/base32"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// UseCase is a first-class "kind of problem omoikane covers". See
// design.md §23.15.4. Many-to-many with entries via use_case_entries.
//
// ParentID is the self-referential link that makes use_cases a tree.
// The growth pattern is BOTTOM-UP: leaves (concrete categories close to
// entries) come first, and the indexer's "tidy" mode periodically stacks
// META categories ABOVE the leaves when the top-level count gets too
// high to browse. The same rule applies at any level — meta of meta,
// meta of meta of meta — so the tree deepens as the corpus grows
// without anyone having to predefine "large / medium / small".
type UseCase struct {
	ID            string
	Slug          string
	NameJA        string
	NameEN        string
	DescriptionJA string
	DescriptionEN string
	Domain        string
	Source        string
	ParentID      string // empty = top-level (root)
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// UseCaseSummary is one row of the /lookup browse list: a UseCase plus
// its entry count, child count (for tree drilldown), and a small sample
// of linked entries.
type UseCaseSummary struct {
	UseCase
	EntryCount    int
	ChildCount    int // direct children (parent_id = this.id)
	SampleEntries []UseCaseSampleEntry
}

type UseCaseSampleEntry struct {
	EntryID   string
	Title     string
	Type      string
	ProjectID string
}

// EntryUseCase is a UseCase row plus the linkage timestamp — for the
// "use cases this entry belongs to" view on the entry page.
type EntryUseCase struct {
	UseCase
	LinkedAt time.Time
}

// UseCaseFilter narrows ListUseCases.
type UseCaseFilter struct {
	ProjectID string // restrict to UseCases that have at least one entry in this project
	Domain    string
	Query     string // matches name_ja or name_en (LIKE)

	// Tree filters — Level and ParentID are interchangeable views of the
	// same dimension; set at most one. Level="top" → parent_id IS NULL.
	// Level="all" or zero-value → no parent filter. ParentID set → only
	// direct children of that node.
	Level    string // "" | "top" | "all"
	ParentID string
}

// ------------------------------------------------------------------
// ID + slug helpers
// ------------------------------------------------------------------

// newUseCaseID returns a fresh "U-XXXXXX" id.
func newUseCaseID() (string, error) {
	var buf [4]byte
	if _, err := randRead(buf[:]); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	enc := strings.ToUpper(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf[:]))
	if len(enc) > 6 {
		enc = enc[:6]
	}
	return "U-" + enc, nil
}

var nonSlugChar = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify turns a UseCase's English name into a kebab-case slug. Lower-case
// ASCII alphanumerics joined by single hyphens; non-ASCII / punctuation
// becomes a separator. Length-capped at 60.
//
// Indexers should send the same name → same slug, so parallel agents
// converge on the same UseCase row via UPSERT (slug is UNIQUE).
func Slugify(nameEN string) string {
	s := strings.ToLower(nameEN)
	s = nonSlugChar.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = s[:60]
		s = strings.TrimRight(s, "-")
	}
	return s
}

// ------------------------------------------------------------------
// CRUD
// ------------------------------------------------------------------

// UpsertUseCase creates or updates a UseCase by slug. Slug is canonical —
// supplying the same slug twice updates the existing row's name / desc /
// domain / source / updated_at, while preserving its id and linkage.
//
// If uc.ID is empty AND the slug doesn't yet exist, a new "U-XXXXXX" is
// generated. uc.ID and uc.Slug are filled in on the returned row.
func (s *Store) UpsertUseCase(ctx context.Context, uc *UseCase) (*UseCase, error) {
	if uc == nil {
		return nil, fmt.Errorf("%w: use_case required", ErrInvalidInput)
	}
	uc.NameJA = strings.TrimSpace(uc.NameJA)
	uc.NameEN = strings.TrimSpace(uc.NameEN)
	if uc.NameJA == "" || uc.NameEN == "" {
		return nil, fmt.Errorf("%w: name_ja and name_en required", ErrInvalidInput)
	}
	uc.Slug = strings.TrimSpace(uc.Slug)
	if uc.Slug == "" {
		uc.Slug = Slugify(uc.NameEN)
	}
	if uc.Slug == "" {
		return nil, fmt.Errorf("%w: slug could not be derived from name_en", ErrInvalidInput)
	}
	if uc.Source == "" {
		uc.Source = "indexer"
	}

	// Existing slug? Update in place.
	existing, err := s.GetUseCaseBySlug(ctx, uc.Slug)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if existing != nil {
		// parent_id semantics on upsert: if the caller explicitly supplied
		// one, we honour it; otherwise we leave the existing parent in
		// place (so an indexer re-running an old leaf doesn't yank it out
		// of the tree the tidy mode placed it in).
		if uc.ParentID != "" {
			_, err := s.db.ExecContext(ctx, `
				UPDATE use_cases
				   SET name_ja = ?, name_en = ?,
				       description_ja = ?, description_en = ?,
				       domain = ?, source = ?, parent_id = ?,
				       updated_at = CURRENT_TIMESTAMP
				 WHERE id = ?`,
				uc.NameJA, uc.NameEN, uc.DescriptionJA, uc.DescriptionEN,
				nullable(uc.Domain), uc.Source, uc.ParentID, existing.ID)
			if err != nil {
				return nil, translateErr(err)
			}
		} else {
			_, err := s.db.ExecContext(ctx, `
				UPDATE use_cases
				   SET name_ja = ?, name_en = ?,
				       description_ja = ?, description_en = ?,
				       domain = ?, source = ?,
				       updated_at = CURRENT_TIMESTAMP
				 WHERE id = ?`,
				uc.NameJA, uc.NameEN, uc.DescriptionJA, uc.DescriptionEN,
				nullable(uc.Domain), uc.Source, existing.ID)
			if err != nil {
				return nil, translateErr(err)
			}
		}
		return s.GetUseCase(ctx, existing.ID)
	}

	// New row.
	if uc.ID == "" {
		id, err := newUseCaseID()
		if err != nil {
			return nil, err
		}
		uc.ID = id
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO use_cases
		    (id, slug, name_ja, name_en, description_ja, description_en, domain, source, parent_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uc.ID, uc.Slug, uc.NameJA, uc.NameEN,
		uc.DescriptionJA, uc.DescriptionEN, nullable(uc.Domain), uc.Source,
		nullable(uc.ParentID))
	if err != nil {
		return nil, translateErr(err)
	}
	return s.GetUseCase(ctx, uc.ID)
}

// SetUseCaseParent rewrites a UseCase's parent. Pass empty parentID to
// promote it back to top-level. The indexer's "tidy" mode calls this in
// batches when it stacks meta categories above existing leaves.
func (s *Store) SetUseCaseParent(ctx context.Context, useCaseID, parentID string) error {
	if useCaseID == "" {
		return fmt.Errorf("%w: use_case_id required", ErrInvalidInput)
	}
	if useCaseID == parentID {
		return fmt.Errorf("%w: a use_case cannot be its own parent", ErrInvalidInput)
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE use_cases
		   SET parent_id = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, nullable(parentID), useCaseID)
	if err != nil {
		return translateErr(err)
	}
	return nil
}

// GetUseCase returns a UseCase by id, or ErrNotFound.
func (s *Store) GetUseCase(ctx context.Context, id string) (*UseCase, error) {
	return s.queryUseCase(ctx, `WHERE id = ?`, id)
}

// GetUseCaseBySlug returns a UseCase by slug, or ErrNotFound.
func (s *Store) GetUseCaseBySlug(ctx context.Context, slug string) (*UseCase, error) {
	return s.queryUseCase(ctx, `WHERE slug = ?`, slug)
}

func (s *Store) queryUseCase(ctx context.Context, whereSQL string, arg any) (*UseCase, error) {
	uc := &UseCase{}
	var domain, parent *string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, slug, name_ja, name_en, description_ja, description_en,
		       COALESCE(domain,''), source, parent_id, created_at, updated_at
		  FROM use_cases `+whereSQL, arg).Scan(
		&uc.ID, &uc.Slug, &uc.NameJA, &uc.NameEN, &uc.DescriptionJA, &uc.DescriptionEN,
		&domain, &uc.Source, &parent, &uc.CreatedAt, &uc.UpdatedAt)
	if err != nil {
		return nil, translateErr(err)
	}
	if domain != nil {
		uc.Domain = *domain
	}
	if parent != nil {
		uc.ParentID = *parent
	}
	return uc, nil
}

// ------------------------------------------------------------------
// Linking
// ------------------------------------------------------------------

// LinkUseCaseEntry attaches an entry to a UseCase. Idempotent: re-linking
// the same pair is a no-op (PRIMARY KEY conflict swallowed). Bumps the
// parent UseCase's updated_at so lists re-sort to the front.
func (s *Store) LinkUseCaseEntry(ctx context.Context, useCaseID, entryID, source string) error {
	if useCaseID == "" || entryID == "" {
		return fmt.Errorf("%w: use_case_id and entry_id required", ErrInvalidInput)
	}
	if source == "" {
		source = "indexer"
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO use_case_entries (use_case_id, entry_id, source)
		VALUES (?, ?, ?)`, useCaseID, entryID, source); err != nil {
		return translateErr(err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE use_cases SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		useCaseID); err != nil {
		return translateErr(err)
	}
	return nil
}

// UnlinkUseCaseEntry detaches an entry from a UseCase. No-op if the link
// didn't exist (no error).
func (s *Store) UnlinkUseCaseEntry(ctx context.Context, useCaseID, entryID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM use_case_entries WHERE use_case_id = ? AND entry_id = ?`,
		useCaseID, entryID)
	if err != nil {
		return translateErr(err)
	}
	return nil
}

// ------------------------------------------------------------------
// Listing
// ------------------------------------------------------------------

// ListUseCases returns paginated UseCases sorted by most recently updated
// first, each enriched with its total entry count and up to 4 sample entries.
// Returns the page rows plus the total count.
func (s *Store) ListUseCases(ctx context.Context, f UseCaseFilter, limit, offset int) ([]*UseCaseSummary, int, error) {
	// Clamp explicitly. Falling back to the default (30) on an over-limit
	// request would silently truncate the result set — callers see fewer
	// rows than they asked for, with no signal. Cap at 200 instead so an
	// "I want as much as you can give me" caller gets the maximum.
	if limit <= 0 {
		limit = 30
	} else if limit > 200 {
		limit = 200
	}

	// Build the WHERE clause shared by count + page queries.
	conds := []string{}
	args := []any{}
	if f.Domain != "" {
		conds = append(conds, "uc.domain = ?")
		args = append(args, f.Domain)
	}
	if f.Query != "" {
		conds = append(conds, "(uc.name_ja LIKE ? OR uc.name_en LIKE ? OR uc.slug LIKE ?)")
		q := "%" + f.Query + "%"
		args = append(args, q, q, q)
	}
	if f.ProjectID != "" {
		conds = append(conds,
			"EXISTS (SELECT 1 FROM use_case_entries uce JOIN entries e ON e.id = uce.entry_id WHERE uce.use_case_id = uc.id AND e.project_id = ?)")
		args = append(args, f.ProjectID)
	}
	// Tree filters — ParentID wins if both are set (more specific).
	switch {
	case f.ParentID != "":
		conds = append(conds, "uc.parent_id = ?")
		args = append(args, f.ParentID)
	case f.Level == "top":
		conds = append(conds, "uc.parent_id IS NULL")
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM use_cases uc `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	pageSQL := `
		SELECT uc.id, uc.slug, uc.name_ja, uc.name_en,
		       uc.description_ja, uc.description_en,
		       COALESCE(uc.domain,''), uc.source, uc.parent_id,
		       uc.created_at, uc.updated_at,
		       COALESCE((SELECT COUNT(*) FROM use_case_entries WHERE use_case_id = uc.id),0) AS entry_count,
		       COALESCE((SELECT COUNT(*) FROM use_cases ch WHERE ch.parent_id = uc.id),0) AS child_count
		  FROM use_cases uc ` + where + `
		 ORDER BY uc.updated_at DESC, uc.id
		 LIMIT ? OFFSET ?`
	pageArgs := append(append([]any{}, args...), limit, offset)
	rows, err := s.db.QueryContext(ctx, pageSQL, pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []*UseCaseSummary
	for rows.Next() {
		var sum UseCaseSummary
		var domain string
		var parent *string
		if err := rows.Scan(&sum.ID, &sum.Slug, &sum.NameJA, &sum.NameEN,
			&sum.DescriptionJA, &sum.DescriptionEN, &domain, &sum.Source,
			&parent, &sum.CreatedAt, &sum.UpdatedAt, &sum.EntryCount, &sum.ChildCount); err != nil {
			return nil, 0, err
		}
		sum.Domain = domain
		if parent != nil {
			sum.ParentID = *parent
		}
		out = append(out, &sum)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	// Sample entries — small per-row queries (4 each, 30 rows max).
	for _, sum := range out {
		sRows, sErr := s.db.QueryContext(ctx, `
			SELECT e.id, e.title, e.type, e.project_id
			  FROM use_case_entries uce
			  JOIN entries e ON e.id = uce.entry_id
			 WHERE uce.use_case_id = ?
			 ORDER BY uce.created_at DESC, e.id
			 LIMIT 4`, sum.ID)
		if sErr != nil {
			continue
		}
		for sRows.Next() {
			var se UseCaseSampleEntry
			if sRows.Scan(&se.EntryID, &se.Title, &se.Type, &se.ProjectID) == nil {
				sum.SampleEntries = append(sum.SampleEntries, se)
			}
		}
		sRows.Close()
	}
	return out, total, nil
}

// ListEntryUseCases returns the UseCases an entry belongs to, most recently
// linked first. For the entry page's "属するユースケース" chips.
func (s *Store) ListEntryUseCases(ctx context.Context, entryID string) ([]*EntryUseCase, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT uc.id, uc.slug, uc.name_ja, uc.name_en,
		       uc.description_ja, uc.description_en,
		       COALESCE(uc.domain,''), uc.source, uc.parent_id,
		       uc.created_at, uc.updated_at,
		       uce.created_at
		  FROM use_case_entries uce
		  JOIN use_cases uc ON uc.id = uce.use_case_id
		 WHERE uce.entry_id = ?
		 ORDER BY uce.created_at DESC, uc.id`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*EntryUseCase
	for rows.Next() {
		var r EntryUseCase
		var domain string
		var parent *string
		if err := rows.Scan(&r.ID, &r.Slug, &r.NameJA, &r.NameEN,
			&r.DescriptionJA, &r.DescriptionEN, &domain, &r.Source,
			&parent, &r.CreatedAt, &r.UpdatedAt, &r.LinkedAt); err != nil {
			return nil, err
		}
		r.Domain = domain
		if parent != nil {
			r.ParentID = *parent
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// ListUseCaseEntries returns the full entries linked to a UseCase, paginated,
// most-recently-linked first. For the UseCase detail page.
func (s *Store) ListUseCaseEntries(ctx context.Context, useCaseID string, limit, offset int) ([]*Entry, int, error) {
	// Clamp explicitly: cap at the upper bound rather than
	// silently dropping to the default on overflow.
	if limit <= 0 {
		limit = 30
	} else if limit > 200 {
		limit = 200
	}
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM use_case_entries WHERE use_case_id = ?`,
		useCaseID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT uce.entry_id
		  FROM use_case_entries uce
		 WHERE uce.use_case_id = ?
		 ORDER BY uce.created_at DESC, uce.entry_id
		 LIMIT ? OFFSET ?`, useCaseID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	entries := make([]*Entry, 0, len(ids))
	for _, id := range ids {
		e, err := s.GetEntry(ctx, id)
		if err == nil {
			entries = append(entries, e)
		}
	}
	return entries, total, nil
}
