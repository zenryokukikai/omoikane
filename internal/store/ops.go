package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"
)

// ============================================================
// Phase 6: tier scoring + anomaly triage
// ============================================================

// EntryTier mirrors a row of the entry_tiers view.
type EntryTier struct {
	ID               string
	ProjectID        string
	Title            string
	Type             string
	Status           string
	TotalUses        int
	HelpfulCount     int
	MisleadingCount  int
	HelpfulnessScore *float64
	Tier             int
}

// ListEntriesByTier returns rows of `entry_tiers` filtered by tier
// number (1..4). Limit defaults to 100.
func (s *Store) ListEntriesByTier(ctx context.Context, tier, limit int) ([]*EntryTier, error) {
	if tier < 1 || tier > 4 {
		return nil, fmt.Errorf("%w: tier must be 1..4", ErrInvalidInput)
	}
	// Clamp explicitly: cap at the upper bound rather than
	// silently dropping to the default on overflow.
	if limit <= 0 {
		limit = 100
	} else if limit > 500 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, COALESCE(project_id,''), title, type, status,
		       total_uses, helpful_count, misleading_count, helpfulness_score, tier
		FROM entry_tiers WHERE tier = ?
		ORDER BY total_uses DESC, id
		LIMIT ?`, tier, limit)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[EntryTier](rows, func(c rowScanner, e *EntryTier) error {
		var score nullFloat
		if err := c.Scan(&e.ID, &e.ProjectID, &e.Title, &e.Type, &e.Status,
			&e.TotalUses, &e.HelpfulCount, &e.MisleadingCount, &score, &e.Tier); err != nil {
			return err
		}
		if score.Valid {
			v := score.Value
			e.HelpfulnessScore = &v
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]*EntryTier, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// CoordinatorTriage is the result of a single anomaly-scan pass.
type CoordinatorTriage struct {
	ReviewQueueDepth    int
	StaleInstances      []string
	MisleadingHeavy     []string // entry IDs with ≥3 misleading
	DormantEntryCount   int
	GeneratedAt         time.Time
}

// CoordinatorAnomalyScan looks for the cluster of conditions that the
// coordinator role would normally surface: review-queue overflow,
// missing librarian heartbeats, misleading-heavy entries, dormancy.
// Returns a snapshot the caller can route to the appropriate
// specialists.
func (s *Store) CoordinatorAnomalyScan(ctx context.Context, missingHeartbeatMinutes int) (*CoordinatorTriage, error) {
	if missingHeartbeatMinutes <= 0 {
		missingHeartbeatMinutes = 30
	}
	out := &CoordinatorTriage{GeneratedAt: time.Now().UTC()}

	// Review queue depth
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM review_queue`).Scan(&out.ReviewQueueDepth); err != nil {
		return nil, err
	}

	// Stale instances (heartbeat_at older than threshold, status != STOPPED)
	rows, err := s.db.QueryContext(ctx, `
		SELECT instance_id FROM librarian_instances
		WHERE status != 'STOPPED'
		  AND (heartbeat_at IS NULL OR heartbeat_at < datetime('now', ?))`,
		fmt.Sprintf("-%d minutes", missingHeartbeatMinutes))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		out.StaleInstances = append(out.StaleInstances, id)
	}
	rows.Close()

	// Misleading-heavy entries
	rows, err = s.db.QueryContext(ctx, `
		SELECT id FROM entry_signals WHERE misleading_count >= 3`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		out.MisleadingHeavy = append(out.MisleadingHeavy, id)
	}
	rows.Close()

	// Dormant entry count
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dormant_entries`).Scan(&out.DormantEntryCount); err != nil {
		return nil, err
	}

	return out, nil
}

// ProposeQuartet selects 3 participants + 1 judge based on role-domain
// affinity to a topic. Pass `participants` to bias selection toward
// instances that have authored in the relevant thread recently; pass
// nil to fall back to the deterministic role mapping.
func (s *Store) ProposeQuartet(ctx context.Context, topic, threadID string) (*QuartetAssignment, error) {
	// Phase 6 stub: just pick the canonical 3 specialists + judge-01.
	// A future heuristic can pull recent authors from the thread.
	q := &QuartetAssignment{
		Topic:        topic,
		ThreadID:     threadID,
		Participant1: "curator-01",
		Participant2: "detective-01",
		Participant3: "conservator-01",
		Judge:        "judge-01",
		Status:       "OPEN",
	}
	id, err := s.CreateQuartet(ctx, q)
	if err != nil {
		return nil, err
	}
	q.ID = id
	return q, nil
}

// ============================================================
// Phase 7: backup
// ============================================================

// BackupJob is one row of backup_jobs.
type BackupJob struct {
	ID         string
	Path       string
	StartedAt  time.Time
	FinishedAt *time.Time
	Status     string
	Bytes      int64
	Error      string
}

func newBackupID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "BK-" + hex.EncodeToString(b[:])
}

// RunBackup snapshots the database to `path` via SQLite's `VACUUM INTO`
// (atomic, online, no journal interference). Returns the BackupJob row.
func (s *Store) RunBackup(ctx context.Context, path string) (*BackupJob, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: path required", ErrInvalidInput)
	}
	id := newBackupID()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO backup_jobs(id, path, status) VALUES (?, ?, 'RUNNING')`,
		id, path); err != nil {
		return nil, translateErr(err)
	}
	// VACUUM INTO needs a literal path; sqlite3 driver doesn't bind it.
	// Reject anything that has a quote-escape attempt baked in.
	if strings.ContainsAny(path, "';\\") {
		_ = s.markBackup(ctx, id, "FAILED", 0, "invalid path characters")
		return nil, fmt.Errorf("%w: path contains forbidden characters", ErrInvalidInput)
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO '`+path+`'`); err != nil {
		_ = s.markBackup(ctx, id, "FAILED", 0, err.Error())
		return nil, err
	}
	info, statErr := os.Stat(path)
	var bytes int64
	if statErr == nil {
		bytes = info.Size()
	}
	if err := s.markBackup(ctx, id, "DONE", bytes, ""); err != nil {
		return nil, err
	}
	return s.GetBackup(ctx, id)
}

func (s *Store) markBackup(ctx context.Context, id, status string, bytes int64, errMsg string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
		UPDATE backup_jobs SET status=?, finished_at=?, bytes=?, error=?
		WHERE id = ?`, status, now, bytes, nullable(errMsg), id)
	return translateErr(err)
}

