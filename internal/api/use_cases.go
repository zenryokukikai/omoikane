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
	"github.com/kojira/omoikane/internal/store"
)

// ============================================================
// Request / response types
// ============================================================

type useCaseUpsertReq struct {
	Slug          string `json:"slug,omitempty"` // optional; derived from name_en if empty
	NameJA        string `json:"name_ja"`
	NameEN        string `json:"name_en"`
	DescriptionJA string `json:"description_ja,omitempty"`
	DescriptionEN string `json:"description_en,omitempty"`
	Domain        string `json:"domain,omitempty"`
	Source        string `json:"source,omitempty"`
	ParentID      string `json:"parent_id,omitempty"`
}

type useCaseJSON struct {
	ID            string    `json:"id"`
	Slug          string    `json:"slug"`
	NameJA        string    `json:"name_ja"`
	NameEN        string    `json:"name_en"`
	DescriptionJA string    `json:"description_ja"`
	DescriptionEN string    `json:"description_en"`
	Domain        string    `json:"domain,omitempty"`
	Source        string    `json:"source"`
	ParentID      string    `json:"parent_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func toUseCaseJSON(uc *store.UseCase) useCaseJSON {
	return useCaseJSON{
		ID: uc.ID, Slug: uc.Slug, NameJA: uc.NameJA, NameEN: uc.NameEN,
		DescriptionJA: uc.DescriptionJA, DescriptionEN: uc.DescriptionEN,
		Domain: uc.Domain, Source: uc.Source, ParentID: uc.ParentID,
		CreatedAt: uc.CreatedAt, UpdatedAt: uc.UpdatedAt,
	}
}

type useCaseSummaryJSON struct {
	useCaseJSON
	EntryCount    int                      `json:"entry_count"`
	ChildCount    int                      `json:"child_count"`
	SampleEntries []useCaseSampleEntryJSON `json:"sample_entries,omitempty"`
}

type useCaseSampleEntryJSON struct {
	EntryID   string `json:"entry_id"`
	Title     string `json:"title"`
	Type      string `json:"type"`
	ProjectID string `json:"project_id"`
}

type linkUseCaseEntryReq struct {
	EntryID string `json:"entry_id"`
	Source  string `json:"source,omitempty"`
}

// ============================================================
// Handlers
// ============================================================

// upsertUseCase creates or updates a UseCase, keyed by slug (derived from
// name_en if not supplied). Multi-write safe: parallel indexers converge.
func (h *Handler) upsertUseCase(w http.ResponseWriter, r *http.Request) {
	var req useCaseUpsertReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if strings.TrimSpace(req.NameJA) == "" || strings.TrimSpace(req.NameEN) == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields,
			"name_ja and name_en required", nil)
		return
	}
	uc, err := h.Store.UpsertUseCase(httpCtx(r), &store.UseCase{
		Slug:          req.Slug,
		NameJA:        req.NameJA,
		NameEN:        req.NameEN,
		DescriptionJA: req.DescriptionJA,
		DescriptionEN: req.DescriptionEN,
		Domain:        req.Domain,
		Source:        req.Source,
		ParentID:      req.ParentID,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toUseCaseJSON(uc))
}

// listUseCases returns paginated UseCase summaries (UseCase + entry_count +
// sample entries). Filters: ?project, ?domain, ?q, ?limit, ?offset.
func (h *Handler) listUseCases(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := atoiOr(q.Get("limit"), 30)
	offset := atoiOr(q.Get("offset"), 0)
	// Mirror the store's defensive clamp so the echoed `limit` field
	// matches what was actually used. Over-limit no longer silently
	// drops to the default — it caps at 200.
	if limit < 1 {
		limit = 30
	} else if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	out, total, err := h.Store.ListUseCases(httpCtx(r), store.UseCaseFilter{
		ProjectID: q.Get("project"),
		Domain:    q.Get("domain"),
		Query:     q.Get("q"),
		Level:     q.Get("level"),     // "" | "top" | "all"
		ParentID:  q.Get("parent_id"), // when set, returns direct children only
	}, limit, offset)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	rows := make([]useCaseSummaryJSON, 0, len(out))
	for _, s := range out {
		row := useCaseSummaryJSON{
			useCaseJSON: toUseCaseJSON(&s.UseCase),
			EntryCount:  s.EntryCount,
			ChildCount:  s.ChildCount,
		}
		for _, e := range s.SampleEntries {
			row.SampleEntries = append(row.SampleEntries, useCaseSampleEntryJSON{
				EntryID: e.EntryID, Title: e.Title, Type: e.Type, ProjectID: e.ProjectID,
			})
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"use_cases": rows,
		"total":     total,
		"limit":     limit,
		"offset":    offset,
	})
}

// getUseCase returns one UseCase plus a paginated slice of its entries.
// Accepts the URL param as either a "U-XXXXXX" id or a slug.
func (h *Handler) getUseCase(w http.ResponseWriter, r *http.Request) {
	ref := chi.URLParam(r, "ref")
	uc, err := h.resolveUseCaseRef(httpCtx(r), ref)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	q := r.URL.Query()
	limit := atoiOr(q.Get("limit"), 30)
	offset := atoiOr(q.Get("offset"), 0)
	entries, total, err := h.Store.ListUseCaseEntries(httpCtx(r), uc.ID, limit, offset)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Tree context — parent (if any) + direct children. Lets a tree-aware
	// caller draw the breadcrumb and the drilldown without a second round trip.
	var parent *useCaseJSON
	if uc.ParentID != "" {
		if p, err := h.Store.GetUseCase(httpCtx(r), uc.ParentID); err == nil && p != nil {
			j := toUseCaseJSON(p)
			parent = &j
		}
	}
	childSums, _, err := h.Store.ListUseCases(httpCtx(r), store.UseCaseFilter{ParentID: uc.ID}, 200, 0)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	children := make([]useCaseSummaryJSON, 0, len(childSums))
	for _, c := range childSums {
		children = append(children, useCaseSummaryJSON{
			useCaseJSON: toUseCaseJSON(&c.UseCase),
			EntryCount:  c.EntryCount,
			ChildCount:  c.ChildCount,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"use_case":      toUseCaseJSON(uc),
		"parent":        parent,
		"children":      children,
		"entries":       entries,
		"entries_total": total,
		"limit":         limit,
		"offset":        offset,
	})
}

// linkUseCaseEntry attaches an entry to a UseCase.
func (h *Handler) linkUseCaseEntry(w http.ResponseWriter, r *http.Request) {
	ref := chi.URLParam(r, "ref")
	uc, err := h.resolveUseCaseRef(httpCtx(r), ref)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var req linkUseCaseEntryReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if strings.TrimSpace(req.EntryID) == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "entry_id required", nil)
		return
	}
	// Reject unknown entries up front (404 vs dangling link row).
	if _, err := h.Store.GetEntry(httpCtx(r), req.EntryID); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := h.Store.LinkUseCaseEntry(httpCtx(r), uc.ID, req.EntryID, req.Source); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"use_case_id": uc.ID, "entry_id": req.EntryID, "linked": true,
	})
}

// unlinkUseCaseEntry detaches an entry from a UseCase. Idempotent: 204
// whether the link existed or not.
func (h *Handler) unlinkUseCaseEntry(w http.ResponseWriter, r *http.Request) {
	ref := chi.URLParam(r, "ref")
	uc, err := h.resolveUseCaseRef(httpCtx(r), ref)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	entryID := chi.URLParam(r, "entryID")
	if entryID == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "entry_id required", nil)
		return
	}
	if err := h.Store.UnlinkUseCaseEntry(httpCtx(r), uc.ID, entryID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listEntryUseCases returns the UseCases an entry belongs to.
func (h *Handler) listEntryUseCases(w http.ResponseWriter, r *http.Request) {
	entryID := chi.URLParam(r, "id")
	if _, err := h.Store.GetEntry(httpCtx(r), entryID); err != nil {
		writeStoreError(w, err)
		return
	}
	rows, err := h.Store.ListEntryUseCases(httpCtx(r), entryID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := make([]useCaseJSON, 0, len(rows))
	for _, r := range rows {
		out = append(out, toUseCaseJSON(&r.UseCase))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entry_id":  entryID,
		"use_cases": out,
	})
}

// ============================================================
// Helpers
// ============================================================

// resolveUseCaseRef accepts either a "U-XXXXXX" id (starts with "U-") or
// a slug, and returns the resolved UseCase.
func (h *Handler) resolveUseCaseRef(ctx context.Context, ref string) (*store.UseCase, error) {
	if strings.HasPrefix(ref, "U-") {
		return h.Store.GetUseCase(ctx, ref)
	}
	return h.Store.GetUseCaseBySlug(ctx, ref)
}

func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// Ensure errors package import is used (writeStoreError handles ErrNotFound etc.)
var _ = errors.Is
