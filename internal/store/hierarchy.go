package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// HierarchyNode is a single node in the project's category tree.
type HierarchyNode struct {
	ID          string
	ProjectID   string
	ParentID    string
	Name        string
	Description string
	SortOrder   int
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Metadata    string
}

// HierarchyEntry links an entry to a hierarchy node with a weight.
type HierarchyEntry struct {
	NodeID  string
	EntryID string
	Weight  float64
	AddedAt time.Time
	AddedBy string
}

// DerivedSummary captures a generated overview of a subset of the KB
// (typically the entries beneath one hierarchy node).
type DerivedSummary struct {
	ID          string
	SourceType  string
	SourceKey   string
	Title       string
	Summary     string
	EntryCount  int
	GeneratedAt time.Time
	GeneratedBy string
	Metadata    string
}

func newHierarchyID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "H-" + hex.EncodeToString(b[:])
}

func newSummaryID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "SM-" + hex.EncodeToString(b[:])
}

// CreateHierarchyNode inserts a new node. parent_id may be empty for root.
func (s *Store) CreateHierarchyNode(ctx context.Context, n *HierarchyNode) (string, error) {
	if strings.TrimSpace(n.Name) == "" {
		return "", fmt.Errorf("%w: name required", ErrInvalidInput)
	}
	if n.ID == "" {
		n.ID = newHierarchyID()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO hierarchy_nodes(id, project_id, parent_id, name, description, sort_order, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		n.ID, nullable(n.ProjectID), nullable(n.ParentID),
		n.Name, nullable(n.Description), n.SortOrder, nullable(n.Metadata))
	if err != nil {
		return "", translateErr(err)
	}
	return n.ID, nil
}

func (s *Store) GetHierarchyNode(ctx context.Context, id string) (*HierarchyNode, error) {
	var n HierarchyNode
	err := s.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(project_id,''), COALESCE(parent_id,''), name,
		       COALESCE(description,''), sort_order, created_at, updated_at,
		       COALESCE(metadata,'')
		FROM hierarchy_nodes WHERE id = ?`, id).Scan(
		&n.ID, &n.ProjectID, &n.ParentID, &n.Name, &n.Description,
		&n.SortOrder, &n.CreatedAt, &n.UpdatedAt, &n.Metadata)
	if err != nil {
		return nil, translateErr(err)
	}
	return &n, nil
}

// ListHierarchyNodes returns nodes with a matching parent_id. Pass empty
// string for root nodes. The project filter is independent and intersects.
func (s *Store) ListHierarchyNodes(ctx context.Context, projectID, parentID string) ([]*HierarchyNode, error) {
	var (
		sb   strings.Builder
		args = []any{}
	)
	sb.WriteString(`SELECT id, COALESCE(project_id,''), COALESCE(parent_id,''), name,
		COALESCE(description,''), sort_order, created_at, updated_at, COALESCE(metadata,'')
		FROM hierarchy_nodes WHERE 1=1`)
	if projectID != "" {
		sb.WriteString(` AND project_id = ?`)
		args = append(args, projectID)
	}
	if parentID == "" {
		sb.WriteString(` AND parent_id IS NULL`)
	} else {
		sb.WriteString(` AND parent_id = ?`)
		args = append(args, parentID)
	}
	sb.WriteString(` ORDER BY sort_order, name`)
	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[HierarchyNode](rows, func(c rowScanner, n *HierarchyNode) error {
		return c.Scan(&n.ID, &n.ProjectID, &n.ParentID, &n.Name, &n.Description,
			&n.SortOrder, &n.CreatedAt, &n.UpdatedAt, &n.Metadata)
	})
	if err != nil {
		return nil, err
	}
	out := make([]*HierarchyNode, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// DeleteHierarchyNode removes a node. The ON DELETE CASCADE on parent_id
// drops its descendants; hierarchy_entries cascades too.
func (s *Store) DeleteHierarchyNode(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM hierarchy_nodes WHERE id = ?`, id)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// AttachEntryToNode links an entry to a node. Re-linking updates weight.
func (s *Store) AttachEntryToNode(ctx context.Context, nodeID, entryID string, weight float64, addedBy string) error {
	if weight == 0 {
		weight = 1.0
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO hierarchy_entries(node_id, entry_id, weight, added_by)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(node_id, entry_id) DO UPDATE SET
			weight = excluded.weight,
			added_by = excluded.added_by`,
		nodeID, entryID, weight, nullable(addedBy))
	return translateErr(err)
}

// DetachEntryFromNode drops one (node, entry) link.
func (s *Store) DetachEntryFromNode(ctx context.Context, nodeID, entryID string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM hierarchy_entries WHERE node_id = ? AND entry_id = ?`,
		nodeID, entryID)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListEntriesAtNode returns entries attached to one node, newest first.
// Limit defaults to 50.
func (s *Store) ListEntriesAtNode(ctx context.Context, nodeID string, limit int) ([]*Entry, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT he.entry_id
		FROM hierarchy_entries he
		WHERE he.node_id = ?
		ORDER BY he.weight DESC, he.added_at DESC
		LIMIT ?`, nodeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*Entry, 0, len(ids))
	for _, id := range ids {
		e, err := s.GetEntry(ctx, id)
		if err != nil {
			continue // entry may have been hard-deleted; skip
		}
		out = append(out, e)
	}
	return out, nil
}

// ListNodesForEntry returns the hierarchy nodes an entry is attached to.
func (s *Store) ListNodesForEntry(ctx context.Context, entryID string) ([]*HierarchyNode, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT h.id, COALESCE(h.project_id,''), COALESCE(h.parent_id,''), h.name,
		       COALESCE(h.description,''), h.sort_order, h.created_at, h.updated_at,
		       COALESCE(h.metadata,'')
		FROM hierarchy_nodes h
		JOIN hierarchy_entries he ON he.node_id = h.id
		WHERE he.entry_id = ?
		ORDER BY h.sort_order, h.name`, entryID)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[HierarchyNode](rows, func(c rowScanner, n *HierarchyNode) error {
		return c.Scan(&n.ID, &n.ProjectID, &n.ParentID, &n.Name, &n.Description,
			&n.SortOrder, &n.CreatedAt, &n.UpdatedAt, &n.Metadata)
	})
	if err != nil {
		return nil, err
	}
	out := make([]*HierarchyNode, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// ---- derived summaries ----

// PutDerivedSummary writes (or replaces) a summary for the given source.
// Replacement semantics: delete any existing rows for the same
// (source_type, source_key) first, then insert.
func (s *Store) PutDerivedSummary(ctx context.Context, ds *DerivedSummary) (string, error) {
	if ds.SourceType == "" || ds.SourceKey == "" || ds.Title == "" || ds.Summary == "" {
		return "", fmt.Errorf("%w: source_type, source_key, title, summary required", ErrInvalidInput)
	}
	if ds.ID == "" {
		ds.ID = newSummaryID()
	}
	if ds.GeneratedBy == "" {
		ds.GeneratedBy = "heuristic"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM derived_summaries WHERE source_type = ? AND source_key = ?`,
		ds.SourceType, ds.SourceKey); err != nil {
		return "", translateErr(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO derived_summaries(id, source_type, source_key, title, summary, entry_count, generated_by, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ds.ID, ds.SourceType, ds.SourceKey, ds.Title, ds.Summary,
		ds.EntryCount, ds.GeneratedBy, nullable(ds.Metadata)); err != nil {
		return "", translateErr(err)
	}
	return ds.ID, tx.Commit()
}

// GetDerivedSummary returns the latest summary for a source, or ErrNotFound.
func (s *Store) GetDerivedSummary(ctx context.Context, sourceType, sourceKey string) (*DerivedSummary, error) {
	var d DerivedSummary
	err := s.db.QueryRowContext(ctx, `
		SELECT id, source_type, source_key, title, summary, entry_count,
		       generated_at, COALESCE(generated_by,''), COALESCE(metadata,'')
		FROM derived_summaries
		WHERE source_type = ? AND source_key = ?
		ORDER BY generated_at DESC
		LIMIT 1`, sourceType, sourceKey).Scan(
		&d.ID, &d.SourceType, &d.SourceKey, &d.Title, &d.Summary,
		&d.EntryCount, &d.GeneratedAt, &d.GeneratedBy, &d.Metadata)
	if err != nil {
		return nil, translateErr(err)
	}
	return &d, nil
}

// ListDerivedSummaries returns recent summaries across all sources.
func (s *Store) ListDerivedSummaries(ctx context.Context, limit int) ([]*DerivedSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, source_type, source_key, title, summary, entry_count,
		       generated_at, COALESCE(generated_by,''), COALESCE(metadata,'')
		FROM derived_summaries
		ORDER BY generated_at DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[DerivedSummary](rows, func(c rowScanner, d *DerivedSummary) error {
		return c.Scan(&d.ID, &d.SourceType, &d.SourceKey, &d.Title, &d.Summary,
			&d.EntryCount, &d.GeneratedAt, &d.GeneratedBy, &d.Metadata)
	})
	if err != nil {
		return nil, err
	}
	out := make([]*DerivedSummary, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// ---- aggregates for /v1/index ----

// IndexBucket is one group in the index page (tag / recent / hierarchy).
type IndexBucket struct {
	Key   string
	Label string
	Count int
}

// IndexByTag returns the top tags by count across the (optionally
// project-filtered) entry set. Defaults to top 50 unless overridden.
func (s *Store) IndexByTag(ctx context.Context, projectID string, limit int) ([]*IndexBucket, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	var (
		sb   strings.Builder
		args = []any{}
	)
	sb.WriteString(`SELECT t.tag, COUNT(*) c
		FROM tags t
		JOIN entries e ON e.id = t.entry_id
		WHERE e.status NOT IN ('SUPERSEDED','ARCHIVED','DUPLICATE')`)
	if projectID != "" {
		sb.WriteString(` AND e.project_id = ?`)
		args = append(args, projectID)
	}
	sb.WriteString(` GROUP BY t.tag ORDER BY c DESC, t.tag LIMIT ?`)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[IndexBucket](rows, func(c rowScanner, b *IndexBucket) error {
		if err := c.Scan(&b.Key, &b.Count); err != nil {
			return err
		}
		b.Label = b.Key
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]*IndexBucket, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// IndexByRecent returns entries grouped by month of creation.
func (s *Store) IndexByRecent(ctx context.Context, projectID string, limit int) ([]*IndexBucket, error) {
	if limit <= 0 || limit > 500 {
		limit = 12
	}
	var (
		sb   strings.Builder
		args = []any{}
	)
	sb.WriteString(`SELECT strftime('%Y-%m', created_at) m, COUNT(*) c
		FROM entries
		WHERE status NOT IN ('SUPERSEDED','ARCHIVED','DUPLICATE')`)
	if projectID != "" {
		sb.WriteString(` AND project_id = ?`)
		args = append(args, projectID)
	}
	sb.WriteString(` GROUP BY m ORDER BY m DESC LIMIT ?`)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[IndexBucket](rows, func(c rowScanner, b *IndexBucket) error {
		if err := c.Scan(&b.Key, &b.Count); err != nil {
			return err
		}
		b.Label = b.Key
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]*IndexBucket, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// IndexByHierarchy returns the root hierarchy nodes with entry counts.
func (s *Store) IndexByHierarchy(ctx context.Context, projectID string) ([]*IndexBucket, error) {
	var (
		sb   strings.Builder
		args = []any{}
	)
	sb.WriteString(`SELECT h.id, h.name, COALESCE(COUNT(he.entry_id), 0) c
		FROM hierarchy_nodes h
		LEFT JOIN hierarchy_entries he ON he.node_id = h.id
		WHERE h.parent_id IS NULL`)
	if projectID != "" {
		sb.WriteString(` AND h.project_id = ?`)
		args = append(args, projectID)
	}
	sb.WriteString(` GROUP BY h.id ORDER BY h.sort_order, h.name`)
	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[IndexBucket](rows, func(c rowScanner, b *IndexBucket) error {
		return c.Scan(&b.Key, &b.Label, &b.Count)
	})
	if err != nil {
		return nil, err
	}
	out := make([]*IndexBucket, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}
