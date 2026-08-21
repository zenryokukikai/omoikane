package api

// Admin space/group management (issue #60, Phase 1 slice 5).
//
// Thin JSON wrappers over the slice-1 store CRUD (spaces.go). All
// routes sit behind RequireScope("admin") — spaces, groups and grants
// are org-wide metadata managed only by admins; visibility itself is
// still resolved exclusively through store.VisibleSpaces. Personal
// spaces appear in the listing but are not grantable (the store
// rejects space_acl rows on kind=personal with ErrInvalidInput).

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// spaceWithACL is the /v1/admin/spaces list item: the space plus its
// grants, so one call paints the whole management table.
type spaceWithACL struct {
	store.Space
	ACL []*store.SpaceACL `json:"acl"`
}

// groupWithMembers is the /v1/admin/groups list item.
type groupWithMembers struct {
	store.Group
	Members []string `json:"members"`
}

// adminListSpaces → GET /v1/admin/spaces
func (h *Handler) adminListSpaces(w http.ResponseWriter, r *http.Request) {
	spaces, err := h.Store.ListSpaces(httpCtx(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := make([]spaceWithACL, 0, len(spaces))
	for _, sp := range spaces {
		row := spaceWithACL{Space: *sp, ACL: []*store.SpaceACL{}}
		if sp.Kind != store.SpaceKindPersonal { // personal ACL is implicit
			acl, err := h.Store.ListSpaceACL(httpCtx(r), sp.ID)
			if err != nil {
				writeStoreError(w, err)
				return
			}
			row.ACL = acl
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"spaces": out})
}

// adminCreateSpace → POST /v1/admin/spaces {"name": "..."}
func (h *Handler) adminCreateSpace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, "invalid JSON body", nil)
		return
	}
	sp, err := h.Store.CreateSpace(httpCtx(r), req.Name)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sp)
}

// adminListGroups → GET /v1/admin/groups
func (h *Handler) adminListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := h.Store.ListGroups(httpCtx(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := make([]groupWithMembers, 0, len(groups))
	for _, g := range groups {
		members, err := h.Store.ListGroupMembers(httpCtx(r), g.ID)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		out = append(out, groupWithMembers{Group: *g, Members: members})
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out})
}

// adminCreateGroup → POST /v1/admin/groups {"name": "..."}
func (h *Handler) adminCreateGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, "invalid JSON body", nil)
		return
	}
	g, err := h.Store.CreateGroup(httpCtx(r), req.Name)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

// adminAddGroupMember → PUT /v1/admin/groups/{id}/members/{userID}
func (h *Handler) adminAddGroupMember(w http.ResponseWriter, r *http.Request) {
	err := h.Store.AddGroupMember(httpCtx(r),
		chi.URLParam(r, "id"), chi.URLParam(r, "userID"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// adminRemoveGroupMember → DELETE /v1/admin/groups/{id}/members/{userID}
func (h *Handler) adminRemoveGroupMember(w http.ResponseWriter, r *http.Request) {
	err := h.Store.RemoveGroupMember(httpCtx(r),
		chi.URLParam(r, "id"), chi.URLParam(r, "userID"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// adminSetSpaceACL → PUT /v1/admin/spaces/{id}/acl/{groupID} {"role": "admin|member"}
func (h *Handler) adminSetSpaceACL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, "invalid JSON body", nil)
		return
	}
	err := h.Store.SetSpaceACL(httpCtx(r),
		chi.URLParam(r, "id"), chi.URLParam(r, "groupID"), req.Role)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// adminRemoveSpaceACL → DELETE /v1/admin/spaces/{id}/acl/{groupID}
func (h *Handler) adminRemoveSpaceACL(w http.ResponseWriter, r *http.Request) {
	err := h.Store.RemoveSpaceACL(httpCtx(r),
		chi.URLParam(r, "id"), chi.URLParam(r, "groupID"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}
