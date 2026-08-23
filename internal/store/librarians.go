package store

// librarians.go owns the librarian instance/role domain: the
// librarian_instances table (register / status / heartbeat / list) and
// the canonical role + chat-author vocabularies.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// LibrarianInstance is one running (or paused) librarian.
type LibrarianInstance struct {
	InstanceID   string     `json:"instance_id"`
	Role         string     `json:"role"`
	SkillVersion string     `json:"skill_version,omitempty"`
	AgentRuntime string     `json:"agent_runtime,omitempty"`
	Status       string     `json:"status"`
	StartedAt    time.Time  `json:"started_at"`
	HeartbeatAt  *time.Time `json:"heartbeat_at,omitempty"`
	Metadata     string     `json:"metadata,omitempty"`
}

// ValidLibrarianRole reports whether r is one of the 8 canonical roles.
// Used for `librarian_instances.role` and `librarian_tasks.role` — these
// belong to specific agents, so the human author is NOT accepted here.
func ValidLibrarianRole(r string) bool {
	switch r {
	case "coordinator", "cataloger", "curator", "detective",
		"conservator", "scout", "indexer", "summarizer", "judge",
		"synthesizer":
		return true
	}
	return false
}

// LibrarianRoleSlice returns the canonical roles as a sorted slice.
// Used to echo the allowed list in error responses so callers can
// self-correct without a doc lookup.
func LibrarianRoleSlice() []string {
	return []string{
		"cataloger", "conservator", "coordinator", "curator",
		"detective", "indexer", "judge", "scout", "summarizer",
		"synthesizer",
	}
}

// ValidLibrarianRoles is a map view of the canonical roles for
// constant-time membership tests. Kept in sync with ValidLibrarianRole.
var ValidLibrarianRoles = map[string]bool{
	"coordinator": true,
	"cataloger":   true,
	"curator":     true,
	"detective":   true,
	"conservator": true,
	"scout":       true,
	"indexer":     true,
	"summarizer":  true,
	"judge":       true,
	"synthesizer": true,
}

// ValidChatAuthor is the broader set of accepted `author_role` values
// on `librarian_chat`. In addition to the 8 librarians, "human" is
// allowed so the user (Z-axis observer per design.md §24) can join the
// shared chat. Phase 5 ships this so the dashboard chat room is
// actually two-way.
func ValidChatAuthor(r string) bool {
	// Humans and every rostered librarian may speak; "chronicler" is the
	// deliberately OFF-ROSTER community agent (the /talk responder) — it never
	// holds librarian_instances/tasks, but it does answer in chat
	// (/talk), so it is a valid chat author without widening the
	// librarian-role vocabulary. "assistant" is off-roster in the same
	// way: the per-user personal librarian (issue #73) answering its
	// owner's /talk threads with the owner's own token. The vocabulary
	// only lets it post chat — it never appears on the librarian roster.
	if r == "human" || r == "chronicler" || r == "assistant" {
		return true
	}
	return ValidLibrarianRole(r)
}

func (s *Store) RegisterLibrarianInstance(ctx context.Context, i *LibrarianInstance) (string, error) {
	if !ValidLibrarianRole(i.Role) {
		return "", fmt.Errorf("%w: invalid role %q", ErrInvalidInput, i.Role)
	}
	if i.InstanceID == "" {
		i.InstanceID = i.Role + "-" + newLibrarianID("inst")[5:]
	}
	if i.Status == "" {
		i.Status = "OBSERVING"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO librarian_instances(instance_id, role, skill_version, agent_runtime, status, metadata)
		VALUES (?, ?, ?, ?, ?, ?)`,
		i.InstanceID, i.Role, nullable(i.SkillVersion),
		nullable(i.AgentRuntime), i.Status, nullable(i.Metadata))
	if err != nil {
		return "", translateErr(err)
	}
	return i.InstanceID, nil
}

func (s *Store) SetLibrarianStatus(ctx context.Context, instanceID, status string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE librarian_instances SET status = ? WHERE instance_id = ?`,
		status, instanceID)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetLibrarianInstance returns one instance by id, or ErrNotFound. Used
// by the emergency-stop check at the start of each librarian tick.
func (s *Store) GetLibrarianInstance(ctx context.Context, instanceID string) (*LibrarianInstance, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT instance_id, role, COALESCE(skill_version,''), COALESCE(agent_runtime,''),
		       status, started_at, heartbeat_at, COALESCE(metadata,'')
		FROM librarian_instances WHERE instance_id = ?`, instanceID)
	var (
		i  LibrarianInstance
		hb sql.NullTime
	)
	err := row.Scan(&i.InstanceID, &i.Role, &i.SkillVersion, &i.AgentRuntime,
		&i.Status, &i.StartedAt, &hb, &i.Metadata)
	if err != nil {
		return nil, translateErr(err)
	}
	if hb.Valid {
		t := hb.Time
		i.HeartbeatAt = &t
	}
	return &i, nil
}

func (s *Store) RecordHeartbeat(ctx context.Context, instanceID string) error {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`UPDATE librarian_instances SET heartbeat_at = ? WHERE instance_id = ?`,
		now, instanceID)
	if err != nil {
		return translateErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListLibrarianInstances(ctx context.Context, role, status string) ([]*LibrarianInstance, error) {
	var (
		sb   strings.Builder
		args = []any{}
	)
	sb.WriteString(`SELECT instance_id, role, COALESCE(skill_version,''), COALESCE(agent_runtime,''),
		status, started_at, heartbeat_at, COALESCE(metadata,'')
		FROM librarian_instances WHERE 1=1`)
	if role != "" {
		sb.WriteString(` AND role = ?`)
		args = append(args, role)
	}
	if status != "" {
		sb.WriteString(` AND status = ?`)
		args = append(args, status)
	}
	sb.WriteString(` ORDER BY role, instance_id`)
	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	values, err := mapRows[LibrarianInstance](rows, func(c rowScanner, i *LibrarianInstance) error {
		var hb nullTimeBox
		if err := c.Scan(&i.InstanceID, &i.Role, &i.SkillVersion, &i.AgentRuntime,
			&i.Status, &i.StartedAt, &hb, &i.Metadata); err != nil {
			return err
		}
		if hb.Valid {
			t := hb.Time
			i.HeartbeatAt = &t
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]*LibrarianInstance, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out, nil
}
