package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kojira/omoikane/internal/auth"
	"github.com/kojira/omoikane/internal/config"
	"github.com/kojira/omoikane/internal/enrich"
	"github.com/kojira/omoikane/internal/secrets"
	"github.com/kojira/omoikane/internal/store"
)

type entryRequest struct {
	ProjectID           string   `json:"project_id"`
	Type                string   `json:"type"`
	Title               string   `json:"title"`
	Status              string   `json:"status,omitempty"`
	Symptom             string   `json:"symptom,omitempty"`
	RootCause           string   `json:"root_cause,omitempty"`
	Resolution          string   `json:"resolution,omitempty"`
	Prohibited          string   `json:"prohibited,omitempty"`
	AttemptedApproaches string   `json:"attempted_approaches,omitempty"`
	ObservedBehavior    string   `json:"observed_behavior,omitempty"`
	Hypotheses          string   `json:"hypotheses,omitempty"`
	Body                string   `json:"body"`
	BodyFormat          string   `json:"body_format,omitempty"`
	Scope               any      `json:"scope,omitempty"`
	Metadata            any      `json:"metadata,omitempty"`
	Tags                []string `json:"tags,omitempty"`
}

type entryResponse struct {
	*store.Entry
	Enrichment enrichmentReport `json:"enrichment,omitempty"`
}

type enrichmentReport struct {
	Version           int      `json:"version"`
	Source            string   `json:"source"`
	TagsAdded         []string `json:"tags_added,omitempty"`
	SymptomsExtracted []string `json:"symptoms_extracted,omitempty"`
	TriggersExtracted []string `json:"triggers_extracted,omitempty"`
}

func (h *Handler) createEntry(w http.ResponseWriter, r *http.Request) {
	var req entryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if !store.ValidEntryType(req.Type) {
		writeError(w, http.StatusBadRequest, CodeInvalidType,
			"type must be one of trap|decision|design|lesson|incident|librarian_meta|external_finding",
			map[string]any{"got": req.Type})
		return
	}
	if req.Status != "" && !store.ValidStatus(req.Status) {
		writeError(w, http.StatusBadRequest, CodeInvalidStatus,
			"invalid status",
			map[string]any{"got": req.Status})
		return
	}
	scopeJSON := marshalJSONField(req.Scope)
	metaJSON := marshalJSONField(req.Metadata)

	// Secret/PII scan (write-time per §12.3).
	if h.rejectIfSecrets(w, secrets.Doc{
		Title:               req.Title,
		Body:                req.Body,
		Symptom:             req.Symptom,
		RootCause:           req.RootCause,
		Resolution:          req.Resolution,
		Prohibited:          req.Prohibited,
		AttemptedApproaches: req.AttemptedApproaches,
		ObservedBehavior:    req.ObservedBehavior,
		Hypotheses:          req.Hypotheses,
		Metadata:            metaJSON,
	}) {
		return
	}

	tok := auth.FromContext(r.Context())
	createdBy, createdByRole := identifyCreator(tok)

	e := &store.Entry{
		ProjectID:           strings.TrimSpace(req.ProjectID),
		Type:                req.Type,
		Title:               strings.TrimSpace(req.Title),
		Status:              req.Status,
		Symptom:             req.Symptom,
		RootCause:           req.RootCause,
		Resolution:          req.Resolution,
		Prohibited:          req.Prohibited,
		AttemptedApproaches: req.AttemptedApproaches,
		ObservedBehavior:    req.ObservedBehavior,
		Hypotheses:          req.Hypotheses,
		Body:                req.Body,
		BodyFormat:          req.BodyFormat,
		Scope:               scopeJSON,
		Metadata:            metaJSON,
		CreatedBy:           createdBy,
		CreatedByRole:       createdByRole,
		Tags:                req.Tags,
	}

	id, err := h.Store.CreateEntry(httpCtx(r), e)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// runEnrichment writes tags + enrichment metadata to the store AND
	// returns the merged tag set so we don't need a re-fetch (eliminating
	// the dead post-success-then-GetEntry error branch).
	report, mergedTags := h.runEnrichment(r.Context(), id, e, req.Tags)
	e.Tags = mergedTags
	if report.Version > 0 {
		e.EnrichmentVersion = report.Version
		now := time.Now().UTC()
		e.EnrichmentAt = &now
	}
	writeJSON(w, http.StatusCreated, entryResponse{Entry: e, Enrichment: report})
}

