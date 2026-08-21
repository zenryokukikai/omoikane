package dashboard

// /admin/spaces — space & group management (issue #60, Phase 1 slice 5).
//
// Server-rendered admin console over the slice-1 store CRUD: list
// spaces with their group grants, create restricted spaces, manage
// groups and their members, grant/revoke a group's access to a space.
// Personal spaces are listed (so the admin sees the full picture) but
// offer no grant controls — their ACL is implicit and the store
// rejects space_acl rows on kind=personal.
//
// Authorisation is the admin *scope* (the one admin contract, design
// v2) — never users.role. Non-admin viewers get a readable "admins
// only" banner like /members; the form POSTs answer 403.

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// adminSpacesData is everything the admin_spaces template renders.
type adminSpacesData struct {
	Forbidden bool   // viewer lacks the admin scope
	Error     string // last action's error (via ?err=), if any

	Spaces []adminSpaceRow
	Groups []adminGroupRow
	Users  []*store.User // member pickers (humans + agents)
}

type adminSpaceRow struct {
	Space  *store.Space
	Grants []adminGrantRow
}

type adminGrantRow struct {
	GroupID   string
	GroupName string
	Role      string // admin | member
}

type adminGroupRow struct {
	Group   *store.Group
	Members []string // user ids
}

// adminSpacesPage renders GET /admin/spaces.
func (h *Handler) adminSpacesPage(w http.ResponseWriter, r *http.Request) {
	pc := h.renderCtx(r)
	pc.Title = "omoikane — spaces admin"
	pc.Admin = &adminSpacesData{}
	if !isAdmin(r) {
		pc.Admin.Forbidden = true
		h.render(w, "admin_spaces", pc)
		return
	}
	// Surface the previous action's failure, if we were redirected with
	// one. The value is server-generated (our own redirects), but treat
	// it as untrusted text regardless — the template escapes it.
	pc.Admin.Error = r.URL.Query().Get("err")

	ctx := r.Context()
	spaces, err := h.Store.ListSpaces(ctx)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	groups, err := h.Store.ListGroups(ctx)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	groupName := make(map[string]string, len(groups))
	for _, g := range groups {
		groupName[g.ID] = g.Name
	}
	for _, sp := range spaces {
		row := adminSpaceRow{Space: sp}
		if sp.Kind != store.SpaceKindPersonal { // personal ACL is implicit
			acl, err := h.Store.ListSpaceACL(ctx, sp.ID)
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			for _, a := range acl {
				row.Grants = append(row.Grants, adminGrantRow{
					GroupID: a.GroupID, GroupName: groupName[a.GroupID], Role: a.Role,
				})
			}
		}
		pc.Admin.Spaces = append(pc.Admin.Spaces, row)
	}
	for _, g := range groups {
		members, err := h.Store.ListGroupMembers(ctx, g.ID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		pc.Admin.Groups = append(pc.Admin.Groups, adminGroupRow{Group: g, Members: members})
	}
	// Every user (human + agent) for the add-member picker.
	if users, err := h.Store.ListUsers(ctx, "", 500); err == nil {
		pc.Admin.Users = users
	}
	h.render(w, "admin_spaces", pc)
}

// adminAction wraps the shared shape of every /admin/... form POST:
// admin-scope gate, form parse, do, redirect back to /admin/spaces
// (with the error carried in ?err= so the page can show it).
func (h *Handler) adminAction(w http.ResponseWriter, r *http.Request, do func() error) {
	if !isAdmin(r) {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}
	dest := "/admin/spaces"
	q := url.Values{}
	if t := r.URL.Query().Get("token"); t != "" {
		q.Set("token", t)
	}
	if err := do(); err != nil {
		q.Set("err", err.Error())
	}
	if len(q) > 0 {
		dest += "?" + q.Encode()
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// adminSpaceCreate handles POST /admin/spaces/create. Field: name.
func (h *Handler) adminSpaceCreate(w http.ResponseWriter, r *http.Request) {
	h.adminAction(w, r, func() error {
		_, err := h.Store.CreateSpace(r.Context(), strings.TrimSpace(r.FormValue("name")))
		return err
	})
}

// adminGroupCreate handles POST /admin/groups/create. Field: name.
func (h *Handler) adminGroupCreate(w http.ResponseWriter, r *http.Request) {
	h.adminAction(w, r, func() error {
		_, err := h.Store.CreateGroup(r.Context(), strings.TrimSpace(r.FormValue("name")))
		return err
	})
}

// adminGroupMemberAdd handles POST /admin/groups/{id}/members/add.
// Field: user_id.
func (h *Handler) adminGroupMemberAdd(w http.ResponseWriter, r *http.Request) {
	h.adminAction(w, r, func() error {
		return h.Store.AddGroupMember(r.Context(),
			chi.URLParam(r, "id"), strings.TrimSpace(r.FormValue("user_id")))
	})
}

// adminGroupMemberRemove handles POST /admin/groups/{id}/members/remove.
// Field: user_id.
func (h *Handler) adminGroupMemberRemove(w http.ResponseWriter, r *http.Request) {
	h.adminAction(w, r, func() error {
		return h.Store.RemoveGroupMember(r.Context(),
			chi.URLParam(r, "id"), strings.TrimSpace(r.FormValue("user_id")))
	})
}

// adminSpaceACLSet handles POST /admin/spaces/{id}/acl.
// Fields: group_id, role (admin|member).
func (h *Handler) adminSpaceACLSet(w http.ResponseWriter, r *http.Request) {
	h.adminAction(w, r, func() error {
		return h.Store.SetSpaceACL(r.Context(), chi.URLParam(r, "id"),
			strings.TrimSpace(r.FormValue("group_id")),
			strings.TrimSpace(r.FormValue("role")))
	})
}

// adminSpaceACLRemove handles POST /admin/spaces/{id}/acl/remove.
// Field: group_id.
func (h *Handler) adminSpaceACLRemove(w http.ResponseWriter, r *http.Request) {
	h.adminAction(w, r, func() error {
		return h.Store.RemoveSpaceACL(r.Context(), chi.URLParam(r, "id"),
			strings.TrimSpace(r.FormValue("group_id")))
	})
}
