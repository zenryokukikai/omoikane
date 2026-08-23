package dashboard

import (
	"encoding/json"
	"net/http"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// ----------------------------------------------------------------------
// Front page (/) and the daily-journal reading index (/journal), plus
// the metaKind helper both use to spot summarizer journals.
// ----------------------------------------------------------------------

func (h *Handler) home(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ps, err := h.Store.ListProjects(ctx)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	const pageSize = 8 // front page shows a short "what's new" list; the full browser is /entries
	page := pageParam(r)
	entries, total, err := h.Store.ListEntries(ctx, store.EntryFilter{Limit: pageSize, Offset: (page - 1) * pageSize})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Top-level categories (UseCase tree roots) for the home overview.
	// One row per META + standalone leaf. Drill into /use_cases/{slug}.
	// Errors are non-fatal — home shouldn't 500 because UseCases failed.
	cats, _, cErr := h.Store.ListUseCases(ctx, store.UseCaseFilter{Level: "top"}, 50, 0)
	pc := h.renderCtx(r)
	pc.Title = "omoikane — home"
	// Today's journal teaser — the single most valuable thing on the
	// front page for a human (issue #21). Non-fatal on error.
	if js, _, jErr := h.Store.ListEntries(ctx, store.EntryFilter{
		Type: "librarian_meta", Status: "ACTIVE", Limit: 30,
	}); jErr == nil {
		for _, j := range js {
			if metaKind(j) == "daily_journal" {
				pc.LatestJournal = j
				pc.JournalTeaser = teaser(j.Body, 180)
				break
			}
		}
	}
	pc.Projects = ps
	pc.Entries = entries
	if cErr == nil {
		pc.UseCaseList = cats
	}
	pc.Pagination = buildPagination(r, total, page, pageSize)
	h.render(w, "home", pc)
}

// journalList shows the daily journals (summarizer's morning digests),
// newest first — the human-facing reading index. Journals are
// librarian_meta entries with metadata.kind=daily_journal, posted ACTIVE.
func (h *Handler) journalList(w http.ResponseWriter, r *http.Request) {
	entries, _, err := h.Store.ListEntries(r.Context(), store.EntryFilter{
		Type: "librarian_meta", Status: "ACTIVE", Limit: 200,
	})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	journals := make([]*store.Entry, 0, len(entries))
	for _, e := range entries {
		if metaKind(e) == "daily_journal" {
			journals = append(journals, e)
		}
	}
	// Paginate the filtered slice (daily journals are sparse — one per
	// day — so an in-memory window is fine).
	const pageSize = 30
	total := len(journals)
	page := pageParam(r)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	pc := h.renderCtx(r)
	pc.Title = "omoikane — journal"
	pc.Entries = journals[start:end]
	pc.Pagination = buildPagination(r, total, page, pageSize)
	h.render(w, "journal", pc)
}

// metaKind extracts metadata.kind from an entry's raw JSON metadata,
// returning "" when absent or unparseable.
func metaKind(e *store.Entry) string {
	if len(e.Metadata) == 0 {
		return ""
	}
	var m struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(e.Metadata, &m); err != nil {
		return ""
	}
	return m.Kind
}