// runEnrichment invokes the configured enricher, persists the resulting
// tags + symptoms + triggers + enrichment version, and returns both the
// report and the merged tag set so the caller can update its in-memory
// entry without a re-fetch.
//
// Failures during the side-effects (ReplaceTags / ReplaceSymptoms /
// ReplaceTriggers / SetEnrichment) are logged and otherwise swallowed —
// by the same fail-open rule that lets a missing LLM not break entry
// creation.
func (h *Handler) runEnrichment(parent context.Context, id string, e *store.Entry, userTags []string) (enrichmentReport, []string) {
	if h.Enricher == nil {
		return enrichmentReport{}, normaliseUserTags(userTags)
	}
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	res, err := h.Enricher.Enrich(ctx, enrich.Input{
		Type:                e.Type,
		Title:               e.Title,
		Body:                e.Body,
		Symptom:             e.Symptom,
		RootCause:           e.RootCause,
		Resolution:          e.Resolution,
		Prohibited:          e.Prohibited,
		AttemptedApproaches: e.AttemptedApproaches,
		ObservedBehavior:    e.ObservedBehavior,
		Hypotheses:          e.Hypotheses,
	})
	if err != nil {
		h.Logger.Warn("enrichment failed", "entry", id, "err", err)
		return enrichmentReport{}, normaliseUserTags(userTags)
	}
	seen := map[string]bool{}
	merged := make([]string, 0, len(userTags)+len(res.Tags))
	for _, t := range userTags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		merged = append(merged, t)
	}
	added := make([]string, 0, len(res.Tags))
	for _, t := range res.Tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		merged = append(merged, t)
		added = append(added, t)
	}
	if err := h.Store.ReplaceTags(ctx, id, merged, res.Source); err != nil {
		h.Logger.Warn("tag write after enrichment failed", "entry", id, "err", err)
	}
	if len(res.Symptoms) > 0 {
		if err := h.Store.ReplaceSymptoms(ctx, id, res.Symptoms, res.Source); err != nil {
			h.Logger.Warn("symptom write failed", "entry", id, "err", err)
		}
	}
	if len(res.Triggers) > 0 {
		triggers := make([]store.IndexedTrigger, 0, len(res.Triggers))
		for _, t := range res.Triggers {
			triggers = append(triggers, store.IndexedTrigger{
				Phrase: t.Phrase, Domain: t.Domain,
			})
		}
		if err := h.Store.ReplaceTriggers(ctx, id, triggers, res.Source); err != nil {
			h.Logger.Warn("trigger write failed", "entry", id, "err", err)
		}
	}
	if err := h.Store.SetEnrichment(ctx, id, res.Version); err != nil {
		h.Logger.Warn("enrichment_version write failed", "entry", id, "err", err)
	}
	return enrichmentReport{
		Version:           res.Version,
		Source:            res.Source,
		TagsAdded:         added,
		SymptomsExtracted: res.Symptoms,
		TriggersExtracted: triggerPhrases(res.Triggers),
	}, merged
}

func triggerPhrases(t []enrich.Trigger) []string {
	out := make([]string, len(t))
	for i, x := range t {
		out[i] = x.Phrase
	}
	return out
}

// normaliseUserTags applies the same trim/dedupe pipeline runEnrichment
// does to user tags so the in-memory entry matches what the store layer
// would have persisted.
func normaliseUserTags(tags []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func (h *Handler) getEntry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if asOf := r.URL.Query().Get("as_of"); asOf != "" {
		t, err := time.Parse(time.RFC3339, asOf)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidAsOf,
				"as_of must be RFC3339 timestamp",
				map[string]any{"got": asOf, "format": "RFC3339"})
			return
		}
		e, err := h.Store.GetEntryAsOf(httpCtx(r), id, t)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, e)
		return
	}
	e, err := h.Store.GetEntry(httpCtx(r), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// ETag tied to version for OCC-aware clients.
	w.Header().Set("ETag", `"`+strconv.Itoa(e.Version)+`"`)
	writeJSON(w, http.StatusOK, e)
}

