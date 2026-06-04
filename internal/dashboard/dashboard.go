// Package dashboard serves the minimal Phase 1 read-only Web UI described in
// docs/design.md §11. The pages are intentionally read-only — the audit role
// is "let humans verify what agents wrote". Editing is via the JSON API or CLI.
package dashboard

import (
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kojira/omoikane/dist/samples"
	"github.com/kojira/omoikane/internal/auth"
	"github.com/kojira/omoikane/internal/store"
	"github.com/kojira/omoikane/internal/version"
)

//go:embed templates/*.html templates/*.tmpl
var templatesFS embed.FS

type Handler struct {
	Store *store.Store
	Open  bool
	pages map[string]*template.Template

	// Phase A: whether the server has Google OAuth wired up. Drives the
	// /login page's button visibility.
	GoogleEnabled bool
}

// sessionCookieName must match api.sessionCookieName. Kept duplicated
// (string constant) rather than imported to avoid a circular dep.
const sessionCookieName = "kb_session"

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
		"trunc":       trunc,
		"urlq":        url.QueryEscape,
		"deref":       deref,
		"wikiLinks":   wikiLinks,
		"chatContent": chatContent,
		// markdown + wiki + mentions + attachment unfurl in one shot;
		// preferred renderer for entry bodies and chat messages.
		// Captures `s` so attachment refs can be resolved at render
		// time without threading the store through every template
		// invocation.
		"renderContent": func(text, token string) template.HTML {
			return renderContent(text, token, s)
		},
		// appVersion lets layout.html's footer show the running version
		// on every page without threading it through each handler's data.
		"appVersion": version.String,
		// assetVersion busts the browser CSS cache on each deploy. The
		// stylesheet is served with a 4h max-age, so without a changing
		// URL a redeploy leaves stale CSS in browsers. Tie the URL to the
		// build (git SHA, or app semver for un-stamped local builds).
		"assetVersion": func() string {
			if version.Build != "" && version.Build != "dev" {
				return version.Build
			}
			return version.App
		},
		// isJournal reports whether an entry is a summarizer daily journal,
		// so the entry page can render it as a clean reading sheet.
		"isJournal": func(e *store.Entry) bool { return metaKind(e) == "daily_journal" },
		// journalDate pulls metadata.journal_date off a daily-journal entry
		// (falls back to the created date) for the journal index.
		"journalDate": func(e *store.Entry) string {
			if len(e.Metadata) > 0 {
				var m struct {
					JournalDate string `json:"journal_date"`
				}
				if json.Unmarshal(e.Metadata, &m) == nil && m.JournalDate != "" {
					return m.JournalDate
				}
			}
			return e.CreatedAt.Format("2006-01-02")
		},
		// journalPosted shows when a journal was actually written, in JST,
		// so a reader can tell "this morning's journal" from an older one —
		// the journal *covers* the previous day but is *posted* the next
		// morning, and the index date alone hides that distinction.
		"journalPosted": func(e *store.Entry) string {
			jst := time.FixedZone("JST", 9*60*60)
			return e.CreatedAt.In(jst).Format("2006-01-02 15:04")
		},
	}
	pages := map[string]*template.Template{}
	for _, name := range []string{"home", "journal", "project", "entry", "entry_history", "search",
		"review_queue", "clusters", "cluster", "situations", "situation",
		"browse", "browse_node", "index", "lookup", "entries",
		"chat_threads", "chat_thread", "login", "claim", "agents", "profile",
		"members", "member_claim"} {
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
	// Public: /login is the unauthenticated landing for browsers without
	// a token yet. The OAuth callback lives under /v1/auth/google/... in
	// the API package.
	r.Get("/login", h.loginPage)

	// Public: /skill.md is the single, canonical Agent-Skills-standard
	// SKILL.md for omoikane. One URL, one source of truth — agents
	// fetch this once and have everything they need (auth, API
	// contract, chat ping-pong protocol, error handling, security
	// notes). Previously there was also /skills/omoikane/SKILL.md
	// serving the same content; that was redundant and is now gone.
	r.Get("/skill.md", h.serveAgentSkillMD)

	// Public sample helper scripts (no auth — these are read-only
	// sample shell scripts that an agent reading skill.md may want
	// to fetch from the same origin to avoid a second trust boundary
	// at GitHub. They're MIT-licensed sample copy. The on-disk
	// source remains under dist/samples/agent-helpers/ for browsers
	// who prefer to read them in the repo.
	r.Get("/samples/agent-helpers/{name}", h.serveSampleHelper)
	r.Get("/claim/{code}", h.claimPage)
	// Public landing for a member invitation. The invitee opens this
	// before having an account — auth would break the flow. The
	// actual redemption happens in the OAuth callback by email match.
	r.Get("/members/claim/{code}", h.memberClaimPage)


	r.Group(func(r chi.Router) {
		// Cookie → bearer must run before query-token promotion so a
		// freshly-issued session cookie takes precedence over a stale
		// ?token= bookmark.
		r.Use(auth.SessionCookieToBearer(sessionCookieName))
		r.Use(auth.AllowQueryTokenForGET)
		if !h.Open {
			mw := &auth.Middleware{S: h.Store}
			// Order: browserAuthRedirect outer, Authenticate inner.
			// When Authenticate writes a 401, the redirect wrapper
			// catches it and turns it into /login?next=… for browsers.
			// API clients (no text/html in Accept) still see JSON 401.
			r.Use(browserAuthRedirect)
			r.Use(mw.Authenticate)
			r.Use(auth.RequireScope("read"))
		}
		r.Get("/", h.home)
		r.Get("/journal", h.journalList)
		r.Get("/projects/{id}", h.project)
		r.Get("/entries", h.entriesList)
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
	r.Get("/lookup", h.lookupPage)
		r.Get("/chat", h.chatThreadsPage)
		r.Get("/chat/{id}", h.chatThreadPage)
		r.Get("/agents", h.agentsPage)
		r.Get("/u/{id}", h.profilePage)
		r.Get("/members", h.membersPage)
		r.Get("/static/style.css", h.css)
	})
	// Write surfaces for the dashboard (chat + agents). Form submissions
	// can't set Authorization headers, so we accept the token via
	// `?token=` AND via the session cookie (see auth.AllowQueryTokenAny).
	r.Group(func(r chi.Router) {
		r.Use(auth.SessionCookieToBearer(sessionCookieName))
		r.Use(auth.AllowQueryTokenAny)
		if !h.Open {
			mw := &auth.Middleware{S: h.Store}
			r.Use(mw.Authenticate)
			r.Use(auth.RequireScope("write"))
		}
		r.Post("/chat/new", h.chatThreadCreate)
		r.Post("/chat/{id}/post", h.chatThreadPostMessage)
		r.Post("/chat/{id}/close", h.chatThreadClose)
		r.Post("/agents/issue", h.agentsIssue)
		r.Post("/u/{id}/edit", h.profileEdit)
		r.Post("/members/invite", h.membersInvite)
		r.Post("/members/{id}/role", h.membersRoleChange)
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

	// Reverse-lookup page (/lookup) — symptom/trigger → entries
	LookupMode   string // "symptom" | "trigger"
	LookupDomain string
	LookupRows   []lookupRow
	IndexedList  []*store.IndexedEntrySummary // browse list when no query

	// Entry page — this entry's reverse-lookup index (symptom/trigger → here)
	EntrySymptoms []string
	EntryTriggers []store.IndexedTrigger

	// Phase 5 — chat
	ChatThreads      []*store.ChatThread
	ChatThread       *store.ChatThread
	ChatMessages     []*store.ChatMessage
	ChatStatusFilter string // "OPEN" default, "CLOSED", or "" (= all). Used by chat_threads.html to render the filter UI.

	// Phase A — login page
	GoogleEnabled bool
	Next          string
	LoginError    string

	// Claim page
	ClaimCode      string
	ClaimAgent     *store.User
	ClaimExpiresAt *time.Time
	ClaimedAt      *time.Time
	ClaimedByMe    bool
	ClaimError     string

	// Agents page
	Me              *store.User
	NewInviteCode   string
	AgentsPageError string
	Invitations     []*store.AgentInvitation
	MyAgents        []*store.User
	BaseURL         string

	// Profile page (/u/{id}) — public view of any user or agent
	Profile         *store.User
	ProfileParent   *store.User    // human owner if Profile is an agent
	ProfileChildren []*store.User  // agents parented to this profile (if it's a human)
	ProfileError    string
	IsSelfProfile   bool           // viewer is the same as profile target → show edit form

	// Members page (/members) — admin-only directory + invite management
	MembersList       []*store.User
	MemberInvitations []*store.MemberInvitation
	MembersPageError  string
	NewMemberCode     string                  // populated when ?new=<code> is set after issue
	ClaimInvitation   *store.MemberInvitation // for /members/claim/{code}
	ClaimInviter      *store.User

	// Entries list page (/entries) — filterable index over all entries.
	// EntriesTotal lets the template show "showing N of M total" without
	// rendering the whole corpus. EntriesFilter echoes the active filter
	// back so the form preserves user input across navigation.
	EntriesTotal  int
	EntriesFilter store.EntryFilter
	Pagination    *pagination
}

func (h *Handler) renderCtx(r *http.Request) pageCtx {
	pc := pageCtx{
		Open:  h.Open,
		Token: r.URL.Query().Get("token"),
	}
	// Populate Me from the request auth context so every page can show
	// the signed-in user in the header. Falls through silently when
	// the request isn't authenticated.
	if tok := auth.FromContext(r.Context()); tok != nil && tok.UserID != "" {
		if u, err := h.Store.GetUser(r.Context(), tok.UserID); err == nil {
			pc.Me = u
		}
	}
	return pc
}

func (h *Handler) home(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ps, err := h.Store.ListProjects(ctx)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	const pageSize = 20
	page := pageParam(r)
	entries, total, err := h.Store.ListEntries(ctx, store.EntryFilter{Limit: pageSize, Offset: (page - 1) * pageSize})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc := h.renderCtx(r)
	pc.Title = "omoikane — home"
	pc.Projects = ps
	pc.Entries = entries
	pc.Pagination = buildPagination(r, total, page, pageSize)
	h.render(w, "home", pc)
}

// entriesList renders a filterable list of entries. Filters accepted via
// query params (each optional):
//   ?type=<lesson|trap|decision|design|incident|librarian_meta|external_finding>
//   ?project=<id>
//   ?status=<DRAFT|ACTIVE|SUPERSEDED|ARCHIVED|...>
//   ?tag=<tag>
//   ?q=<full-text>
//   ?limit=<N> (default 100, max 500)
//   ?include_superseded=true
//
// Useful URLs for the librarian flow:
//   /entries?type=librarian_meta              — every librarian's output
//   /entries?type=librarian_meta&tag=cataloger — cataloger's output only
//   /entries?type=trap                        — every trap in the corpus
//   /entries?project=lipsync                  — lipsync's full corpus
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
	pc.Pagination = buildPagination(r, total, page, limit)
	h.render(w, "entries", pc)
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

// lookupRow is one reverse-lookup hit, enriched with the entry's title/type
// for display (LookupHit itself only carries the id + matched phrase).
type lookupRow struct {
	EntryID string
	Title   string
	Type    string
	Phrase  string // the symptom/trigger phrase that matched
	Source  string // "rule" | "fts"
}

// lookupPage is the human view of the reverse-lookup index the indexer fills:
// type a symptom or a trigger phrase, get the entries it leads to. This is the
// dashboard counterpart of /v1/lookup/by-symptom|trigger.
func (h *Handler) lookupPage(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	mode := r.URL.Query().Get("mode")
	if mode != "trigger" {
		mode = "symptom"
	}
	domain := strings.TrimSpace(r.URL.Query().Get("domain"))

	pc := h.renderCtx(r)
	pc.Title = "omoikane — lookup"
	pc.Query = q
	pc.LookupMode = mode
	pc.LookupDomain = domain

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
	} else {
		// No query → browse the indexed articles, most-recently-indexed first.
		const pageSize = 30
		page := pageParam(r)
		list, total, lErr := h.Store.ListIndexedEntries(r.Context(),
			r.URL.Query().Get("project"), pageSize, (page-1)*pageSize)
		if lErr == nil {
			pc.IndexedList = list
			pc.Pagination = buildPagination(r, total, page, pageSize)
		}
	}
	h.render(w, "lookup", pc)
}

// ----------------------------------------------------------------------
// Phase A — login page (no auth required)
// ----------------------------------------------------------------------

func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	pc := h.renderCtx(r)
	pc.Title = "omoikane — sign in"
	pc.GoogleEnabled = h.GoogleEnabled
	if next := r.URL.Query().Get("next"); next != "" && strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") {
		pc.Next = next
	}
	if e := r.URL.Query().Get("error"); e != "" {
		pc.LoginError = e
	}
	h.render(w, "login", pc)
}

