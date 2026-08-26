package opencrab

// RuntimeSubjectResolver — the production SubjectResolver (issue #104).
// The opencrab runtime's GET /api/agents/{id} now includes the gate
// admin plane's subject_id for the agent (upstream opencrab#763), with
// fail-loud semantics runtime-side: zero mappings answer 404, multiple
// mappings answer 409.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// RuntimeSubjectResolver resolves an agent's gate subject_id via the
// runtime's agents API, on the same client/transport the provisioning
// pipeline already uses.
//
// Outcome map:
//
//	subject_id present and positive  → (subject_id, nil)
//	subject_id absent or zero        → ErrSubjectUnresolved (older
//	                                   runtime without the field —
//	                                   registration stays a logged skip)
//	HTTP 404 / null row              → ErrSubjectUnresolved (agent row
//	                                   not on the runtime yet)
//	HTTP 409, other 4xx/5xx, error   → real error (fail-loud; surfaces
//	body, transport failure            in the save banner like other
//	                                   provisioning errors)
type RuntimeSubjectResolver struct {
	Client *Client
}

func (r *RuntimeSubjectResolver) Resolve(ctx context.Context, agentID string) (int64, error) {
	if agentID == "" {
		return 0, fmt.Errorf("opencrab: agent id required")
	}
	path := "/api/agents/" + agentID

	var raw json.RawMessage
	if err := r.Client.call(ctx, http.MethodGet, path, nil, &raw); err != nil {
		var he *httpError
		if errors.As(err, &he) && he.status == http.StatusNotFound {
			// Zero subject mappings (or no agent row) — skip for now.
			return 0, fmt.Errorf("%w: GET %s answered 404", ErrSubjectUnresolved, path)
		}
		return 0, fmt.Errorf("subject lookup (GET %s): %w", path, err)
	}

	// Older runtimes answer 200 + JSON null for an absent agent row
	// (see agentExists) — same "not there yet" as the new 404.
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0, fmt.Errorf("%w: agent row absent (GET %s answered null)", ErrSubjectUnresolved, path)
	}

	var row struct {
		SubjectID int64 `json:"subject_id"`
	}
	if err := json.Unmarshal(raw, &row); err != nil {
		return 0, fmt.Errorf("subject lookup (GET %s): decode: %w", path, err)
	}
	if row.SubjectID <= 0 {
		// Field absent or zero: a runtime that predates the subject_id
		// field. Skip — never fail the save over a version gap.
		return 0, fmt.Errorf("%w: runtime response carries no positive subject_id (GET %s)", ErrSubjectUnresolved, path)
	}
	return row.SubjectID, nil
}
