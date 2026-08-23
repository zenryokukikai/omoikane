package dashboard

import (
	"context"
	"net/http"
	"time"

	"github.com/zenryokukikai/omoikane/internal/auth"
	"github.com/zenryokukikai/omoikane/internal/store"
)

// ----------------------------------------------------------------------
// pageCtx — the single template data struct shared by every page — plus
// its per-request construction (renderCtx) and the display-language
// plumbing (resolveLang / langSwitch / persistLangCookie).
//
// pageCtx stays ONE struct on purpose: templates rely on flat field
// access ({{.Entries}}, {{.Me}}, …) across every page, so splitting it
// into per-page structs would break the shared layout/partial templates.
// ----------------------------------------------------------------------

type pageCtx struct {
	Title string
	Query string
	AsOf  string
	Token string
	Open  bool
	Lang  string // "ja" | "en" — display language for bilingual content

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
	UseCaseList  []*store.UseCaseSummary // new browse list — UseCase-first

	// Entry page — author user (resolved from created_by)
	EntryAuthor *store.User

	// Entry page — this entry's reverse-lookup index (symptom/trigger → here)
	EntrySymptoms    []string
	EntryTriggers    []store.IndexedTrigger
	EntryUseCases    []*store.EntryUseCase   // UseCases this entry belongs to
	Comments         []*store.EntryComment   // review/discussion thread on the entry
	UseCase          *store.UseCase          // for /use_cases/{ref} detail page
	UseCaseParent    *store.UseCase          // for breadcrumb on detail page
	UseCaseChildren  []*store.UseCaseSummary // for tree drilldown on detail page
	EntrySummaries   map[string]*store.Entry // entry_id → cataloger summary (if any)
	UseCaseSynthesis *store.Entry            // cross-entry common insight (if any)

	// Phase 5 — chat
	ChatThreads      []*store.ChatThread
	ChatThread       *store.ChatThread
	ChatMessages     []*store.ChatMessage
	ChatHasEarlier   bool                 // /talk: older messages exist above the rendered window (#45)
	TalkThreads      []*store.ChatThread  // /talk: the signed-in user's responder-chat threads
	TalkAgent        *store.User          // /talk: the default answering agent (avatar + display name)
	TalkLibrarian    *store.UserLibrarian // /talk: thread owner's personal librarian; nil → default responder (#73)
	NavLibrarian     *store.UserLibrarian // header nav: viewer's own librarian (name+icon); nil → default responder
	Bookmarked       bool                 // entry page: current user starred this entry
	LatestJournal    *store.Entry         // home: newest daily journal (teaser)
	JournalTeaser    string               // home: its first lines, markdown stripped
	Bookmarks        []*store.Bookmark    // /bookmarks: the current user's shortlist
	Directives       []*store.Directive   // /directives: operator watch-topics for scout
	ChatStatusFilter string               // "OPEN" default, "CLOSED", or "" (= all). Used by chat_threads.html to render the filter UI.

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
	ProfileParent   *store.User   // human owner if Profile is an agent
	ProfileChildren []*store.User // agents parented to this profile (if it's a human)
	ProfileError    string
	IsSelfProfile   bool // viewer is the same as profile target → show edit form

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
