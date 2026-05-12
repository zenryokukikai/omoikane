package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/kojira/omoikane/internal/auth"
	"github.com/kojira/omoikane/internal/store"
)

// lookupResponse is the shape returned by all four lookup endpoints. We
// include the matched entry's brief summary (title + prohibited rules) so
// agents don't need a follow-up GET for the most common pre-flight use.
// When `create_cases=true` is set on the request, a fresh usage_case is
// minted for each match and its case_id is returned so the agent can
// PATCH outcome/result later.
type lookupMatch struct {
	EntryID    string  `json:"entry_id"`
	Title      string  `json:"title"`
	Type       string  `json:"type"`
	Status     string  `json:"status"`
	Score      float64 `json:"score"`
	Symptom    string  `json:"symptom,omitempty"`
	Resolution string  `json:"resolution,omitempty"`
	Prohibited string  `json:"prohibited,omitempty"`
	MatchedVia string  `json:"matched_via,omitempty"`
	CaseID     string  `json:"case_id,omitempty"`
}

type lookupResponse struct {
	Matches []lookupMatch `json:"matches"`
}

// ---- by-trigger ----

type byTriggerReq struct {
	Domain             string `json:"domain,omitempty"`
	TriggerDescription string `json:"trigger_description"`
	TopK               int    `json:"top_k,omitempty"`
	ProjectID          string `json:"project_id,omitempty"`
	IncludeProhibited  bool   `json:"include_prohibited,omitempty"`
	CreateCases        bool   `json:"create_cases,omitempty"`
}

func (h *Handler) lookupByTrigger(w http.ResponseWriter, r *http.Request) {
	var req byTriggerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if strings.TrimSpace(req.TriggerDescription) == "" {
		writeError(w, http.StatusBadRequest, CodeBadQuery,
			"trigger_description is required", nil)
		return
	}
	hits, err := h.Store.LookupByTrigger(httpCtx(r), req.TriggerDescription, req.Domain, req.TopK)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := h.buildLookupResponse(r, lookupCtx{
		hits:              hits,
		projectID:         req.ProjectID,
		includeProhibited: req.IncludeProhibited,
		createCases:       req.CreateCases,
		triggerQuery:      req.TriggerDescription,
	})
	writeJSON(w, http.StatusOK, out)
}

// ---- by-symptom ----

type bySymptomReq struct {
	SymptomDescription string `json:"symptom_description"`
	TopK               int    `json:"top_k,omitempty"`
	ProjectID          string `json:"project_id,omitempty"`
	IncludeProhibited  bool   `json:"include_prohibited,omitempty"`
	CreateCases        bool   `json:"create_cases,omitempty"`
}

func (h *Handler) lookupBySymptom(w http.ResponseWriter, r *http.Request) {
	var req bySymptomReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if strings.TrimSpace(req.SymptomDescription) == "" {
		writeError(w, http.StatusBadRequest, CodeBadQuery,
			"symptom_description is required", nil)
		return
	}
	hits, err := h.Store.LookupBySymptom(httpCtx(r), req.SymptomDescription, req.TopK)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := h.buildLookupResponse(r, lookupCtx{
		hits:              hits,
		projectID:         req.ProjectID,
		includeProhibited: req.IncludeProhibited,
		createCases:       req.CreateCases,
		triggerQuery:      req.SymptomDescription,
	})
	writeJSON(w, http.StatusOK, out)
}

// ---- by-tags ----

type byTagsReq struct {
	Tags              []string `json:"tags"`
	MatchMode         string   `json:"match_mode,omitempty"`
	TopK              int      `json:"top_k,omitempty"`
	ProjectID         string   `json:"project_id,omitempty"`
	IncludeProhibited bool     `json:"include_prohibited,omitempty"`
	CreateCases       bool     `json:"create_cases,omitempty"`
}

func (h *Handler) lookupByTags(w http.ResponseWriter, r *http.Request) {
	var req byTagsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if len(req.Tags) == 0 {
		writeError(w, http.StatusBadRequest, CodeBadQuery, "tags is required", nil)
		return
	}
	hits, err := h.Store.LookupByTags(httpCtx(r), req.Tags, req.MatchMode, req.TopK)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := h.buildLookupResponse(r, lookupCtx{
		hits:              hits,
		projectID:         req.ProjectID,
		includeProhibited: req.IncludeProhibited,
		createCases:       req.CreateCases,
		triggerQuery:      strings.Join(req.Tags, ","),
	})
	writeJSON(w, http.StatusOK, out)
}

