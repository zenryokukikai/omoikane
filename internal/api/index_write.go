package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/zenryokukikai/omoikane/internal/store"
)

// putIndexRequest is the body of POST /v1/entries/{id}/index. The indexer
// librarian sends the reverse-lookup phrases it extracted for one entry.
type putIndexRequest struct {
	Symptoms []string `json:"symptoms,omitempty"`
	Triggers []struct {
		Phrase string `json:"phrase"`
		Domain string `json:"domain,omitempty"`
	} `json:"triggers,omitempty"`
	// Source labels who produced these phrases (audit). Defaults to "indexer".
	Source string `json:"source,omitempty"`
}

type putIndexResponse struct {
	EntryID  string `json:"entry_id"`
	Symptoms int    `json:"symptoms"`
	Triggers int    `json:"triggers"`
	Source   string `json:"source"`
}

// putEntryIndex populates the reverse-lookup index for one entry: symptom
// phrases and (phrase, domain) triggers. It is the agent-facing counterpart to
// the automatic enrichment-time indexing. omoikane runs without an LLM, so
// nothing extracts symptoms/triggers on write and symptoms_index/triggers_index
// stay empty — which makes /v1/lookup/by-symptom|trigger return nothing. The
// indexer librarian fills the gap by reading entries and POSTing here.
//
// Idempotent per dimension: each call REPLACES the entry's symptoms (and/or its
// triggers) with the supplied set, so re-indexing is safe and the index is
// always regenerable from the entries. Sending only triggers leaves symptoms
// untouched, and vice versa.
func (h *Handler) putEntryIndex(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Verify the entry exists (clean 404 instead of a dangling index row).
	if _, err := h.Store.GetEntry(httpCtx(r), id); err != nil {
		writeStoreError(w, err)
		return
	}

	var req putIndexRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}

	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "indexer"
	}

	symptoms := make([]string, 0, len(req.Symptoms))
	for _, s := range req.Symptoms {
		if s = strings.TrimSpace(s); s != "" {
			symptoms = append(symptoms, s)
		}
	}
	triggers := make([]store.IndexedTrigger, 0, len(req.Triggers))
	for _, t := range req.Triggers {
		p := strings.TrimSpace(t.Phrase)
		if p == "" {
			continue
		}
		triggers = append(triggers, store.IndexedTrigger{
			Phrase: p, Domain: strings.TrimSpace(t.Domain),
		})
	}

	if len(symptoms) == 0 && len(triggers) == 0 {
		writeError(w, http.StatusBadRequest, CodeMissingFields,
			"at least one non-empty symptom or trigger is required", nil)
		return
	}

	if len(symptoms) > 0 {
		if err := h.Store.ReplaceSymptoms(httpCtx(r), id, symptoms, source); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	if len(triggers) > 0 {
		if err := h.Store.ReplaceTriggers(httpCtx(r), id, triggers, source); err != nil {
			writeStoreError(w, err)
			return
		}
	}

	writeJSON(w, http.StatusOK, putIndexResponse{
		EntryID:  id,
		Symptoms: len(symptoms),
		Triggers: len(triggers),
		Source:   source,
	})
}