func (s *Store) GetBackup(ctx context.Context, id string) (*BackupJob, error) {
	var b BackupJob
	var finished nullTimeBox
	err := s.db.QueryRowContext(ctx, `
		SELECT id, path, started_at, finished_at, status, COALESCE(bytes,0), COALESCE(error,'')
		FROM backup_jobs WHERE id = ?`, id).Scan(
		&b.ID, &b.Path, &b.StartedAt, &finished, &b.Status, &b.Bytes, &b.Error)
	if err != nil {
		return nil, translateErr(err)
	}
	if finished.Valid {
		x := finished.Time
		b.FinishedAt = &x
	}
	return &b, nil
}

func (s *Store) ListBackups(ctx context.Context, limit int) ([]*BackupJob, error) {
	// Clamp explicitly: cap at the upper bound rather than
	// silently dropping to the default on overflow.
	if limit <= 0 {
		limit = 50
	} else if limit > 500 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, path, started_at, finished_at, status, COALESCE(bytes,0), COALESCE(error,'')
		FROM backup_jobs ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[BackupJob](rows, func(c rowScanner, b *BackupJob) error {
		var finished nullTimeBox
		if err := c.Scan(&b.ID, &b.Path, &b.StartedAt, &finished, &b.Status, &b.Bytes, &b.Error); err != nil {
			return err
		}
		if finished.Valid {
			x := finished.Time
			b.FinishedAt = &x
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]*BackupJob, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}

// ============================================================
// Phase 7: dead-pool
// ============================================================

// ArchiveDormantEntries marks entries from the dormant_entries view as
// ARCHIVED with `invalidation_reason='dead-pool: dormant'`. Returns the
// archived count.
func (s *Store) ArchiveDormantEntries(ctx context.Context) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM dormant_entries`)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	now := time.Now().UTC()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `
			UPDATE entries
			SET status='ARCHIVED', valid_to=?, invalidation_reason='dead-pool: dormant',
			    updated_at=?, version = version + 1
			WHERE id = ? AND status = 'ACTIVE'`,
			now, now, id); err != nil {
			return 0, translateErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// ============================================================
// Phase 7: LLM budget
// ============================================================

// LLMUsage records a single LLM call.
type LLMUsage struct {
	ID           int64
	Timestamp    time.Time
	Provider     string
	Model        string
	InputTokens  int
	OutputTokens int
	CostUSD      float64
	Purpose      string
	EntryID      string
	Metadata     string
}

func (s *Store) RecordLLMUsage(ctx context.Context, u *LLMUsage) error {
	if u.Provider == "" {
		return fmt.Errorf("%w: provider required", ErrInvalidInput)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO llm_usage_log(provider, model, input_tokens, output_tokens, cost_usd, purpose, entry_id, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.Provider, nullable(u.Model), u.InputTokens, u.OutputTokens,
		u.CostUSD, nullable(u.Purpose), nullable(u.EntryID), nullable(u.Metadata))
	return translateErr(err)
}

// LLMUsageStats is a windowed aggregate.
type LLMUsageStats struct {
	WindowDays   int     `json:"window_days"`
	Calls        int     `json:"calls"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// LLMUsageStatsWindow returns the aggregate over the trailing N days.
func (s *Store) LLMUsageStatsWindow(ctx context.Context, days int) (*LLMUsageStats, error) {
	if days <= 0 {
		days = 30
	}
	var out LLMUsageStats
	out.WindowDays = days
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT COALESCE(COUNT(*),0),
		       COALESCE(SUM(input_tokens),0),
		       COALESCE(SUM(output_tokens),0),
		       COALESCE(SUM(cost_usd),0)
		FROM llm_usage_log
		WHERE timestamp >= datetime('now', '-%d days')`, days)).Scan(
		&out.Calls, &out.InputTokens, &out.OutputTokens, &out.CostUSD); err != nil {
		return nil, err
	}
	return &out, nil
}

// ============================================================
// Phase 7: self-improvement metrics
// ============================================================

// HealthCoverage reports how much of the project graph has been
// enriched, has any feedback, and has tags.
type HealthCoverage struct {
	TotalActive     int `json:"total_active"`
	WithTags        int `json:"with_tags"`
	WithEnrichment  int `json:"with_enrichment"`
	WithFeedback    int `json:"with_feedback"`
	WithRelations   int `json:"with_relations"`
	WithHierarchy   int `json:"with_hierarchy"`
}

func (s *Store) HealthCoverageStats(ctx context.Context) (*HealthCoverage, error) {
	out := &HealthCoverage{}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM entries WHERE status NOT IN ('SUPERSEDED','ARCHIVED','DUPLICATE')`).Scan(
		&out.TotalActive); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT t.entry_id) FROM tags t
		JOIN entries e ON e.id = t.entry_id
		WHERE e.status NOT IN ('SUPERSEDED','ARCHIVED','DUPLICATE')`).Scan(
		&out.WithTags); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM entries WHERE enrichment_version > 0
		   AND status NOT IN ('SUPERSEDED','ARCHIVED','DUPLICATE')`).Scan(
		&out.WithEnrichment); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT entry_id) FROM usage_cases`).Scan(&out.WithFeedback); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT from_id) FROM relations`).Scan(&out.WithRelations); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT entry_id) FROM hierarchy_entries`).Scan(&out.WithHierarchy); err != nil {
		return nil, err
	}
	return out, nil
}
