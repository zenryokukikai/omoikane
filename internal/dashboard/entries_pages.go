package dashboard

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// ----------------------------------------------------------------------
// Entry-centric pages: the filterable /entries list, the /entries/new
// form, per-project and per-entry views, entry history, and the shared
// pagination + space-select helpers the list pages use.
// ----------------------------------------------------------------------

// entriesList renders a filterable list of entries. Filters accepted via
// query params (each optional):
//
//	?type=<lesson|trap|decision|design|incident|note|librarian_meta|external_finding>
//	?project=<id>
//	?status=<DRAFT|ACTIVE|SUPERSEDED|ARCHIVED|...>
//	?tag=<tag>
//	?q=<full-text>
//	?limit=<N> (default 100, max 500)
//	?include_superseded=true
//
// Useful URLs for the librarian flow:
//
//	/entries?type=librarian_meta              — every librarian's output
//	/entries?type=librarian_meta&tag=cataloger — cataloger's output only
//	/entries?type=trap                        — every trap in the corpus
//	/entries?project=lipsync                  — lipsync's full corpus
func (h *Handler) entriesList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.EntryFilter{
		ProjectID:         q.Get("project"),
		Type:              q.Get("type"),
		Status:            q.Get("status"),
		Tag:               q.Get("tag"),
		Query:             q.Get("q"),
		IncludeSuperseded: q.Get("include_superseded") == "true",
	}
	// ?space= narrows to one visible space (視界∩指定). A space outside
	// the viewer's visibility — or one that does not exist — answers
	// 404, indistinguishable by design (the same oracle-sealing
	// semantics as /entries/{id}).
	if sp := q.Get("space"); sp != "" {
		if err := h.Store.RequireVisibleSpace(r.Context(), sp); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		filter.SpaceID = sp
	}
	limit := 50
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	page := pageParam(r)
	filter.Limit = limit
	filter.Offset = (page - 1) * limit
	entries, total, err := h.Store.ListEntries(r.Context(), filter)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc := h.renderCtx(r)
	pc.Title = "omoikane — entries"
	pc.Entries = entries
	pc.EntriesTotal = total
	pc.EntriesFilter = filter
	pc.SpaceOptions = h.spaceOptions(r.Context(), pc.Me)
	pc.Pagination = buildPagination(r, total, page, limit)
	h.render(w, "entries", pc)
}

// entryNewPage renders the human entry-creation form (issue #71).
// GET-only: the submission is a JS fetch to POST /v1/entries with the
// session cookie, keeping authorization, space-404 semantics and the
// secrets scan on the single API write path. ?space=<id> presets the
// space select — a space outside the viewer's visibility (or a missing
// one) answers 404, same oracle-sealing as /entries?space=.
func (h *Handler) entryNewPage(w http.ResponseWriter, r *http.Request) {
	pc := h.renderCtx(r)
	pc.Title = "omoikane — 新規エントリ"
	if sp := r.URL.Query().Get("space"); sp != "" {
		if err := h.Store.RequireVisibleSpace(r.Context(), sp); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		// Reuse the entries-list filter slot as "the preselected space" —
		// entry_new.html reads the same field the /entries select does.
		pc.EntriesFilter.SpaceID = sp
	}
	pc.SpaceOptions = h.spaceOptions(r.Context(), pc.Me)
	h.render(w, "entry_new", pc)
}

// spaceOptions returns the viewer's visible spaces as select choices,
// or nil when fewer than two are visible (an internal-only view has
// nothing to switch between, so the select stays hidden). One
// ListSpaces query filtered in memory against the context's visibility
// — no per-space lookups. Best-effort: an error just hides the select.
func (h *Handler) spaceOptions(ctx context.Context, me *store.User) []spaceOption {
	all, err := h.Store.ListSpaces(ctx)
	if err != nil {
		return nil
	}
	visible, restricted := store.VisibleSpacesFromContext(ctx)
	var vis map[string]bool
	if restricted {
		vis = make(map[string]bool, len(visible))
		for _, id := range visible {
			vis[id] = true
		}
	}
	opts := make([]spaceOption, 0, len(all))
	for _, sp := range all {
		if vis != nil && !vis[sp.ID] {
			continue
		}
		opts = append(opts, spaceOption{ID: sp.ID, Label: spaceLabel(sp, me)})
	}
	if len(opts) < 2 {
		return nil
	}
	return opts
}

// spaceLabel is the human display name for a space: the viewer's own
// personal space reads 「個人スペース」, internal reads as the org-wide
// default, everything else keeps its stored name.
func spaceLabel(sp *store.Space, me *store.User) string {
	if sp.ID == store.SpaceInternal {
		return "internal(全体)"
	}
	if me != nil && sp.ID == store.PersonalSpaceID(me.ID) {
		return "個人スペース"
	}
	return sp.Name
}

// pagination is the data a list page needs to render prev/next controls.
// PrevURL/NextURL are "" when that direction doesn't exist. The URLs
// preserve the request's existing query (filters, token) and only swap
// the page number.
type pagination struct {
	Page, Pages, From, To, Total int
	PrevURL, NextURL             string
}

