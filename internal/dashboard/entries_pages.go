package dashboard

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
	// Quick views carry the current {space, token} and swap {type|status};
	// the active one is the view matching the filter (see buildQuickViews).
	pc.QuickViews = buildQuickViews(filter, pc.Token)
	// Empty-state guidance is only computed when there is nothing to show:
	// it names the filters actually in effect and offers ways to clear them.
	if total == 0 {
		pc.EntriesEmpty = h.buildEntriesEmpty(r.Context(), pc, filter)
	}
	h.render(w, "entries", pc)
}

// quickView is one entry in the /entries "Quick views:" row. The Href is
// built Go-side so the {space, token} carry-over contract lives in ONE
// place (buildQuickViews) instead of being hand-repeated per link in the
// template — adding a new query dimension no longer means editing 8 lines.
type quickView struct {
	Label  string
	Href   template.URL
	Active bool
}

// buildQuickViews assembles the fixed set of quick views. Each carries the
// viewer's current visible space and token forward and swaps in its own
// type/status; project/tag/q/include_superseded are form-side refinements,
// not part of a view, so they are intentionally dropped on a view click.
func buildQuickViews(f store.EntryFilter, token string) []quickView {
	base := func(setType, setStatus string) template.URL {
		q := url.Values{}
		if setType != "" {
			q.Set("type", setType)
		}
		if setStatus != "" {
			q.Set("status", setStatus)
		}
		if f.SpaceID != "" {
			q.Set("space", f.SpaceID)
		}
		if token != "" {
			q.Set("token", token)
		}
		if len(q) == 0 {
			return template.URL("/entries")
		}
		return template.URL("/entries?" + q.Encode())
	}
	defs := []struct{ Label, Type, Status string }{
		{"all", "", ""},
		{"🗂️ librarian output", "librarian_meta", ""},
		{"⚠️ traps", "trap", ""},
		{"💡 lessons", "lesson", ""},
		{"📋 decisions", "decision", ""},
		{"🏗️ designs", "design", ""},
		{"🚨 incidents", "incident", ""},
		{"📝 drafts", "", "DRAFT"},
	}
	out := make([]quickView, 0, len(defs))
	for _, d := range defs {
		out = append(out, quickView{
			Label:  d.Label,
			Href:   base(d.Type, d.Status),
			Active: activeQuickView(f, d.Type, d.Status),
		})
	}
	return out
}

// activeQuickView reports whether the current filter IS this quick view:
// the (type, status) pair matches and no other refinement is in effect.
// space and include_superseded are excluded — space is the base every view
// carries, and include_superseded is orthogonal to "which view". When a
// project/tag/q refinement is present, no view is active (a first-class
// state: the row shows nothing highlighted rather than a wrong guess).
func activeQuickView(f store.EntryFilter, viewType, viewStatus string) bool {
	return f.Type == viewType && f.Status == viewStatus &&
		f.ProjectID == "" && f.Tag == "" && f.Query == ""
}

// entriesEmpty is the empty-state guidance shown when a filtered /entries
// list has zero results: the human-readable summary of the filters in
// effect plus up to two "clear" actions. All hrefs are built Go-side with
// the same space/token carry-over contract the quick views use.
type entriesEmpty struct {
	SummaryText   string       // "（スペース: 個人スペース / 種別: trap）" or ""
	ClearHref     template.URL // keeps space+token, drops other filters; "" hides it
	ClearLabel    string       // "絞り込みを解除" / "スペース内で絞り込みを解除"
	AllSpacesHref template.URL // drops space too (/entries{?token}); "" hides it
}

// buildEntriesEmpty names the filters in effect and offers clear actions.
// The space name is resolved ONLY through the viewer's already-visible
// SpaceOptions (or a GetSpace on filter.SpaceID, which reaches this code
// solely after RequireVisibleSpace has passed in entriesList) — so it can
// never surface a space the viewer cannot already see. It reads output;
// it introduces no lookup on an unvalidated space id.
func (h *Handler) buildEntriesEmpty(ctx context.Context, pc pageCtx, f store.EntryFilter) entriesEmpty {
	var e entriesEmpty
	hasSpace := f.SpaceID != ""
	hasOther := f.Type != "" || f.Status != "" || f.ProjectID != "" ||
		f.Tag != "" || f.Query != "" || f.IncludeSuperseded

	// Summary: labelled `ラベル: 値` clauses joined by " / ", in this order.
	var parts []string
	if hasSpace {
		if name := h.spaceDisplayName(ctx, pc, f.SpaceID); name != "" {
			parts = append(parts, "スペース: "+name)
		}
	}
	if f.Type != "" {
		parts = append(parts, "種別: "+f.Type)
	}
	if f.Status != "" {
		parts = append(parts, "状態: "+f.Status)
	}
	if f.ProjectID != "" {
		parts = append(parts, "プロジェクト: "+f.ProjectID)
	}
	if f.Tag != "" {
		parts = append(parts, "タグ: "+f.Tag)
	}
	if f.Query != "" {
		parts = append(parts, "検索: "+f.Query)
	}
	if f.IncludeSuperseded {
		parts = append(parts, "SUPERSEDED を含む")
	}
	if len(parts) > 0 {
		e.SummaryText = "（" + strings.Join(parts, " / ") + "）"
	}

	// Clear action: keep the space (users usually want to stay in it and
	// just widen the type) and token, drop every other refinement.
	if hasOther {
		q := url.Values{}
		if hasSpace {
			q.Set("space", f.SpaceID)
		}
		if pc.Token != "" {
			q.Set("token", pc.Token)
		}
		e.ClearHref = entriesHref(q)
		if hasSpace {
			e.ClearLabel = "スペース内で絞り込みを解除"
		} else {
			e.ClearLabel = "絞り込みを解除"
		}
	}
	// Escape hatch for an empty personal/other space: drop the space too.
	if hasSpace {
		q := url.Values{}
		if pc.Token != "" {
			q.Set("token", pc.Token)
		}
		e.AllSpacesHref = entriesHref(q)
	}
	return e
}

// entriesHref builds "/entries" with an optional query string, matching the
// quick-view href shape (empty query → bare "/entries").
func entriesHref(q url.Values) template.URL {
	if len(q) == 0 {
		return template.URL("/entries")
	}
	return template.URL("/entries?" + q.Encode())
}

// spaceDisplayName resolves a VALIDATED, visible space id to its display
// name without any new visibility lookup: first the viewer's SpaceOptions
// (present when 2+ spaces are visible, zero extra queries), then — only
// when the select is hidden (a single visible space) — a best-effort
// GetSpace on the same already-RequireVisibleSpace-checked id used by the
// entry-page badge. On miss the name is simply omitted, never fabricated.
func (h *Handler) spaceDisplayName(ctx context.Context, pc pageCtx, id string) string {
	for _, opt := range pc.SpaceOptions {
		if opt.ID == id {
			return opt.Label
		}
	}
	if len(pc.SpaceOptions) == 0 {
		if sp, err := h.Store.GetSpace(ctx, id); err == nil {
			return spaceLabel(sp, pc.Me)
		}
	}
	return ""
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
