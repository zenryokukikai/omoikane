package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// IncidentCluster groups similar incident-type entries. Per docs/design.md
// §4.2; status lifecycle: OPEN → PROMOTED | DISMISSED.
type IncidentCluster struct {
	ID                 string
	ProjectID          string
	Title              string
	Summary            string
	MemberCount        int
	PromotedToEntryID  string
	Status             string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	Metadata           string
}

type IncidentClusterMember struct {
	ClusterID  string
	EntryID    string
	Similarity float64
	AddedAt    time.Time
	AddedBy    string
}

func newClusterID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "CL-" + hex.EncodeToString(b[:])
}

// CreateCluster inserts a new cluster row. The member list is empty by
// default — use AddClusterMember to attach entries.
func (s *Store) CreateCluster(ctx context.Context, c *IncidentCluster) (string, error) {
	if strings.TrimSpace(c.Title) == "" {
		return "", fmt.Errorf("%w: title required", ErrInvalidInput)
	}
	if c.ID == "" {
		c.ID = newClusterID()
	}
	if c.Status == "" {
		c.Status = "OPEN"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO incident_clusters(id, project_id, title, summary, member_count, status, metadata)
		VALUES (?, ?, ?, ?, 0, ?, ?)`,
		c.ID, nullable(c.ProjectID), c.Title, nullable(c.Summary),
		c.Status, nullable(c.Metadata))
	if err != nil {
		return "", translateErr(err)
	}
	return c.ID, nil
}

// AddClusterMember links an entry to a cluster and bumps member_count.
// Idempotent — re-adding the same pair only updates the similarity.
func (s *Store) AddClusterMember(ctx context.Context, clusterID, entryID string, similarity float64, addedBy string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		INSERT INTO incident_cluster_members(cluster_id, entry_id, similarity, added_by)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(cluster_id, entry_id) DO UPDATE SET
			similarity = excluded.similarity`,
		clusterID, entryID, similarity, nullable(addedBy))
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		// On insert (RowsAffected=1) AND on UPDATE (n=1 for sqlite upsert)
		// we recompute member_count from the source of truth — cheap and
		// avoids drift.
		if _, err := tx.ExecContext(ctx, `
			UPDATE incident_clusters
			SET member_count = (SELECT COUNT(*) FROM incident_cluster_members WHERE cluster_id = ?),
			    updated_at = ?
			WHERE id = ?`, clusterID, time.Now().UTC(), clusterID); err != nil {
			return translateErr(err)
		}
	}
	return tx.Commit()
}

// RemoveClusterMember drops an entry from a cluster and decrements the
// member_count.
func (s *Store) RemoveClusterMember(ctx context.Context, clusterID, entryID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`DELETE FROM incident_cluster_members WHERE cluster_id = ? AND entry_id = ?`,
		clusterID, entryID)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE incident_clusters
		SET member_count = (SELECT COUNT(*) FROM incident_cluster_members WHERE cluster_id = ?),
		    updated_at = ?
		WHERE id = ?`, clusterID, time.Now().UTC(), clusterID); err != nil {
		return translateErr(err)
	}
	return tx.Commit()
}

// PromoteCluster marks a cluster PROMOTED and links it to the entry that
// canonicalises the pattern. Idempotent — already-PROMOTED clusters can be
// pointed at a different entry.
func (s *Store) PromoteCluster(ctx context.Context, clusterID, entryID string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE incident_clusters
		SET status = 'PROMOTED',
		    promoted_to_entry_id = ?,
		    updated_at = ?
		WHERE id = ?`, entryID, time.Now().UTC(), clusterID)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DismissCluster marks a cluster DISMISSED. Used when reviewers decide the