// pageParam reads ?page (1-based, default 1, never < 1).
func pageParam(r *http.Request) int {
	if n, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && n > 1 {
		return n
	}
	return 1
}

// buildPagination computes the prev/next/window for a list of `total`
// items shown `pageSize` per page at the given 1-based `page`.
func buildPagination(r *http.Request, total, page, pageSize int) *pagination {
	if pageSize < 1 {
		pageSize = 1
	}
	pages := (total + pageSize - 1) / pageSize
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	from := (page-1)*pageSize + 1
	to := page * pageSize
	if to > total {
		to = total
	}
	if total == 0 {
		from = 0
	}
	mk := func(p int) string {
		q := r.URL.Query()
		q.Set("page", strconv.Itoa(p))
		return r.URL.Path + "?" + q.Encode()
	}
	pg := &pagination{Page: page, Pages: pages, From: from, To: to, Total: total}
	if page > 1 {
		pg.PrevURL = mk(page - 1)
	}
	if page < pages {
		pg.NextURL = mk(page + 1)
	}
	return pg
}

func (h *Handler) project(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p, err := h.Store.GetProject(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	const pageSize = 50
	page := pageParam(r)
	entries, total, err := h.Store.ListEntries(r.Context(), store.EntryFilter{
		ProjectID: id, Limit: pageSize, Offset: (page - 1) * pageSize, IncludeSuperseded: true,
	})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc := h.renderCtx(r)
	pc.Title = "omoikane — " + p.Name
	pc.Project = p
	pc.Entries = entries
	pc.Pagination = buildPagination(r, total, page, pageSize)
	h.render(w, "project", pc)
}

func (h *Handler) entry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	pc := h.renderCtx(r)

	var (
		e   *store.Entry
		err error
	)
	if asOf := r.URL.Query().Get("as_of"); asOf != "" {
		t, perr := time.Parse(time.RFC3339, asOf)
		if perr != nil {
			http.Error(w, "as_of must be RFC3339", http.StatusBadRequest)
			return
		}
		pc.AsOf = asOf
		e, err = h.Store.GetEntryAsOf(r.Context(), id, t)
	} else {
		e, err = h.Store.GetEntry(r.Context(), id)
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc.Title = "omoikane — " + e.Title
	pc.Entry = e
	// Space badge — internal entries carry no badge (internal is the
	// unmarked default); anything else shows the space's display name.
	// One GetSpace for the single entry on this page — never a per-row
	// lookup on list pages. Best-effort: on error the badge is omitted.
	if e.SpaceID != "" && e.SpaceID != store.SpaceInternal {
		if sp, spErr := h.Store.GetSpace(r.Context(), e.SpaceID); spErr == nil {
			pc.EntrySpaceName = spaceLabel(sp, pc.Me)
		}
	}
	// Project overview — the domain primer that lets a reader without this
	// project's domain knowledge decode its entries. Best-effort.
	if e.ProjectID != "" {
		if proj, pErr := h.Store.GetProject(r.Context(), e.ProjectID); pErr == nil {
			pc.Project = proj
		}
	}
	// Best-effort enrichment for Phase 3 panels — failures degrade silently.
	if sig, sErr := h.Store.EntrySignal(r.Context(), id); sErr == nil {
		pc.Signals = sig
	}
	if cases, cErr := h.Store.ListCases(r.Context(), id, 20); cErr == nil {
		pc.Cases = cases
	}
	if rels, rErr := h.Store.ListRelationsFrom(r.Context(), id); rErr == nil {
		pc.Relations = rels
	}
	if back, bErr := h.Store.ListRelationsTo(r.Context(), id); bErr == nil {
		pc.Backlinks = back
	}
	// Reverse-lookup index for this entry (what symptoms/triggers reach it).
	if syms, sErr := h.Store.EntrySymptoms(r.Context(), id); sErr == nil {
		pc.EntrySymptoms = syms
	}
	if trigs, tErr := h.Store.EntryTriggers(r.Context(), id); tErr == nil {
		pc.EntryTriggers = trigs
	}
	// UseCases this entry belongs to (chips on the entry page).
	if ucs, uErr := h.Store.ListEntryUseCases(r.Context(), id); uErr == nil {
		pc.EntryUseCases = ucs
	}
	// Entry author — resolve created_by user ID to a full User for avatar display.
	if e.CreatedBy != "" {
		if au, aErr := h.Store.GetUser(r.Context(), e.CreatedBy); aErr == nil {
			pc.EntryAuthor = au
		}
	}
	if pc.Me != nil {
		pc.Bookmarked, _ = h.Store.IsBookmarked(r.Context(), pc.Me.ID, e.ID)
	}
	// Review/discussion comments (humans + agents) — §23.21.
	if cs, cErr := h.Store.ListComments(r.Context(), id); cErr == nil {
		pc.Comments = cs
	}
	h.render(w, "entry", pc)
}

func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	hist, err := h.Store.EntryHistory(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc := h.renderCtx(r)
	pc.Title = "omoikane — history " + id
	pc.History = hist
	// Surface the current state for navigation links.
	if cur, err := h.Store.GetEntry(r.Context(), id); err == nil {
		pc.Entry = cur
	}
	h.render(w, "entry_history", pc)
}
