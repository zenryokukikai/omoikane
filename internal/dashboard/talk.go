package dashboard

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/store"
)

// ----------------------------------------------------------------------
// /talk — the per-user responder chat (Open-WebUI-style two-pane page):
// the page handler, the personal-librarian identity resolution (#73),
// and the virtualized message-window fragments (#45).
// ----------------------------------------------------------------------

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
