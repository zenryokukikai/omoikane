package dashboard

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/auth"
	"github.com/zenryokukikai/omoikane/internal/store"
)

// ----------------------------------------------------------------------
// Phase 5 — /chat, the shared librarian-coordination room (read + write
// from the dashboard): thread list/detail pages and the form-POST write
// surface (create/post/close). intent=talk threads are excluded — those
// are personal conversations surfaced only on /talk (issue #60 slice 4).
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
