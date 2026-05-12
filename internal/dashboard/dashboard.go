// Package dashboard serves the minimal Phase 1 read-only Web UI described in
// docs/design.md §11. The pages are intentionally read-only — the audit role
// is "let humans verify what agents wrote". Editing is via the JSON API or CLI.
package dashboard

import (
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kojira/omoikane/internal/auth"
	"github.com/kojira/omoikane/internal/store"
)

//go:embed templates/*.html
var templatesFS embed.FS

type Handler struct {
	Store *store.Store
	Open  bool
	pages map[string]*template.Template
}

// New parses one *template.Template *per page* — html/template has no
// per-file scoping, so a single ParseFS over all pages would let the last
// `{{define "content"}}` win for every page.
func New(s *store.Store, open bool) (*Handler, error) {
	return newFromFS(s, open, templatesFS)
}

// newFromFS is the testable form of New. Tests inject a broken fs.FS to
// exercise the error-return branch that the embedded templatesFS can never
// actually hit.
func newFromFS(s *store.Store, open bool, fsys fs.FS) (*Handler, error) {
	funcs := template.FuncMap{
		"trunc":     trunc,
		"urlq":      url.QueryEscape,
		"deref":     deref,
		"wikiLinks": wikiLinks,
	}
	pages := map[string]*template.Template{}
	for _, name := range []string{"home", "project", "entry", "entry_history", "search",
		"review_queue", "clusters", "cluster", "situations", "situation",
		"browse", "browse_node", "index"} {
		t, err := template.New(name).Funcs(funcs).ParseFS(fsys,
			"templates/layout.html",
			"templates/"+name+".html")
		if err != nil {
			return nil, err
		}
		pages[name] = t
	}
	return &Handler{Store: s, Open: open, pages: pages}, nil
}

func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.AllowQueryTokenForGET)
		if !h.Open {
			mw := &auth.Middleware{S: h.Store}
			r.Use(mw.Authenticate)
			r.Use(auth.RequireScope("read"))
		}
		r.Get("/", h.home)
		r.Get("/projects/{id}", h.project)
		r.Get("/entries/{id}", h.entry)
		r.Get("/entries/{id}/history", h.history)
		r.Get("/search", h.search)
		r.Get("/review-queue", h.reviewQueuePage)
		r.Get("/clusters", h.clustersPage)
		r.Get("/clusters/{id}", h.clusterPage)
		r.Get("/situations", h.situationsPage)
		r.Get("/situations/{id}", h.situationPage)
		r.Get("/browse", h.browsePage)
		r.Get("/browse/{id}", h.browseNodePage)
		r.Get("/index", h.indexPage)
		r.Get("/static/style.css", h.css)
	})
}

type pageCtx struct {
	Title    string
	Query    string
	AsOf     string
	Token    string
	Open     bool
	Projects []*store.Project
	Project  *store.Project
	Entries  []*store.Entry
	Entry    *store.Entry
	History  []*store.EntryHistory
	Results  []*store.SearchResult

	// Phase 3
	Signals          *store.EntrySignals
	Cases            []*store.UsageCase
	Relations        []*store.Relation
	ReviewQueue      []*store.ReviewQueueRow
	Clusters         []*store.IncidentCluster
	Cluster          *store.IncidentCluster
	ClusterMembers   []*store.IncidentClusterMember
	Situations       []*store.Situation
	Situation        *store.Situation
	SituationEntries []*store.SituationEntry

	// Phase 4
	Backlinks      []*store.Relation
	BrowseRoots    []*store.HierarchyNode
	BrowseNode     *store.HierarchyNode
	BrowseChildren []*store.HierarchyNode
	BrowseEntries  []*store.Entry
	IndexBuckets   []*store.IndexBucket
	GroupBy        string
}

func (h *Handler) renderCtx(r *http.Request) pageCtx {
	return pageCtx{
		Open:  h.Open,
		Token: r.URL.Query().Get("token"),
	}
}

