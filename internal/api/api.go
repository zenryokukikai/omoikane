// Package api implements the HTTP REST surface described in
// docs/design.md §5. Handlers depend only on store, enrich, secrets, and
// auth — never on the database directly.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kojira/omoikane/internal/auth"
	"github.com/kojira/omoikane/internal/auth/oauth"
	"github.com/kojira/omoikane/internal/config"
	"github.com/kojira/omoikane/internal/enrich"
	"github.com/kojira/omoikane/internal/store"
)

type Handler struct {
	Store       *store.Store
	Enricher    enrich.Enricher
	SecretsMode config.SecretsMode
	Logger      *slog.Logger
	StartedAt   string
	BuildInfo   string

	// Phase A auth — nil disables OAuth login (the rest of the API
	// keeps working with admin-issued Bearer tokens).
	OAuthGoogle      oauth.Provider
	OAuthRedirectBase string // for canonical-host enforcement (e.g. "http://localhost:8095")
	AuthAllowDomains []string
	AuthAllowEmails  []string
	HTTPSEnabled     bool
	SessionTTL       time.Duration

	// Agent registration policy
	RegisterOpen bool // KB_REGISTER_OPEN=1 disables invite-code requirement
}

// Mount registers the Phase 1 surface on r under /v1. Process-wide middleware
// (request id, recoverer, access log, body limit, audit) is installed by the
// caller on r; we only install auth-related middleware on sub-routes.
func (h *Handler) Mount(r chi.Router) {
	if h.Logger == nil {
		h.Logger = slog.Default()
	}
	authMW := &auth.Middleware{S: h.Store}

	r.Route("/v1", func(r chi.Router) {
		r.Get("/health", h.health)

		// Phase A — OAuth flow (public; no auth required to initiate)
		r.Get("/auth/google/login", h.authGoogleLogin)
		r.Get("/auth/google/callback", h.authGoogleCallback)
		r.Post("/auth/logout", h.authLogout)

		// Agent self-onboarding (public; rate-limited at the middleware
		// layer by KB_REQUEST_BODY_MAX + access-log review)
		r.Post("/agents/register", h.agentRegister)
		r.Get("/agents/claim/{code}", h.agentClaimGet)

		r.Group(func(r chi.Router) {
			// Promote browser session cookies to Bearer tokens so the
			// existing token-based auth middleware sees them.
			r.Use(auth.SessionCookieToBearer(sessionCookieName))
			r.Use(authMW.Authenticate)
			r.Get("/auth/me", h.authMe)
			r.Post("/agents/claim/{code}", h.agentClaimPost)
			// Invite issuance — any authenticated human can issue invites
			// for their own agents.
			r.Post("/admin/agent-invites", h.issueAgentInvite)
			r.Get("/admin/agent-invites", h.listAgentInvites)

			r.With(auth.RequireScope("read")).Get("/projects", h.listProjects)
			r.With(auth.RequireScope("read")).Get("/projects/{id}", h.getProject)
			r.With(auth.RequireScope("write")).Post("/projects", h.createProject)

			r.With(auth.RequireScope("read")).Get("/entries", h.listEntries)
			r.With(auth.RequireScope("read")).Get("/entries/{id}", h.getEntry)
			r.With(auth.RequireScope("read")).Get("/entries/{id}/history", h.entryHistory)
			r.With(auth.RequireScope("write")).Post("/entries", h.createEntry)
			r.With(auth.RequireScope("write")).Patch("/entries/{id}", h.updateEntry)
			r.With(auth.RequireScope("write")).Delete("/entries/{id}", h.deleteEntry)

			r.With(auth.RequireScope("read")).Post("/search", h.search)

			// Lookups (Phase 2 reverse-index endpoints).
			r.With(auth.RequireScope("read")).Post("/lookup/by-trigger", h.lookupByTrigger)
			r.With(auth.RequireScope("read")).Post("/lookup/by-symptom", h.lookupBySymptom)
			r.With(auth.RequireScope("read")).Post("/lookup/by-tags", h.lookupByTags)
			r.With(auth.RequireScope("read")).Post("/lookup/by-situation", h.lookupBySituation)

			// Phase 3 — usage cases (feedback loop)
			r.With(auth.RequireScope("write")).Post("/cases", h.createCase)
			r.With(auth.RequireScope("write")).Patch("/cases/{id}", h.patchCase)
			r.With(auth.RequireScope("read")).Get("/cases/{id}", h.getCase)
			r.With(auth.RequireScope("read")).Get("/entries/{id}/cases", h.listEntryCases)
			r.With(auth.RequireScope("read")).Get("/entries/{id}/signals", h.entrySignals)
			r.With(auth.RequireScope("read")).Get("/entries/{id}/relations", h.listEntryRelations)
			r.With(auth.RequireScope("read")).Get("/review-queue", h.reviewQueue)

			// Phase 3 — relations
			r.With(auth.RequireScope("write")).Post("/relations", h.createRelation)
			r.With(auth.RequireScope("write")).Delete("/relations", h.deleteRelation)

			// Phase 3 — situations
			r.With(auth.RequireScope("read")).Get("/situations", h.listSituations)
			r.With(auth.RequireScope("read")).Get("/situations/{id}", h.getSituation)
			r.With(auth.RequireScope("write")).Post("/situations", h.createSituation)
			r.With(auth.RequireScope("write")).Post("/situations/{id}/entries", h.addSituationEntry)
			r.With(auth.RequireScope("write")).Delete("/situations/{id}/entries/{entryID}", h.removeSituationEntry)
			r.With(auth.RequireScope("write")).Delete("/situations/{id}", h.deleteSituation)

			// Phase 3 — incident clusters
			r.With(auth.RequireScope("read")).Get("/clusters", h.listClusters)
			r.With(auth.RequireScope("read")).Get("/clusters/{id}", h.getCluster)
			r.With(auth.RequireScope("write")).Post("/clusters", h.createCluster)
			r.With(auth.RequireScope("write")).Post("/clusters/{id}/members", h.addClusterMember)
			r.With(auth.RequireScope("write")).Delete("/clusters/{id}/members/{entryID}", h.removeClusterMember)
			r.With(auth.RequireScope("write")).Post("/clusters/{id}/promote", h.promoteCluster)
			r.With(auth.RequireScope("write")).Post("/clusters/{id}/dismiss", h.dismissCluster)
			r.With(auth.RequireScope("admin")).Post("/clusters/rebuild", h.rebuildClusters)

			// Phase 4 — hierarchy + index + reflect
			r.With(auth.RequireScope("read")).Get("/browse", h.browseRoots)
			r.With(auth.RequireScope("read")).Get("/browse/{id}", h.browseNode)
			r.With(auth.RequireScope("read")).Get("/browse/{id}/entries", h.browseNodeEntries)
			r.With(auth.RequireScope("write")).Post("/browse", h.createHierarchyNode)
			r.With(auth.RequireScope("write")).Post("/browse/{id}/entries", h.attachEntryToNode)
			r.With(auth.RequireScope("write")).Delete("/browse/{id}/entries/{entryID}", h.detachEntryFromNode)
			r.With(auth.RequireScope("write")).Delete("/browse/{id}", h.deleteHierarchyNode)
			r.With(auth.RequireScope("read")).Get("/index", h.indexPage)
			r.With(auth.RequireScope("read")).Post("/reflect", h.reflect)

			// Phase 5 — librarian community
			r.Route("/librarian", func(r chi.Router) {
				r.With(auth.RequireScope("admin")).Post("/instances", h.librarianRegister)
				r.With(auth.RequireScope("read")).Get("/instances", h.librarianList)
				r.With(auth.RequireScope("write")).Patch("/instances/{id}", h.librarianSetStatus)
				r.With(auth.RequireScope("write")).Post("/instances/{id}/heartbeat", h.librarianHeartbeat)

				r.With(auth.RequireScope("read")).Get("/threads", h.chatListThreads)
				r.With(auth.RequireScope("write")).Post("/threads", h.chatOpenThread)
				r.With(auth.RequireScope("write")).Post("/threads/{id}/close", h.chatCloseThread)
				r.With(auth.RequireScope("read")).Get("/threads/{id}/messages", h.chatList)
				r.With(auth.RequireScope("write")).Post("/chat", h.chatPost)

				r.With(auth.RequireScope("read")).Get("/tasks", h.taskList)
				r.With(auth.RequireScope("write")).Post("/tasks", h.taskEnqueue)
				r.With(auth.RequireScope("write")).Post("/tasks/{id}/claim", h.taskClaim)
				r.With(auth.RequireScope("write")).Post("/tasks/{id}/complete", h.taskComplete)

				r.With(auth.RequireScope("read")).Get("/quartet", h.quartetList)
				r.With(auth.RequireScope("write")).Post("/quartet", h.quartetCreate)
				r.With(auth.RequireScope("write")).Post("/quartet/{id}/decide", h.quartetDecide)

				r.With(auth.RequireScope("read")).Get("/findings", h.findingList)
				r.With(auth.RequireScope("write")).Post("/findings", h.findingRecord)
				r.With(auth.RequireScope("write")).Post("/findings/{id}/correlate", h.findingCorrelate)

				r.With(auth.RequireScope("admin")).Post("/emergency_stop", h.librarianEmergencyStop)

				// Phase 6 — coordinator anomaly scan + quartet proposal
				r.With(auth.RequireScope("read")).Get("/coordinator/triage", h.coordinatorTriage)
				r.With(auth.RequireScope("write")).Post("/coordinator/propose_quartet", h.coordinatorProposeQuartet)
			})

			// Phase 6 — tier listing
			r.With(auth.RequireScope("read")).Get("/tiers", h.tierList)

			// Phase 6+ — open-work loop (agent-first; see entry X-SQATAB)
			r.With(auth.RequireScope("read")).Get("/open_work", h.listOpenWork)
			r.With(auth.RequireScope("write")).Post("/entries/{id}/claim", h.claimOpenWork)
			r.With(auth.RequireScope("write")).Post("/entries/{id}/release", h.releaseOpenWork)
			r.With(auth.RequireScope("write")).Post("/entries/{id}/mark_merged", h.mergeOpenWork)

			// Phase 7 — admin: backup, dead-pool, LLM usage, coverage
			r.Route("/admin", func(r chi.Router) {
				r.Use(auth.RequireScope("admin"))
				r.Post("/backup", h.adminBackup)
				r.Get("/backups", h.adminBackupList)
				r.Post("/dead_pool/run", h.adminDeadPool)
				r.Get("/health/llm_usage", h.adminLLMUsage)
				r.Get("/health/coverage", h.adminCoverage)
			})
		})
	})
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"started_at": h.StartedAt,
		"build":      h.BuildInfo,
	})
}

func httpCtx(r *http.Request) context.Context { return r.Context() }
