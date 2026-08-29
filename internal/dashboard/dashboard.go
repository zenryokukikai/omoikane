// Package dashboard serves the minimal Phase 1 read-only Web UI described in
// docs/design.md §11. The pages are intentionally read-only — the audit role
// is "let humans verify what agents wrote". Editing is via the JSON API or CLI.
package dashboard

import (
	"embed"
	"html/template"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/auth"
	"github.com/zenryokukikai/omoikane/internal/store"
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

	// Gate registers the librarian on the external gate admin plane
	// (issue #104 G2). nil = gate registration off (GATE_ADMIN_URL
	// unset); the save flow then skips the gate step entirely.
	Gate GateRegistrar
}

// sessionCookieName must match api.sessionCookieName. Kept duplicated
// (string constant) rather than imported to avoid a circular dep.
const sessionCookieName = "kb_session"

func (h *Handler) Mount(r chi.Router) {
	// /login is the landing for browsers without a token yet. It renders
	// the sign-in form for anonymous visitors, but it must ALSO recognise
	// an already-signed-in visitor and bounce them to their destination
	// (issue #129). So it runs an OPTIONAL authentication chain: the
	// session cookie / ?token= is promoted and looked up, but a missing or
	// invalid credential renders the form rather than 401-ing. The OAuth
	// callback itself lives under /v1/auth/google/... in the API package.
	r.Group(func(r chi.Router) {
		r.Use(auth.SessionCookieToBearer(sessionCookieName))
		r.Use(auth.AllowQueryTokenForGET)
		if !h.Open {
			mw := &auth.Middleware{S: h.Store}
			r.Use(mw.OptionalAuthenticate)
		}
		r.Get("/login", h.loginPage)
	})

	// Public: the stylesheet is a compiled-in Go const with no
	// request-derived or sensitive data, so it must be reachable WITHOUT a
	// session — otherwise the pre-login pages (/login, /claim/{code})
	// render as unstyled text because their <link> 401s (issue #129).
	r.Get("/static/style.css", h.css)

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
