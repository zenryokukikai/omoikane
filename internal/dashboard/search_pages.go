package dashboard

import (
	"errors"
	"html/template"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// ----------------------------------------------------------------------
// Search-and-discovery pages: full-text /search, the UseCase detail
// page (/use_cases/{ref}) and the reverse-lookup browse (/lookup).
// ----------------------------------------------------------------------

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	pc := h.renderCtx(r)
	pc.Title = "omoikane — search"
	pc.Query = q
	if q != "" {
		res, _, _, err := h.Store.SearchFTS(r.Context(), q, store.EntryFilter{
			ProjectID: r.URL.Query().Get("project"),
			Limit:     50,
		})
		if err != nil && !errors.Is(err, store.ErrInvalidInput) {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		pc.Results = res
	}
	h.render(w, "search", pc)
}

// snippetHTML renders a store search snippet as highlighted HTML.
//
// The order here is the whole point: the snippet is cut from entry text,
// so it is HTML-escaped FIRST and only then are the store's « » markers
// turned into <mark>. Swapping the two steps would let an entry body
// containing literal markup inject tags into the results page.
func snippetHTML(snippet string) template.HTML {
	escaped := template.HTMLEscapeString(snippet)
	escaped = strings.ReplaceAll(escaped, store.SnippetOpen, "<mark>")
	escaped = strings.ReplaceAll(escaped, store.SnippetClose, "</mark>")
	return template.HTML(escaped)
}

// useCasePage shows one UseCase and a paginated list of its linked entries.
func (h *Handler) useCasePage(w http.ResponseWriter, r *http.Request) {
	ref := chi.URLParam(r, "ref")
	var (
		uc  *store.UseCase
		err error
	)
	if strings.HasPrefix(ref, "U-") {
		uc, err = h.Store.GetUseCase(r.Context(), ref)
	} else {
		uc, err = h.Store.GetUseCaseBySlug(r.Context(), ref)
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	const pageSize = 30
	page := pageParam(r)
	entries, total, eErr := h.Store.ListUseCaseEntries(r.Context(), uc.ID, pageSize, (page-1)*pageSize)
	if eErr != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc := h.renderCtx(r)
	pc.Title = "omoikane — " + uc.NameJA + " / " + uc.NameEN
	pc.UseCase = uc
	pc.Entries = entries
	pc.Pagination = buildPagination(r, total, page, pageSize)

	// Cross-entry synthesis (the common insight across this category).
	if syn, synErr := h.Store.UseCaseSynthesis(r.Context(), uc.ID); synErr == nil {
		pc.UseCaseSynthesis = syn
	}
	// Tree context: breadcrumb parent + drilldown children.
	if uc.ParentID != "" {
		if p, pErr := h.Store.GetUseCase(r.Context(), uc.ParentID); pErr == nil {
			pc.UseCaseParent = p
		}
	}
	if kids, _, kErr := h.Store.ListUseCases(r.Context(),
		store.UseCaseFilter{ParentID: uc.ID}, 200, 0); kErr == nil && len(kids) > 0 {
		pc.UseCaseChildren = kids
	}

	// Summary middle layer: for each linked entry, fetch its cataloger
	// summary (if any) so the use-case page can show a 1-paragraph blurb
	// per entry without the user having to click through to the entry.
	pc.EntrySummaries = make(map[string]*store.Entry, len(entries))
	for _, e := range entries {
		if sum, sErr := h.Store.EntrySummary(r.Context(), e.ID); sErr == nil && sum != nil {
			pc.EntrySummaries[e.ID] = sum
		}
	}
	h.render(w, "use_case", pc)
}

// lookupRow is one reverse-lookup hit, enriched with the entry's title/type
// for display (LookupHit itself only carries the id + matched phrase).
type lookupRow struct {
	EntryID string
	Title   string
	Type    string
	Phrase  string // the symptom/trigger phrase that matched
	Source  string // "rule" | "fts"
}

// lookupPage is the UseCase-first browse / search view (design.md §23.15.4).
// Default mode: list use cases (most-recently-updated first, paginated). When
// the user types a query, name_ja/name_en LIKE matching filters the list.
// The legacy symptom/trigger modes are still reachable via ?mode=symptom|trigger
// for back-compat.
func (h *Handler) lookupPage(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	mode := r.URL.Query().Get("mode")
	if mode != "symptom" && mode != "trigger" {
		mode = "use_case" // new default
	}
	domain := strings.TrimSpace(r.URL.Query().Get("domain"))
	project := strings.TrimSpace(r.URL.Query().Get("project"))

	pc := h.renderCtx(r)
	pc.Title = "omoikane — lookup"
	pc.Query = q
	pc.LookupMode = mode
	pc.LookupDomain = domain

	switch mode {
	case "use_case":
		const pageSize = 30
		page := pageParam(r)
		// Tree-aware default: with no query/domain filter, show only the
		// top level (parent_id IS NULL) so growth stays browsable. The
		// user drills down by clicking a META row. When a query is set
		// we flatten and search across all levels.
		filter := store.UseCaseFilter{
			ProjectID: project, Domain: domain, Query: q,
		}
		if q == "" {
			filter.Level = "top"
		}
		list, total, err := h.Store.ListUseCases(r.Context(), filter, pageSize, (page-1)*pageSize)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		pc.UseCaseList = list
		pc.Pagination = buildPagination(r, total, page, pageSize)

	case "symptom", "trigger":
		// Legacy modes: phrase → entries.
		if q != "" {
			var (
				hits []*store.LookupHit
				err  error
			)
			if mode == "trigger" {
				hits, err = h.Store.LookupByTrigger(r.Context(), q, domain, 25)
			} else {
				hits, err = h.Store.LookupBySymptom(r.Context(), q, 25)
			}
			if err != nil && !errors.Is(err, store.ErrInvalidInput) {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			for _, hit := range hits {
				row := lookupRow{EntryID: hit.EntryID, Phrase: hit.Phrase, Source: hit.Source}
				if e, e2 := h.Store.GetEntry(r.Context(), hit.EntryID); e2 == nil && e != nil {
					row.Title = e.Title
					row.Type = e.Type
				}
				pc.LookupRows = append(pc.LookupRows, row)
			}
		}
	}
	h.render(w, "lookup", pc)
}