// ---- by-situation (Phase 3) ----

type bySituationReq struct {
	SituationDescription string `json:"situation_description"`
	TopK                 int    `json:"top_k,omitempty"`
	ProjectID            string `json:"project_id,omitempty"`
	IncludeProhibited    bool   `json:"include_prohibited,omitempty"`
	CreateCases          bool   `json:"create_cases,omitempty"`
}

func (h *Handler) lookupBySituation(w http.ResponseWriter, r *http.Request) {
	var req bySituationReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if strings.TrimSpace(req.SituationDescription) == "" {
		writeError(w, http.StatusBadRequest, CodeBadQuery,
			"situation_description is required", nil)
		return
	}
	hits, err := h.Store.LookupBySituation(httpCtx(r), req.SituationDescription, req.TopK)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := h.buildLookupResponse(r, lookupCtx{
		hits:              hits,
		projectID:         req.ProjectID,
		includeProhibited: req.IncludeProhibited,
		createCases:       req.CreateCases,
		triggerQuery:      req.SituationDescription,
	})
	writeJSON(w, http.StatusOK, out)
}

// ---- shared helpers ----

// lookupCtx bundles arguments for buildLookupResponse, since the function
// has grown enough switches that a positional signature is too easy to
// mis-call.
type lookupCtx struct {
	hits              []*store.LookupHit
	projectID         string
	includeProhibited bool
	createCases       bool
	triggerQuery      string
}

// buildLookupResponse expands LookupHit IDs into full match objects by
// fetching each entry. Entries that the project filter excludes (or that
// have been deleted/SUPERSEDED) are silently dropped. When createCases is
// set, one usage_case row is recorded per surviving match so the agent can
// later PATCH the outcome — failure to mint the case is logged and
// non-fatal (we still surface the match).
//
// Scores are multiplied by a helpfulness boost when available: an entry
// with a strong positive `helpfulness_score` is ranked higher than an
// entry that has been marked misleading repeatedly. The bias is bounded
// so a single helpful case can't dominate a high-quality FTS match, and a
// single misleading case can't flatten a rule hit.
func (h *Handler) buildLookupResponse(r *http.Request, c lookupCtx) lookupResponse {
	out := lookupResponse{Matches: make([]lookupMatch, 0, len(c.hits))}
	if len(c.hits) == 0 {
		return out
	}
	ids := make([]string, 0, len(c.hits))
	for _, hit := range c.hits {
		ids = append(ids, hit.EntryID)
	}
	scores, err := h.Store.HelpfulnessScores(httpCtx(r), ids)
	if err != nil {
		h.Logger.Warn("helpfulness fetch failed; ranking without boost", "err", err)
		scores = map[string]float64{}
	}

	tok := auth.FromContext(r.Context())
	createdBy, _ := identifyCreator(tok)

	for _, hit := range c.hits {
		e, err := h.Store.GetEntry(httpCtx(r), hit.EntryID)
		if err != nil {
			continue
		}
		if c.projectID != "" && e.ProjectID != c.projectID {
			continue
		}
		switch e.Status {
		case "SUPERSEDED", "ARCHIVED", "DUPLICATE":
			continue
		}
		boost := 1.0
		if s, ok := scores[e.ID]; ok {
			// helpfulness_score is in [-1, 1]; we map to [0.5, 1.5].
			boost = 1.0 + 0.5*s
			if boost < 0.5 {
				boost = 0.5
			}
		}
		m := lookupMatch{
			EntryID:    e.ID,
			Title:      e.Title,
			Type:       e.Type,
			Status:     e.Status,
			Score:      hit.Score * boost,
			Symptom:    e.Symptom,
			Resolution: e.Resolution,
			MatchedVia: hit.Source + ":" + hit.Phrase,
		}
		if c.includeProhibited {
			m.Prohibited = e.Prohibited
		} else if e.Prohibited != "" {
			m.Prohibited = "(present — fetch entry for details)"
		}
		if c.createCases {
			caseID, cerr := h.Store.CreateCase(httpCtx(r), &store.UsageCase{
				EntryID:      e.ID,
				ProjectID:    e.ProjectID,
				TriggerQuery: c.triggerQuery,
				AgentRole:    createdBy,
			})
			if cerr != nil {
				h.Logger.Warn("case create failed", "entry", e.ID, "err", cerr)
			} else {
				m.CaseID = caseID
			}
		}
		out.Matches = append(out.Matches, m)
	}
	return out
}
