package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/zenryokukikai/omoikane/internal/store"
)

type searchRequest struct {
	Query   string         `json:"query"`
	Mode    string         `json:"mode,omitempty"`
	Filters *searchFilters `json:"filters,omitempty"`
	TopK    int            `json:"top_k,omitempty"`
	// IncludeChat = true also searches librarian_chat (opt-in;
	// chat is not durable knowledge and would dilute precision on
	// lookup-style queries). Results come back in a separate
	// `chat_results` field on the response.
	IncludeChat bool `json:"include_chat,omitempty"`
	// View selects the projection of each hit (issue #138):
	// ViewFull (the default) keeps the whole entry, ViewIndex returns
	// the id/title/snippet a reader needs to decide what to open.
	View string `json:"view,omitempty"`
}

// Response projections for search hits (issue #138).
//
// ViewFull is the default and is byte-for-byte the pre-#138 shape plus
// the new `snippet` field, so the dashboard and the dist/ librarian
// scripts keep working untouched. ViewIndex drops the entry body: a
// top_k=5 full response runs 6-13 KB, which crowds out an agent's
// context, while the flat hit is ~2 KB and still says enough — title
// plus the matched text — to choose what to fetch in full.
const (
	ViewFull  = "full"
	ViewIndex = "index"
)

// indexHit is the ViewIndex projection. It lives in the API layer on
// purpose: the store keeps carrying the full *Entry either way, because
// RecordAccess and the mode=reasoning re-rank both read it. Only the
// marshalling changes.
type indexHit struct {
	EntryID   string    `json:"entry_id"`
	Title     string    `json:"title"`
	Type      string    `json:"type"`
	ProjectID string    `json:"project_id"`
	UpdatedAt time.Time `json:"updated_at"`
	Score     float64   `json:"score"`
	Snippet   string    `json:"snippet"`
}

func indexHits(results []*store.SearchResult) []indexHit {
	out := make([]indexHit, 0, len(results))
	for _, sr := range results {
		out = append(out, indexHit{
			EntryID:   sr.Entry.ID,
			Title:     sr.Entry.Title,
			Type:      sr.Entry.Type,
			ProjectID: sr.Entry.ProjectID,
			UpdatedAt: sr.Entry.UpdatedAt,
			Score:     sr.Score,
			Snippet:   sr.Snippet,
		})
	}
	return out
}

// ZeroHitHint replaces feedback_prompt on a count:0 response. The prompt
// is hit-time wording ("was this helpful?") and reads, next to an empty
// result list, as though something is still coming — a librarian in
// production really did take count:0 for "not ready yet" and waited.
// This says the opposite in as many words, and tells the reader what to
// try instead (issue #138).
const ZeroHitHint = `該当なし。語を減らすか固有名詞で引き直してください` +
	`(人名・ID が最も当たります)。この count:0 は` +
	`『結果がまだ準備中』ではなく『一致ゼロ』の確定応答です。`

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
	// An unknown view is a 400, never a silent fall back to "full": a
	// typo would otherwise show up only as "search is inexplicably
	// heavy", and the cause would take a long time to find.
	if req.View != "" && req.View != ViewFull && req.View != ViewIndex {
		writeError(w, http.StatusBadRequest, CodeBadRequest,
			"view must be full or index",
			map[string]any{"view": req.View})
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
	results, total, match, err := h.Store.SearchFTS(httpCtx(r), req.Query, filter)
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
	resp := map[string]any{
		"results": results,
		"count":   len(results),
		"total":   total,
		"mode":    defaultMode(req.Mode),
		// `match` is deliberately its own field: `mode` already means the
		// search strategy (fts / reasoning) and `view` the projection.
		// Overloading one of them would give the same knob two meanings.
		"match": match,
	}
	if req.View == ViewIndex {
		resp["results"] = indexHits(results)
	}
	if len(results) == 0 {
		resp["hint"] = ZeroHitHint
	} else {
		resp["feedback_prompt"] = FeedbackPrompt
	}
	// Passive access logging — every entry surfaced via search counts as
	// a "the agent saw this" event. Best-effort; non-fatal.
	if len(results) > 0 {
		ids := make([]string, len(results))
		for i, sr := range results {
			ids[i] = sr.Entry.ID
		}
		userID := r.Header.Get("X-Audit-User")
		_ = h.Store.RecordAccess(httpCtx(r), ids, userID, store.AccessSourceSearch, req.Query)
	}
	// Opt-in chat search. Chat results live in a separate field so
	// existing clients (which read only `results`) are unaffected.
	// The shape is documented in SKILL.md "Searching chat (opt-in)".
	if req.IncludeChat {
		chatResults, cerr := h.Store.SearchChatFTS(httpCtx(r), req.Query, req.TopK)
		if cerr != nil {
			// Don't fail the whole request — entries already came back.
			// Surface the chat search error as a partial-failure note;
			// the entry results are still useful.
			resp["chat_error"] = cerr.Error()
		} else {
			resp["chat_results"] = chatResults
			resp["chat_count"] = len(chatResults)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func defaultMode(m string) string {
	if m == "" {
		return "fts"
	}
	return m
}
