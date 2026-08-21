// Package dashboard serves the minimal Phase 1 read-only Web UI described in
// docs/design.md §11. The pages are intentionally read-only — the audit role
// is "let humans verify what agents wrote". Editing is via the JSON API or CLI.
package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/dist/samples"
	"github.com/zenryokukikai/omoikane/internal/auth"
	"github.com/zenryokukikai/omoikane/internal/store"
	"github.com/zenryokukikai/omoikane/internal/version"
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

	// Librarian provisions personal librarian agents onto the opencrab
	// runtime (issue #73). nil = feature disabled: /my/librarian answers
	// 404 and the header link is hidden. Set by server wiring when
	// OPENCRAB_URL is configured.
	Librarian LibrarianProvisioner
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
		// replyAuthor names the author of the comment a reply points
		// at, for the ↪ indicator inside threads (reply-to-reply).
		"replyAuthor": func(all []*store.EntryComment, id string) string {
			for _, c := range all {
				if c.ID == id {
					return c.AuthorName
				}
			}
			return ""
		},
		"urlq":        url.QueryEscape,
		"minus":       func(a, b int) int { return a - b },
		"langSwitch":  langSwitch,
		// navURL builds a safe template.URL for an internal nav link with
		// optional token + lang query params. Sidesteps html/template's URL-
		// context analyzer (which rejects dynamic values in {{if}}-built
		// query strings as ambiguous). path must be a static prefix; the
		// caller threads token and lang through here.
		"navURL": func(path, token, lang string) template.URL {
			qs := url.Values{}
			if token != "" {
				qs.Set("token", token)
			}
			if lang != "" {
				qs.Set("lang", lang)
			}
			if len(qs) == 0 {
				return template.URL(path)
			}
			return template.URL(path + "?" + qs.Encode())
		},
		"deref":       deref,
		"wikiLinks":   wikiLinks,
		"chatContent": chatContent,
		// markdown + wiki + mentions + attachment unfurl in one shot;
		// preferred renderer for entry bodies and chat messages.
		// Captures `s` so attachment refs can be resolved at render
		// time without threading the store through every template
		// invocation. Takes the request context (templates pass $.Ctx)
		// so the store lookups inside (EntriesExist / GetAttachment)
		// run under the viewer's space visibility — a background
		// context here would be an unrestricted bypass (issue #60
		// slice 5).
		"renderContent": func(ctx context.Context, text, token string) template.HTML {
			return renderContent(ctx, text, token, s)
		},
		// ltime renders a timestamp as a <time> element that layout.html's
		// localizer script rewrites into the viewer's timezone (#43). The
		// UTC-formatted text is the no-JS fallback. p picks the shape:
		// date / dt / t / dts / md (see localizer for the exact formats).
		"ltime": ltime,
		"talkAgentName": talkAgentName,
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
		"browse", "browse_node", "index", "lookup", "use_case", "entries", "entry_new",
		"chat_threads", "chat_thread", "talk", "bookmarks", "directives", "login", "claim", "agents", "profile",
		"members", "member_claim", "admin_spaces", "my_librarian"} {
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
		r.Use(persistLangCookie)
		if !h.Open {
			mw := &auth.Middleware{S: h.Store}
			// Order: browserAuthRedirect outer, Authenticate inner.
			// When Authenticate writes a 401, the redirect wrapper
			// catches it and turns it into /login?next=… for browsers.
			// API clients (no text/html in Accept) still see JSON 401.
			r.Use(browserAuthRedirect)
			r.Use(mw.Authenticate)
			r.Use(auth.RequireScope("read"))
			// Space visibility (issue #60 slice 5): every page's store
			// calls run under the viewer's resolved view.
			r.Use(h.withVisibleSpaces)
		}
		r.Get("/", h.home)
		r.Get("/journal", h.journalList)
		r.Get("/projects/{id}", h.project)
		r.Get("/entries", h.entriesList)
		// Static /entries/new wins over the {id} wildcard in chi's trie.
		// The page only RENDERS the form — the submission goes to the
		// existing POST /v1/entries API (session cookie), so the
		// dashboard gains no second write path for entries (issue #71).
		r.Get("/entries/new", h.entryNewPage)
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
	r.Get("/use_cases/{ref}", h.useCasePage)
		r.Get("/chat", h.chatThreadsPage)
		r.Get("/chat/{id}", h.chatThreadPage)
		r.Get("/bookmarks", h.bookmarksPage)
		r.Get("/directives", h.directivesPage)
		r.Get("/talk", h.talkPage)
		r.Get("/talk/{id}", h.talkPage)
		r.Get("/agents", h.agentsPage)
		r.Get("/my/librarian", h.myLibrarianPage)
		r.Get("/librarian-icon/{userID}", h.librarianIconImage)
		r.Get("/u/{id}", h.profilePage)
		r.Get("/members", h.membersPage)
		r.Get("/admin/spaces", h.adminSpacesPage)
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
			r.Use(h.withVisibleSpaces)
		}
		r.Post("/chat/new", h.chatThreadCreate)
		r.Post("/chat/{id}/post", h.chatThreadPostMessage)
		r.Post("/chat/{id}/close", h.chatThreadClose)
		r.Post("/agents/issue", h.agentsIssue)
		r.Post("/my/librarian", h.myLibrarianSave)
		r.Post("/u/{id}/edit", h.profileEdit)
		r.Post("/members/invite", h.membersInvite)
		r.Post("/members/{id}/role", h.membersRoleChange)
		// Admin space/group management forms (admin scope enforced in
		// the handlers so non-admins get a readable 403, not a 401).
		r.Post("/admin/spaces/create", h.adminSpaceCreate)
		r.Post("/admin/groups/create", h.adminGroupCreate)
		r.Post("/admin/groups/{id}/members/add", h.adminGroupMemberAdd)
		r.Post("/admin/groups/{id}/members/remove", h.adminGroupMemberRemove)
		r.Post("/admin/spaces/{id}/acl", h.adminSpaceACLSet)
		r.Post("/admin/spaces/{id}/acl/remove", h.adminSpaceACLRemove)
	})
}

