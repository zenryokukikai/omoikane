package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/kojira/omoikane/internal/store"
)

type searchRequest struct {
	Query   string         `json:"query"`
	Mode    string         `json:"mode,omitempty"`
	Filters *searchFilters `json:"filters,omitempty"`
	TopK    int            `json:"top_k,omitempty"`
}

type searchFilters struct {
	ProjectID         string `json:"project"`
	Type              string `json:"type"`
	Status            string `json:"status"`
	Tag               string `json:"tag"`
	IncludeSuperseded bool   `json:"include_superseded"`
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		writeError(w, http.StatusBadRequest, CodeBadQuery, "query is required", nil)
		return
	}
	if req.Mode != "" && req.Mode != "fts" && req.Mode != "reasoning" {
		writeError(w, http.StatusNotImplemented, CodeNotImplemented,
			"mode must be fts or reasoning",
			map[string]any{"feature": "search.mode=" + req.Mode})
		return
	}
	filter := store.EntryFilter{Limit: req.TopK}
	if req.Filters != nil {
		filter.ProjectID = req.Filters.ProjectID
		filter.Type = req.Filters.Type
		filter.Status = req.Filters.Status
		filter.Tag = req.Filters.Tag
		filter.IncludeSuperseded = req.Filters.IncludeSuperseded
	}
	// store.SearchFTS rejects empty queries with ErrInvalidInput, but the
	// handler-level guard above prevents that path from being reached —
	// any error here is therefore an internal-store failure that
	// writeStoreError will translate.
	results, total, err := h.Store.SearchFTS(httpCtx(r), req.Query, filter)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// mode=reasoning re-ranks the FTS hits by helpfulness_score. A future
	// LLM-backed implementation can replace this; for now the deterministic
	// re-rank is the Phase 4 deliverable so the endpoint stops being a
	// 501 stub.
	if req.Mode == "reasoning" && len(results) > 0 {
		ids := make([]string, len(results))
		for i, r := range results {
			ids[i] = r.Entry.ID
		}
		boosts, _ := h.Store.HelpfulnessScores(httpCtx(r), ids)
		for i, sr := range results {
			boost := 1.0
			if s, ok := boosts[sr.Entry.ID]; ok {
				boost = 1.0 + 0.5*s
				if boost < 0.5 {
					boost = 0.5
				}
			}
			results[i].Score = sr.Score * boost
		}
		// Simple insertion sort by Score DESC; len is bounded by TopK.
		for i := 1; i < len(results); i++ {
			j := i
			for j > 0 && results[j].Score > results[j-1].Score {
				results[j], results[j-1] = results[j-1], results[j]
				j--
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": results,
		"count":   len(results),
		"total":   total,
		"mode":    defaultMode(req.Mode),
	})
}

func defaultMode(m string) string {
	if m == "" {
		return "fts"
	}
	return m
}