func (h *Handler) listEntries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.EntryFilter{
		ProjectID:         q.Get("project_id"),
		Type:              q.Get("type"),
		Status:            q.Get("status"),
		Tag:               q.Get("tag"),
		Query:             q.Get("q"),
		IncludeSuperseded: q.Get("include_superseded") == "true",
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Offset = n
		}
	}
	entries, total, err := h.Store.ListEntries(httpCtx(r), f)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	nextOffset := f.Offset + len(entries)
	hasMore := nextOffset < total
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"pagination": map[string]any{
			"limit":       limit,
			"offset":      f.Offset,
			"total":       total,
			"next_offset": nextOffset,
			"has_more":    hasMore,
		},
	})
}

func (h *Handler) updateEntry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// OCC via If-Match. The header value can be `"5"` (quoted ETag) or `5`.
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, http.StatusPreconditionRequired, CodePreconditionRequired,
			"If-Match header required for PATCH",
			map[string]any{"header": "If-Match"})
		return
	}
	expectedVersion, err := parseIfMatch(ifMatch)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest,
			"If-Match must be an integer version (optionally double-quoted): "+err.Error(),
			map[string]any{"got": ifMatch})
		return
	}

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	patch := store.EntryPatch{ExpectedVersion: expectedVersion}

	bindString := func(key string, dst **string) error {
		if v, ok := raw[key]; ok {
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return err
			}
			*dst = &s
		}
		return nil
	}
	for _, b := range []struct {
		key string
		dst **string
	}{
		{"title", &patch.Title}, {"status", &patch.Status},
		{"symptom", &patch.Symptom}, {"root_cause", &patch.RootCause},
		{"resolution", &patch.Resolution}, {"prohibited", &patch.Prohibited},
		{"attempted_approaches", &patch.AttemptedApproaches},
		{"observed_behavior", &patch.ObservedBehavior},
		{"hypotheses", &patch.Hypotheses}, {"body", &patch.Body},
		{"body_format", &patch.BodyFormat},
	} {
		if err := bindString(b.key, b.dst); err != nil {
			writeError(w, http.StatusBadRequest, CodeBadJSON,
				"field "+b.key+": "+err.Error(), nil)
			return
		}
	}
	bindJSON := func(key string, dst **string) {
		if v, ok := raw[key]; ok {
			s := string(v)
			*dst = &s
		}
	}
	bindJSON("scope", &patch.Scope)
	bindJSON("metadata", &patch.Metadata)
	if v, ok := raw["tags"]; ok {
		var tags []string
		if err := json.Unmarshal(v, &tags); err != nil {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "tags: "+err.Error(), nil)
			return
		}
		patch.Tags = &tags
	}

	if patch.Status != nil && !store.ValidStatus(*patch.Status) {
		writeError(w, http.StatusBadRequest, CodeInvalidStatus,
			"invalid status", map[string]any{"got": *patch.Status})
		return
	}

	// Re-scan secrets on patched fields. We need the post-patch state — for
	// fields not in the patch we use the current stored value.
	cur, err := h.Store.GetEntry(httpCtx(r), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	merged := mergePatchOnto(cur, patch)
	if h.rejectIfSecrets(w, merged) {
		return
	}

	tok := auth.FromContext(r.Context())
	patch.ChangedBy, patch.ChangedByRole = identifyCreator(tok)
	if v, ok := raw["change_summary"]; ok {
		var s string
		_ = json.Unmarshal(v, &s)
		patch.ChangeSummary = s
	}

	_, updated, err := h.Store.UpdateEntry(httpCtx(r), id, patch)
	if err != nil {
		if errors.Is(err, store.ErrVersionMismatch) {
			writeError(w, http.StatusConflict, CodeVersionMismatch,
				"OCC version mismatch",
				map[string]any{"current_version": cur.Version, "expected_version": expectedVersion})
			return
		}
		writeStoreError(w, err)
		return
	}
	// Re-run enrichment when content changed. runEnrichment returns the
	// merged tag set so we update the in-memory entry without an extra
	// GetEntry round-trip.
	if patch.Body != nil || patch.Symptom != nil || patch.Title != nil {
		report, merged := h.runEnrichment(r.Context(), id, updated, updated.Tags)
		updated.Tags = merged
		if report.Version > 0 {
			updated.EnrichmentVersion = report.Version
			now := time.Now().UTC()
			updated.EnrichmentAt = &now
		}
	}
	w.Header().Set("ETag", `"`+strconv.Itoa(updated.Version)+`"`)
	writeJSON(w, http.StatusOK, updated)
}