type pageCtx struct {
	Title    string
	Query    string
	AsOf     string
	Token    string
	Open     bool
	Lang     string // "ja" | "en" — display language for bilingual content

	// Ctx is the request context, carrying the viewer's space
	// visibility. Templates thread it into store-touching funcs
	// (renderContent) so render-time lookups stay inside the view.
	Ctx context.Context

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
	IndexedList  []*store.IndexedEntrySummary // browse list when no query (legacy)
	UseCaseList  []*store.UseCaseSummary      // new browse list — UseCase-first

	// Entry page — author user (resolved from created_by)
	EntryAuthor   *store.User

	// Entry page — this entry's reverse-lookup index (symptom/trigger → here)
	EntrySymptoms []string
	EntryTriggers []store.IndexedTrigger
	EntryUseCases []*store.EntryUseCase // UseCases this entry belongs to
	Comments      []*store.EntryComment  // review/discussion thread on the entry
	UseCase       *store.UseCase         // for /use_cases/{ref} detail page
	UseCaseParent *store.UseCase         // for breadcrumb on detail page
	UseCaseChildren []*store.UseCaseSummary // for tree drilldown on detail page
	EntrySummaries  map[string]*store.Entry // entry_id → cataloger summary (if any)
	UseCaseSynthesis *store.Entry          // cross-entry common insight (if any)

	// Phase 5 — chat
	ChatThreads      []*store.ChatThread
	ChatThread       *store.ChatThread
	ChatMessages     []*store.ChatMessage
	ChatHasEarlier   bool                // /talk: older messages exist above the rendered window (#45)
	TalkThreads      []*store.ChatThread // /talk: the signed-in user's responder-chat threads
	TalkAgent        *store.User         // /talk: the default answering agent (avatar + display name)
	TalkLibrarian    *store.UserLibrarian // /talk: thread owner's personal librarian; nil → default responder (#73)
	NavLibrarian     *store.UserLibrarian // header nav: viewer's own librarian (name+icon); nil → default responder
	Bookmarked       bool                // entry page: current user starred this entry
	LatestJournal    *store.Entry        // home: newest daily journal (teaser)
	JournalTeaser    string              // home: its first lines, markdown stripped
	Bookmarks        []*store.Bookmark   // /bookmarks: the current user's shortlist
	Directives       []*store.Directive  // /directives: operator watch-topics for scout
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

	// Admin spaces page (/admin/spaces) — nil on every other page.
	Admin *adminSpacesData

	// Personal librarian page (/my/librarian, issue #73).
	// LibrarianEnabled drives the header link on every page; the rest
	// only feed the settings page itself.
	LibrarianEnabled bool                 // OPENCRAB_URL configured → feature on
	MyLibrarian      *store.UserLibrarian // current row, nil if not set up yet
	LibrarianName    string               // form echo (current or submitted)
	LibrarianPersona string               // form echo
	LibrarianIcon    string               // form echo (text icon)
	LibrarianSaved   bool                 // success banner after PRG
	LibrarianError   string               // error banner

	// Entries list page (/entries) — filterable index over all entries.
	// EntriesTotal lets the template show "showing N of M total" without
	// rendering the whole corpus. EntriesFilter echoes the active filter
	// back so the form preserves user input across navigation.
	EntriesTotal  int
	EntriesFilter store.EntryFilter
	Pagination    *pagination

	// SpaceOptions feeds the /entries space select: the viewer's visible
	// spaces with display labels, nil when a select would be noise
	// (fewer than two visible spaces — nothing to switch between).
	SpaceOptions []spaceOption
	// EntrySpaceName labels the entry page's space badge ("" for
	// internal-space entries: internal is the unmarked default).
	EntrySpaceName string
}