func (h *Handler) home(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ps, err := h.Store.ListProjects(ctx)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	entries, _, err := h.Store.ListEntries(ctx, store.EntryFilter{Limit: 20})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc := h.renderCtx(r)
	pc.Title = "omoikane — home"
	pc.Projects = ps
	pc.Entries = entries
	h.render(w, "home", pc)
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
	entries, _, err := h.Store.ListEntries(r.Context(), store.EntryFilter{
		ProjectID: id, Limit: 200, IncludeSuperseded: true,
	})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc := h.renderCtx(r)
	pc.Title = "omoikane — " + p.Name
	pc.Project = p
	pc.Entries = entries
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
	h.render(w, "entry", pc)
}

func (h *Handler) browsePage(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.Store.ListHierarchyNodes(r.Context(), r.URL.Query().Get("project"), "")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc := h.renderCtx(r)
	pc.Title = "omoikane — browse"
	pc.BrowseRoots = nodes
	h.render(w, "browse", pc)
}

func (h *Handler) browseNodePage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	node, err := h.Store.GetHierarchyNode(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	children, err := h.Store.ListHierarchyNodes(r.Context(), node.ProjectID, id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	entries, err := h.Store.ListEntriesAtNode(r.Context(), id, 200)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc := h.renderCtx(r)
	pc.Title = "omoikane — " + node.Name
	pc.BrowseNode = node
	pc.BrowseChildren = children
	pc.BrowseEntries = entries
	h.render(w, "browse_node", pc)
}

func (h *Handler) indexPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	groupBy := q.Get("group_by")
	if groupBy == "" {
		groupBy = "tag"
	}
	var (
		buckets []*store.IndexBucket
		err     error
	)
	switch groupBy {
	case "recent":
		buckets, err = h.Store.IndexByRecent(r.Context(), q.Get("project"), 12)
	case "hierarchy":
		buckets, err = h.Store.IndexByHierarchy(r.Context(), q.Get("project"))
	default:
		groupBy = "tag"
		buckets, err = h.Store.IndexByTag(r.Context(), q.Get("project"), 50)
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc := h.renderCtx(r)
	pc.Title = "omoikane — index"
	pc.IndexBuckets = buckets
	pc.GroupBy = groupBy
	h.render(w, "index", pc)
}

func (h *Handler) reviewQueuePage(w http.ResponseWriter, r *http.Request) {
	rows, err := h.Store.ReviewQueue(r.Context(), 100)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc := h.renderCtx(r)
	pc.Title = "omoikane — review queue"
	pc.ReviewQueue = rows
	h.render(w, "review_queue", pc)
}

func (h *Handler) clustersPage(w http.ResponseWriter, r *http.Request) {
	cls, err := h.Store.ListClusters(r.Context(),
		r.URL.Query().Get("project"), r.URL.Query().Get("status"), 100)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc := h.renderCtx(r)
	pc.Title = "omoikane — clusters"
	pc.Clusters = cls
	h.render(w, "clusters", pc)
}

func (h *Handler) clusterPage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	c, err := h.Store.GetCluster(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	members, err := h.Store.ListClusterMembers(r.Context(), id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc := h.renderCtx(r)
	pc.Title = "omoikane — cluster " + id
	pc.Cluster = c
	pc.ClusterMembers = members
	h.render(w, "cluster", pc)
}

func (h *Handler) situationsPage(w http.ResponseWriter, r *http.Request) {
	sits, err := h.Store.ListSituations(r.Context(), r.URL.Query().Get("project"), 200)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc := h.renderCtx(r)
	pc.Title = "omoikane — situations"
	pc.Situations = sits
	h.render(w, "situations", pc)
}

func (h *Handler) situationPage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sit, err := h.Store.GetSituation(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	entries, err := h.Store.ListSituationEntries(r.Context(), id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc := h.renderCtx(r)
	pc.Title = "omoikane — situation " + id
	pc.Situation = sit
	pc.SituationEntries = entries
	h.render(w, "situation", pc)
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

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	pc := h.renderCtx(r)
	pc.Title = "omoikane — search"
	pc.Query = q
	if q != "" {
		res, _, err := h.Store.SearchFTS(r.Context(), prepareFTSQuery(q), store.EntryFilter{
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

func (h *Handler) css(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write([]byte(stylesheet))
}

func (h *Handler) render(w http.ResponseWriter, page string, data any) {
	tpl, ok := h.pages[page]
	if !ok {
		http.Error(w, "no such page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tpl.ExecuteTemplate(w, "layout", data); err != nil {
		_, _ = w.Write([]byte("<pre>template error: " + template.HTMLEscapeString(err.Error()) + "</pre>"))
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// deref unwraps a *float64 for template printf use. Returns 0 for nil.
func deref(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// wikiLinkRE matches [[X-XXXX]] and [[X-XXXX|alt text]] forms. The ID
// must start with one of the entry-type prefixes (T|D|X|L|I|M|F|E or H
// for hierarchy / SIT for situations / CL for clusters) followed by `-`
// and base32-ish alphanumerics.
var wikiLinkRE = regexp.MustCompile(`\[\[((?:T|D|X|L|I|M|F|E|H|SIT|CL|CASE|SM)-[A-Za-z0-9]+)(?:\|([^\]]+))?\]\]`)

// wikiLinks renders `[[T-XXXX]]` references inside plain text fields as
// HTML anchors to the corresponding entry page. Tokens that don't match
// the entry-ID shape are left untouched. The output is HTML-escaped
// first so the function is XSS-safe when fed user content; this means
// the caller's template should pipe the result through `{{...}}` as
// `template.HTML` to surface the links.
func wikiLinks(text, token string) template.HTML {
	escaped := template.HTMLEscapeString(text)
	out := wikiLinkRE.ReplaceAllStringFunc(escaped, func(match string) string {
		groups := wikiLinkRE.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		id := groups[1]
		label := id
		if len(groups) >= 3 && groups[2] != "" {
			label = groups[2]
		}
		href := wikiHref(id, token)
		return `<a href="` + href + `" class="wiki">` + template.HTMLEscapeString(label) + `</a>`
	})
	return template.HTML(out)
}

// wikiHref routes an ID prefix to the right dashboard page. Entry IDs
// (T/D/X/L/I/M/F/E) go to `/entries/{id}`; H- to `/browse/{id}`; SIT- to
// `/situations/{id}`; CL- to `/clusters/{id}`. Anything else falls back
// to the entry page since unknown prefixes most likely came from a
// freshly-added entry type.
func wikiHref(id, token string) string {
	prefix := id
	if i := strings.IndexByte(id, '-'); i > 0 {
		prefix = id[:i]
	}
	var base string
	switch prefix {
	case "H":
		base = "/browse/" + id
	case "SIT":
		base = "/situations/" + id
	case "CL":
		base = "/clusters/" + id
	default:
		base = "/entries/" + id
	}
	if token != "" {
		base += "?token=" + url.QueryEscape(token)
	}
	return base
}

// prepareFTSQuery wraps each token in double quotes and a prefix marker so a
// user-friendly "mask training" becomes the safe FTS5 expression `"mask"* "training"*`.
//
// strings.FieldsFunc never emits empty tokens (it collapses runs of
// separators), so we don't bother filtering them out.
func prepareFTSQuery(q string) string {
	fields := strings.FieldsFunc(q, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', ',', ';', '.', '(', ')', '[', ']', '{', '}',
			'"', '\'', '`', ':', '/', '\\', '!', '?', '=', '<', '>', '|':
			return true
		}
		return false
	})
	toks := make([]string, 0, len(fields))
	for _, f := range fields {
		toks = append(toks, `"`+strings.ReplaceAll(f, `"`, `""`)+`"*`)
	}
	return strings.Join(toks, " ")
}