// claimPage renders the "do you want to claim this agent?" view. The
// page itself is unauthenticated so the human sees what they're being
// asked to adopt; the actual claim is performed by a JS-less form post
// to /v1/agents/claim/{code}, which requires the session cookie.
func (h *Handler) claimPage(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	c, err := h.Store.GetClaimByCode(r.Context(), code)
	pc := h.renderCtx(r)
	pc.Title = "omoikane — claim agent"
	pc.ClaimCode = code
	if err != nil {
		pc.ClaimError = "claim code not found or expired"
		h.render(w, "claim", pc)
		return
	}
	pc.ClaimAgent = c.AgentUser
	pc.ClaimExpiresAt = &c.ExpiresAt
	pc.ClaimedAt = c.ClaimedAt
	if c.ClaimedAt != nil {
		// We don't know the current viewer's user_id without an auth
		// check, but the API endpoint enforces the "different human"
		// guard separately. For display purposes we just flag it.
		pc.ClaimedByMe = false
	}
	h.render(w, "claim", pc)
}

// ----------------------------------------------------------------------
// Phase 5 — librarian chat room (read + write from the dashboard)
// ----------------------------------------------------------------------

func (h *Handler) chatThreadsPage(w http.ResponseWriter, r *http.Request) {
	// Default view hides closed / archived threads — they're typically
	// post-mortem state (the live phase has ended). To browse the
	// archive, append `?status=CLOSED` or `?status=all` explicitly.
	//
	// This is the "soft-delete" surface for chat: closing a thread
	// with a summary like "superseded by entry T-XXX" makes it
	// disappear from the default /chat listing while staying
	// reachable by direct URL and via the all-status query.
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "OPEN"
	}
	if status == "all" {
		status = "" // store treats empty as no filter
	}
	threads, err := h.Store.ListThreads(r.Context(), status, 100)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc := h.renderCtx(r)
	pc.Title = "omoikane — chat"
	pc.ChatThreads = threads
	pc.ChatStatusFilter = status
	h.render(w, "chat_threads", pc)
}