// spaceOption is one choice in the /entries space select.
type spaceOption struct {
	ID    string
	Label string
}

func (h *Handler) renderCtx(r *http.Request) pageCtx {
	pc := pageCtx{
		Open:             h.Open,
		Token:            r.URL.Query().Get("token"),
		Lang:             resolveLang(r),
		Ctx:              r.Context(),
		LibrarianEnabled: h.Librarian != nil,
	}
	// Populate Me from the request auth context so every page can show
	// the signed-in user in the header. Falls through silently when
	// the request isn't authenticated.
	if tok := auth.FromContext(r.Context()); tok != nil && tok.UserID != "" {
		if u, err := h.Store.GetUser(r.Context(), tok.UserID); err == nil {
			pc.Me = u
		}
	}
	// Nav label: the viewer's own librarian answers THEIR /talk, so the
	// header entry point is named after it (#73 UX: "where do I chat with
	// my librarian?" — same place, now labelled so). Single PK lookup;
	// default responder name when the feature is off or unset.
	if pc.Me != nil && h.Librarian != nil {
		if ul, err := h.Store.GetUserLibrarian(r.Context(), pc.Me.ID); err == nil && ul.Status == "active" && ul.Name != "" {
			pc.NavLibrarian = ul
		}
	}
	return pc
}

// resolveLang picks the display language from ?lang= (which also persists
// into a cookie so subsequent navigation keeps the choice), then the cookie,
// then defaults to Japanese.
func resolveLang(r *http.Request) string {
	if q := r.URL.Query().Get("lang"); q == "ja" || q == "en" {
		return q
	}
	if c, err := r.Cookie("lang"); err == nil && (c.Value == "ja" || c.Value == "en") {
		return c.Value
	}
	return "ja"
}

// langSwitch returns the language to switch TO from the current one.
func langSwitch(cur string) string {
	if cur == "ja" {
		return "en"
	}
	return "ja"
}

// persistLangCookie middleware: when a request arrives with ?lang=ja|en,
// write a cookie so subsequent navigation keeps the choice without having
// to re-thread ?lang= through every link. Idempotent — only writes when
// the query overrides.
func persistLangCookie(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query().Get("lang"); q == "ja" || q == "en" {
			http.SetCookie(w, &http.Cookie{
				Name:     "lang",
				Value:    q,
				Path:     "/",
				MaxAge:   60 * 60 * 24 * 365, // a year
				HttpOnly: false,              // readable by client JS if useful
				SameSite: http.SameSiteLaxMode,
			})
		}
		next.ServeHTTP(w, r)
	})
}

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