// grouping is noise.
func (s *Store) DismissCluster(ctx context.Context, clusterID string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE incident_clusters
		SET status = 'DISMISSED', updated_at = ?
		WHERE id = ?`, time.Now().UTC(), clusterID)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetCluster(ctx context.Context, id string) (*IncidentCluster, error) {
	var c IncidentCluster
	err := s.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(project_id,''), title, COALESCE(summary,''),
		       member_count, COALESCE(promoted_to_entry_id,''), status,
		       created_at, updated_at, COALESCE(metadata,'')
		FROM incident_clusters WHERE id = ?`, id).Scan(
		&c.ID, &c.ProjectID, &c.Title, &c.Summary,
		&c.MemberCount, &c.PromotedToEntryID, &c.Status,
		&c.CreatedAt, &c.UpdatedAt, &c.Metadata)
	if err != nil {
		return nil, translateErr(err)
	}
	return &c, nil
}

// ListClusters returns clusters, optionally filtered by project_id and/or
// status. Status filter accepts the empty string meaning "any".
func (s *Store) ListClusters(ctx context.Context, projectID, status string, limit int) ([]*IncidentCluster, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var (
		sb   strings.Builder
		args = []any{}
	)
	sb.WriteString(`SELECT id, COALESCE(project_id,''), title, COALESCE(summary,''),
		member_count, COALESCE(promoted_to_entry_id,''), status,
		created_at, updated_at, COALESCE(metadata,'')
		FROM incident_clusters WHERE 1=1`)
	if projectID != "" {
		sb.WriteString(` AND project_id = ?`)
		args = append(args, projectID)
	}
	if status != "" {
		sb.WriteString(` AND status = ?`)
		args = append(args, status)
	}
	sb.WriteString(` ORDER BY updated_at DESC LIMIT ?`)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[IncidentCluster](rows, func(r rowScanner, c *IncidentCluster) error {
		return r.Scan(&c.ID, &c.ProjectID, &c.Title, &c.Summary,
			&c.MemberCount, &c.PromotedToEntryID, &c.Status,
			&c.CreatedAt, &c.UpdatedAt, &c.Metadata)
	})
	if err != nil {
		return nil, err
	}
	out := make([]*IncidentCluster, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

func (s *Store) ListClusterMembers(ctx context.Context, clusterID string) ([]*IncidentClusterMember, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT cluster_id, entry_id, COALESCE(similarity, 1.0), added_at, COALESCE(added_by,'')
		FROM incident_cluster_members WHERE cluster_id = ?
		ORDER BY similarity DESC, entry_id`, clusterID)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[IncidentClusterMember](rows, func(r rowScanner, m *IncidentClusterMember) error {
		return r.Scan(&m.ClusterID, &m.EntryID, &m.Similarity, &m.AddedAt, &m.AddedBy)
	})
	if err != nil {
		return nil, err
	}
	out := make([]*IncidentClusterMember, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// clusterCand is one incident under consideration for clustering.
type clusterCand struct {
	id     string
	tokens map[string]struct{}
}

// BuildIncidentClusters runs a simple symptom-token-overlap clustering pass
// over OPEN incident entries that are not already in a cluster. For each
// pair of incidents whose symptom-token Jaccard ≥ threshold, the helper
// either appends to an existing OPEN cluster that shares an entry, or
// creates a new cluster.
//
// Threshold defaults to 0.4; minMembers defaults to 2. Returns the number
// of new clusters created and members added.
func (s *Store) BuildIncidentClusters(ctx context.Context, projectID string, threshold float64, minMembers int) (clustersCreated, membersAdded int, err error) {
	if threshold <= 0 || threshold > 1 {
		threshold = 0.4
	}
	if minMembers < 2 {
		minMembers = 2
	}

	var (
		sb   strings.Builder
		args = []any{}
	)
	sb.WriteString(`SELECT id, COALESCE(symptom,'')
		FROM entries
		WHERE type = 'incident'
		  AND status IN ('ACTIVE','INVESTIGATING','DRAFT')
		  AND COALESCE(symptom,'') <> ''`)
	if projectID != "" {
		sb.WriteString(` AND project_id = ?`)
		args = append(args, projectID)
	}
	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return 0, 0, err
	}
	values, err := mapRows[clusterCand](rows, func(r rowScanner, c *clusterCand) error {
		var symptom string
		if err := r.Scan(&c.id, &symptom); err != nil {
			return err
		}
		c.tokens = symptomTokens(symptom)
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	if len(values) < minMembers {
		return 0, 0, nil
	}

	existingMembership, err := s.loadExistingClusterMemberships(ctx)
	if err != nil {
		return 0, 0, err
	}

	type group struct {
		members map[string]struct{}
		sims    map[string]float64
	}
	var groups []*group
	byID := map[string]*clusterCand{}
	for i := range values {
		byID[values[i].id] = &values[i]
	}
	for i := range values {
		a := &values[i]
		if _, taken := existingMembership[a.id]; taken {
			continue
		}
		placed := false
		for _, g := range groups {
			for member := range g.members {
				b := byID[member]
				if b == nil {
					continue
				}
				sim := jaccard(a.tokens, b.tokens)
				if sim >= threshold {
					g.members[a.id] = struct{}{}
					g.sims[a.id] = sim
					placed = true
					break
				}
			}
			if placed {
				break
			}
		}
		if placed {
			continue
		}
		gr := &group{
			members: map[string]struct{}{a.id: {}},
			sims:    map[string]float64{a.id: 1.0},
		}
		for j := i + 1; j < len(values); j++ {
			b := &values[j]
			if _, taken := existingMembership[b.id]; taken {
				continue
			}
			sim := jaccard(a.tokens, b.tokens)
			if sim >= threshold {
				gr.members[b.id] = struct{}{}
				gr.sims[b.id] = sim
			}
		}
		if len(gr.members) >= minMembers {
			groups = append(groups, gr)
			for m := range gr.members {
				existingMembership[m] = ""
			}
		}
	}

	for _, g := range groups {
		title := summariseGroup(g.members, byID)
		cl := &IncidentCluster{
			ProjectID: projectID,
			Title:     title,
			Summary:   fmt.Sprintf("auto-clustered %d incidents", len(g.members)),
			Status:    "OPEN",
		}
		cid, err := s.CreateCluster(ctx, cl)
		if err != nil {
			return clustersCreated, membersAdded, err
		}
		clustersCreated++
		for m := range g.members {
			if err := s.AddClusterMember(ctx, cid, m, g.sims[m], "auto"); err != nil {
				return clustersCreated, membersAdded, err
			}
			membersAdded++
		}
	}
	return clustersCreated, membersAdded, nil
}

func (s *Store) loadExistingClusterMemberships(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.entry_id, m.cluster_id
		FROM incident_cluster_members m
		JOIN incident_clusters c ON c.id = m.cluster_id
		WHERE c.status = 'OPEN'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var entryID, clusterID string
		if err := rows.Scan(&entryID, &clusterID); err != nil {
			return nil, err
		}
		out[entryID] = clusterID
	}
	return out, rows.Err()
}

// symptomTokens tokenises a symptom into a lowercase token set, dropping
// short tokens. Used by BuildIncidentClusters; close cousin of ftsTokenise
// but optimised for set comparison rather than FTS5 syntax.
func symptomTokens(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, f := range strings.FieldsFunc(s, isTokenBoundary) {
		f = strings.ToLower(strings.TrimSpace(f))
		if len(f) < 3 {
			continue
		}
		out[f] = struct{}{}
	}
	return out
}

func isTokenBoundary(r rune) bool {
	switch r {
	case ' ', '\t', '\n', ',', ';', '.', '(', ')', '[', ']', '{', '}',
		'"', '\'', '`', ':', '/', '\\', '!', '?', '=', '<', '>', '|', '-':
		return true
	}
	return false
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// summariseGroup picks the most common token across the group's members and
// uses it as a one-line title. Falls back to "incident" when the group has
// no tokens (theoretical — symptomTokens always emits at least one for the
// candidates we accept).
func summariseGroup(members map[string]struct{}, byID map[string]*clusterCand) string {
	counts := map[string]int{}
	for m := range members {
		c := byID[m]
		if c == nil {
			continue
		}
		for tok := range c.tokens {
			counts[tok]++
		}
	}
	best, bestN := "", 0
	for t, n := range counts {
		if n > bestN {
			best, bestN = t, n
		}
	}
	if best == "" {
		best = "incident"
	}
	return "cluster: " + best
}
