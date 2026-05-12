// Package api implements the HTTP REST surface described in
// docs/design.md §5. Handlers depend only on store, enrich, secrets, and
// auth — never on the database directly.
package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/kojira/omoikane/internal/auth"
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

		r.Group(func(r chi.Router) {
			r.Use(authMW.Authenticate)

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