// entriesList renders a filterable list of entries. Filters accepted via
// query params (each optional):
//   ?type=<lesson|trap|decision|design|incident|note|librarian_meta|external_finding>
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
		res, _, err := h.Store.SearchFTS(r.Context(), q, store.EntryFilter{
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

// directivesPage manages operator watch-topics for the scout (issue
// #31) — visible to everyone (the team's shared attention list).
func (h *Handler) directivesPage(w http.ResponseWriter, r *http.Request) {
	pc := h.renderCtx(r)
	pc.Title = "omoikane — 巡回指示"
	ds, err := h.Store.ListDirectives(r.Context(), "scout", false)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc.Directives = ds
	h.render(w, "directives", pc)
}

// bookmarksPage lists the signed-in user's starred entries.
func (h *Handler) bookmarksPage(w http.ResponseWriter, r *http.Request) {
	pc := h.renderCtx(r)
	pc.Title = "omoikane — ブックマーク"
	if pc.Me != nil {
		bms, err := h.Store.ListBookmarks(r.Context(), pc.Me.ID, 200)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		pc.Bookmarks = bms
	}
	h.render(w, "bookmarks", pc)
}

// talkPage is the per-user responder chat: an Open-WebUI-style
// two-pane page over the existing chat_threads/librarian_chat machinery.
// Threads are the signed-in user's own (created_by) with intent
// "talk" (a neutral capability id — #54 scrubbed the legacy agent-named
// value); the responder agent answers via the same chat API.
func (h *Handler) talkPage(w http.ResponseWriter, r *http.Request) {
	pc := h.renderCtx(r)
	pc.Title = "omoikane — " + talkAgentName() + "に聞く"
	// The answering agent's own profile drives the header/bubble avatar,
	// so a re-uploaded portrait shows up here without a code change.
	// Matched by name (the chat author_role is "chronicler", but the
	// user is the one displayed).
	if users, err := h.Store.ListUsers(r.Context(), "agent", 200); err == nil {
		for _, u := range users {
			if u.Name == talkAgentName() {
				pc.TalkAgent = u
				break
			}
		}
	}
	if pc.Me == nil {
		// The whole page is per-user; render the signed-out shell and
		// let the template show the login prompt.
		h.render(w, "talk", pc)
		return
	}
	all, err := h.Store.ListThreads(r.Context(), "", pc.Me.ID, 200)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	for _, t := range all {
		if t.Intent == "talk" {
			pc.TalkThreads = append(pc.TalkThreads, t)
		}
	}
	if chi.URLParam(r, "id") == "" {
		// New-conversation view: the answering side is the viewer's own
		// personal librarian, if set (issue #73 slice B).
		h.resolveTalkLibrarian(r, &pc, pc.Me.ID)
		pc.Title = "omoikane — " + pc.TalkRespondentName() + "に聞く"
	}
	if id := chi.URLParam(r, "id"); id != "" {
		th, err := h.Store.GetThread(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		// Private history: only the owner (or the admin scope — the one
		// admin contract, never users.role) may read it.
		if th.CreatedBy != pc.Me.ID && !isAdmin(r) {
			http.NotFound(w, r)
			return
		}
		// The answering side of THIS thread is decided by its owner (an
		// admin viewing another user's thread sees that owner's
		// librarian — matching who actually answers there). Resolved
		// before the fragment path so live-appended bubbles carry the
		// same identity as the initial render.
		h.resolveTalkLibrarian(r, &pc, th.CreatedBy)
		// Fragment mode (#45): serve one rendered message window for the
		// virtualized list instead of the whole page. Auth above applies.
		if frag := r.URL.Query().Get("frag"); frag != "" {
			h.talkFragment(w, r, &pc, id, frag)
			return
		}
		// Initial window: the newest talkWindow messages. Fetching one
		// extra tells us whether an earlier page exists without a COUNT.
		msgs, err := h.Store.ListChatMessagesTail(r.Context(), id, talkWindow+1)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if len(msgs) > talkWindow {
			pc.ChatHasEarlier = true
			msgs = msgs[1:] // oldest-first slice: drop the probe row
		}
		pc.ChatThread = th
		pc.ChatMessages = msgs
		pc.Title = "omoikane — " + firstNonEmpty(th.Title, pc.TalkRespondentName()+"に聞く")
	}
	h.render(w, "talk", pc)
}

// resolveTalkLibrarian fills pc.TalkLibrarian with owner's ACTIVE
// personal librarian, if any. Left nil (→ default responder identity)
// on any miss — the same fail-open direction as the webhook-side
// routing, so what the page shows matches who actually answers.
func (h *Handler) resolveTalkLibrarian(r *http.Request, pc *pageCtx, owner string) {
	// Feature gate first: with the runtime unconfigured (OPENCRAB_URL
	// unset → h.Librarian nil) no librarian can answer, so none may
	// front the page either — a leftover user_librarians row must not
	// split identity ("shown: librarian, answering: default responder",
	// design §25.7). Same gate the webhook router applies via
	// TalkDispatch == nil.
	if h.Librarian == nil {
		return
	}
	if ul, err := h.Store.GetUserLibrarian(r.Context(), owner); err == nil && ul.Status == "active" {
		pc.TalkLibrarian = ul
	}
}

// TalkRespondentName is the display name of whoever answers in the
// current /talk view: the resolved personal librarian, else the default
// responder. Exported for the talk templates; value receiver because
// templates receive pageCtx by value.
// LibrarianIconURL builds the serving URL of a librarian's uploaded
// icon image, or "" when none is uploaded. Built here (not in the
// store) because query-token sessions have no cookie — the browser's
// <img> request must carry ?token= like every other dashboard link,
// and only the request context knows the token. ?v= busts browser
// caches on image replacement.
func (pc pageCtx) LibrarianIconURL(ul *store.UserLibrarian) string {
	if ul == nil || ul.IconMime == "" {
		return ""
	}
	u := "/librarian-icon/" + url.PathEscape(ul.UserID) + "?v=" + strconv.FormatInt(ul.IconVer, 10)
	if pc.Token != "" {
		u += "&token=" + url.QueryEscape(pc.Token)
	}
	return u
}

func (pc pageCtx) TalkRespondentName() string {
	if pc.TalkLibrarian != nil {
		return pc.TalkLibrarian.Name
	}
	return talkAgentName()
}

// talkWindow is the /talk message page size: the initial render and each
// upward infinite-scroll fetch (#45).
const talkWindow = 50

// talkFragment renders the `talk_frag` template — a bare run of message
// rows — for the virtualized /talk list. mode "before" pages upward from
// the cursor message; "since" returns everything newer (live append);
// "tail" returns the newest window with no cursor — the live-update
// recovery path for a thread rendered empty (#57-4).
func (h *Handler) talkFragment(w http.ResponseWriter, r *http.Request, pc *pageCtx, threadID, mode string) {
	if mode == "tail" {
		msgs, err := h.Store.ListChatMessagesTail(r.Context(), threadID, talkWindow+1)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if len(msgs) > talkWindow {
			pc.ChatHasEarlier = true
			msgs = msgs[1:]
		}
		pc.ChatMessages = msgs
		h.renderTalkFrag(w, pc)
		return
	}
	cur, err := h.Store.GetChatMessage(r.Context(), r.URL.Query().Get("cursor"))
	if err != nil || cur.ThreadID != threadID {
		http.Error(w, "unknown cursor", http.StatusBadRequest)
		return
	}
	switch mode {
	case "before":
		msgs, err := h.Store.ListChatMessagesBefore(r.Context(), threadID, cur.Timestamp, talkWindow+1)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if len(msgs) > talkWindow {
			pc.ChatHasEarlier = true
			msgs = msgs[1:]
		}
		pc.ChatMessages = msgs
	case "since":
		msgs, err := h.Store.ListChatMessagesSince(r.Context(), threadID, cur.Timestamp, 200)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		pc.ChatMessages = msgs
	default:
		http.Error(w, "frag must be before|since|tail", http.StatusBadRequest)
		return
	}
	h.renderTalkFrag(w, pc)
}

func (h *Handler) renderTalkFrag(w http.ResponseWriter, pc *pageCtx) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if err := h.pages["talk"].ExecuteTemplate(w, "talk_frag", pc); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

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
	threads, err := h.Store.ListThreads(r.Context(), status, "", 100)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// /chat is the shared librarian-coordination room. intent=talk
	// threads are personal conversations and live on /talk only
	// (issue #60 slice 4).
	shared := threads[:0]
	for _, t := range threads {
		if t.Intent != "talk" {
			shared = append(shared, t)
		}
	}
	pc := h.renderCtx(r)
	pc.Title = "omoikane — chat"
	pc.ChatThreads = shared
	pc.ChatStatusFilter = status
	h.render(w, "chat_threads", pc)
}

func (h *Handler) chatThreadPage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	threads, _ := h.Store.ListThreads(r.Context(), "", "", 500)
	var thread *store.ChatThread
	for _, t := range threads {
		if t.ThreadID == id {
			thread = t
			break
		}
	}
	// intent=talk threads are personal conversations: /talk/{id} (with
	// its owner check) is their only dashboard surface (issue #60
	// slice 4). Hidden == missing, no oracle.
	if thread == nil || thread.Intent == "talk" {
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

// requireSharedThread 404s unless the thread exists and is a shared
// (non-talk) one. The /chat write surface mirrors the /chat read pages:
// intent=talk threads are personal conversations that live on /talk
// only, so posting into or closing one through /chat is answered with
// the same 404 a missing thread gets (no existence oracle).
func (h *Handler) requireSharedThread(w http.ResponseWriter, r *http.Request, id string) bool {
	th, err := h.Store.GetThread(r.Context(), id)
	if err != nil || th.Intent == "talk" {
		http.NotFound(w, r)
		return false
	}
	return true
}

// chatThreadPostMessage accepts a form POST from the thread page.
// Fields: author_role (defaults "human"), content, intent.
func (h *Handler) chatThreadPostMessage(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id := chi.URLParam(r, "id")
	if !h.requireSharedThread(w, r, id) {
		return
	}
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
	if !h.requireSharedThread(w, r, id) {
		return
	}
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
	// Pages carry live-update JS that evolves with deploys; no-cache
	// makes every (re)load revalidate so stale scripts can't linger.
	w.Header().Set("Cache-Control", "no-cache")
	if err := tpl.ExecuteTemplate(w, "layout", data); err != nil {
		_, _ = w.Write([]byte("<pre>template error: " + template.HTMLEscapeString(err.Error()) + "</pre>"))
	}
}

// talkAgentName is the /talk responder's display name. The concrete
// persona name is deployment configuration (env), never code (#51).
func talkAgentName() string {
	if v := os.Getenv("KB_TALK_AGENT_NAME"); v != "" {
		return v
	}
	return "コンシェルジュ"
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ltime emits a <time> element the layout localizer rewrites into the
// viewer's timezone (#43). The text content is the UTC rendering — what
// a no-JS client (or a test) sees. Zero/nil times render as an empty
// string so optional fields don't show the epoch. Accepts time.Time or
// *time.Time — optional store fields are pointers and templates pass
// function arguments without auto-dereferencing.
func ltime(v any, p string) template.HTML {
	var t time.Time
	switch x := v.(type) {
	case time.Time:
		t = x
	case *time.Time:
		if x == nil {
			return ""
		}
		t = *x
	default:
		return ""
	}
	if t.IsZero() {
		return ""
	}
	layout := "2006-01-02 15:04"
	switch p {
	case "date":
		layout = "2006-01-02"
	case "t":
		layout = "15:04"
	case "ts":
		layout = "15:04:05"
	case "dts":
		layout = "2006-01-02 15:04:05"
	case "md":
		layout = "01-02 15:04"
	}
	u := t.UTC()
	return template.HTML(fmt.Sprintf("<time data-p=%q datetime=%q>%s</time>",
		p, u.Format(time.RFC3339), u.Format(layout)))
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

