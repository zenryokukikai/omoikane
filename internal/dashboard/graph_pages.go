package dashboard

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// ----------------------------------------------------------------------
// Structure-browsing pages: hierarchy browse (/browse), the grouped
// index (/index), review queue, incident clusters and situations.
// ----------------------------------------------------------------------

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