func (h *Handler) chatThreadPage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	threads, _ := h.Store.ListThreads(r.Context(), "", 500)
	var thread *store.ChatThread
	for _, t := range threads {
		if t.ThreadID == id {
			thread = t
			break
		}
	}
	if thread == nil {
		http.NotFound(w, r)
		return
	}
	msgs, err := h.Store.ListChatMessages(r.Context(), id, 500)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc := h.renderCtx(r)
	pc.Title = "omoikane — " + firstNonEmpty(thread.Title, thread.ThreadID)
	pc.ChatThread = thread
	pc.ChatMessages = msgs
	h.render(w, "chat_thread", pc)
}

// chatThreadCreate accepts a form POST and redirects to the new thread.
// Fields: title, intent.
func (h *Handler) chatThreadCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id, err := h.Store.OpenThread(r.Context(), &store.ChatThread{
		Title:  strings.TrimSpace(r.FormValue("title")),
		Intent: strings.TrimSpace(r.FormValue("intent")),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dest := "/chat/" + id
	if tok := r.URL.Query().Get("token"); tok != "" {
		dest += "?token=" + url.QueryEscape(tok)
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// chatThreadPostMessage accepts a form POST from the thread page.
// Fields: author_role (defaults "human"), content, intent.
func (h *Handler) chatThreadPostMessage(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id := chi.URLParam(r, "id")
	role := strings.TrimSpace(r.FormValue("author_role"))
	if role == "" {
		role = "human"
	}
	content := strings.TrimSpace(r.FormValue("content"))
	if content == "" {
		http.Error(w, "content required", http.StatusBadRequest)
		return
	}
	// Same authority story as the API path: server fills author_user_id
	// from the session, never the form. The browser can't lie about
	// who's posting.
	var authorUserID string
	if tok := auth.FromContext(r.Context()); tok != nil {
		authorUserID = tok.UserID
	}
	_, err := h.Store.PostChatMessage(r.Context(), &store.ChatMessage{
		ThreadID:     id,
		AuthorRole:   role,
		AuthorUserID: authorUserID,
		Intent:       strings.TrimSpace(r.FormValue("intent")),
		Content:      content,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	dest := "/chat/" + id
	if tok := r.URL.Query().Get("token"); tok != "" {
		dest += "?token=" + url.QueryEscape(tok)
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func (h *Handler) chatThreadClose(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.Store.CloseThread(r.Context(), id, strings.TrimSpace(r.FormValue("summary"))); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dest := "/chat/" + id
	if tok := r.URL.Query().Get("token"); tok != "" {
		dest += "?token=" + url.QueryEscape(tok)
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func (h *Handler) css(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write([]byte(stylesheet))
}

// serveSampleHelper serves one of the sample helper shell scripts
// embedded in the binary. Public (no auth required) — the scripts
// are MIT-licensed sample code, not credentials.
//
// Only files ending in .sh under agent-helpers/ are served; anything
// else returns 404. The chi {name} param is path-cleaned to defend
// against `../` traversal attempts.
func (h *Handler) serveSampleHelper(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	// Defence in depth: chi already prevents `..` in segment values,
	// but path.Clean + suffix check is a cheap second layer.
	if name != path.Base(name) || !strings.HasSuffix(name, ".sh") {
		http.NotFound(w, r)
		return
	}
	data, err := samples.AgentHelpers.ReadFile("agent-helpers/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(data)
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

// mentionRenderRE mirrors store.mentionRE — kept duplicated rather than
// imported so the dashboard package doesn't form a circular dep with
// store's regex internals. Roles must stay in sync.
var mentionRenderRE = regexp.MustCompile(
	`(^|[^A-Za-z0-9_])@(coordinator|cataloger|curator|detective|conservator|scout|summarizer|judge|human)\b`)

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

// chatContent renders a chat-message body: HTML-escapes it, links
// `[[T-XXXX]]` references, and decorates `@<role>` mentions with a
// per-role span. Returns template.HTML so html/template won't
// re-escape the output.
func chatContent(text, token string) template.HTML {
	// Reuse wikiLinks for escaping + wiki-link substitution.
	out := string(wikiLinks(text, token))
	// Now decorate @mentions. We operate on already-escaped HTML; the
	// regex only matches role-shaped tokens at word boundaries, so it
	// will not accidentally split a wikilink's `<a class="wiki" …>`
	// (which contains no '@' at all).
	out = mentionRenderRE.ReplaceAllStringFunc(out, func(match string) string {
		groups := mentionRenderRE.FindStringSubmatch(match)
		if len(groups) < 3 {
			return match
		}
		prefix, role := groups[1], groups[2]
		return prefix + `<span class="mention mention-` + role + `">@` + role + `</span>`
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