func (h *Handler) deleteEntry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tok := auth.FromContext(r.Context())
	changedBy, role := identifyCreator(tok)
	if err := h.Store.SoftDeleteEntry(httpCtx(r), id, changedBy, role); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, CodeNotFound, "entry not found",
				map[string]any{"id": id})
			return
		}
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) entryHistory(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	hist, err := h.Store.EntryHistory(httpCtx(r), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": hist})
}

// ===== helpers =====

// rejectIfSecrets runs the scanner and, when running in enforce mode and
// findings exist, writes a 422 SECRETS_DETECTED response. Returns true when
// the caller should stop processing. In warn / off mode it never rejects;
// warn mode emits a log line.
func (h *Handler) rejectIfSecrets(w http.ResponseWriter, d secrets.Doc) bool {
	if h.SecretsMode == config.SecretsOff {
		return false
	}
	findings := secrets.Scan(d)
	if len(findings) == 0 {
		return false
	}
	if h.SecretsMode == config.SecretsWarn {
		h.Logger.Warn("secrets detected (warn mode)", "findings", len(findings))
		return false
	}
	writeError(w, http.StatusUnprocessableEntity, CodeSecretsDetected,
		"Write rejected: secret or PII detected. Remove the offending data and retry.",
		map[string]any{"findings": findings})
	return true
}

// mergePatchOnto applies the patch's string fields onto a copy of cur and
// returns a secrets.Doc capturing the post-patch state for re-scan.
func mergePatchOnto(cur *store.Entry, p store.EntryPatch) secrets.Doc {
	val := func(ptr *string, fallback string) string {
		if ptr != nil {
			return *ptr
		}
		return fallback
	}
	return secrets.Doc{
		Title:               val(p.Title, cur.Title),
		Body:                val(p.Body, cur.Body),
		Symptom:             val(p.Symptom, cur.Symptom),
		RootCause:           val(p.RootCause, cur.RootCause),
		Resolution:          val(p.Resolution, cur.Resolution),
		Prohibited:          val(p.Prohibited, cur.Prohibited),
		AttemptedApproaches: val(p.AttemptedApproaches, cur.AttemptedApproaches),
		ObservedBehavior:    val(p.ObservedBehavior, cur.ObservedBehavior),
		Hypotheses:          val(p.Hypotheses, cur.Hypotheses),
		Metadata:            val(p.Metadata, cur.Metadata),
	}
}

// identifyCreator picks `created_by` / `created_by_role` from the
// authenticated token. The user_id (if any) is the actor; otherwise we use
// the token name. Role defaults to 'human' for ordinary tokens; later phases
// will populate 'librarian:<role>' from a dedicated header.
func identifyCreator(tok *store.APIToken) (string, string) {
	if tok == nil {
		return "", ""
	}
	if tok.UserID != "" {
		return tok.UserID, "human"
	}
	return "token:" + tok.Name, "agent"
}

func parseIfMatch(v string) (int, error) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, `"`)
	v = strings.TrimSuffix(v, `"`)
	return strconv.Atoi(v)
}

