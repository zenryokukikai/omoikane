package dashboard

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/zenryokukikai/omoikane/internal/auth"
	"github.com/zenryokukikai/omoikane/internal/opencrab"
	"github.com/zenryokukikai/omoikane/internal/store"
)

// ----------------------------------------------------------------------
// /my/librarian — personal librarian settings (issue #73 slice A).
//
// The signed-in user names their librarian and writes its persona; on
// save, omoikane provisions the agent onto the opencrab runtime via
// h.Librarian. The whole surface only exists when the runtime is
// configured (OPENCRAB_URL) — otherwise both routes answer 404.
//
// Security boundary: the operated agent is ALWAYS the signed-in user's
// own — user_id comes from the auth context, never from the form, and
// the agent id is derived server-side ("plib-<user_id>").
// ----------------------------------------------------------------------

// LibrarianProvisioner is what the settings page needs from the
// opencrab client. An interface so tests can inject a fake runtime.
type LibrarianProvisioner interface {
	Provision(ctx context.Context, p opencrab.ProvisionParams) error
}

// personalLibrarianTokenName is the api_tokens.name of the kb token
// issued to a user's personal librarian. Its existence (per user) is
// the idempotency check — re-saving never mints a second token.
const personalLibrarianTokenName = "personal-librarian"

const (
	librarianNameMaxRunes    = 50
	librarianPersonaMaxRunes = 2000
)

// personalLibrarianAgentID derives the runtime agent id for a user.
// Server-side only — never read from a request.
func personalLibrarianAgentID(userID string) string {
	return "plib-" + userID
}

func (h *Handler) myLibrarianPage(w http.ResponseWriter, r *http.Request) {
	if h.Librarian == nil {
		http.NotFound(w, r)
		return
	}
	pc := h.renderCtx(r)
	pc.Title = "omoikane — 個人司書"
	if pc.Me == nil {
		http.Redirect(w, r, "/login?next=/my/librarian", http.StatusFound)
		return
	}
	if ul, err := h.Store.GetUserLibrarian(r.Context(), pc.Me.ID); err == nil {
		pc.MyLibrarian = ul
		pc.LibrarianName = ul.Name
		pc.LibrarianPersona = ul.Persona
	}
	pc.LibrarianSaved = r.URL.Query().Get("saved") == "1"
	h.render(w, "my_librarian", pc)
}

func (h *Handler) myLibrarianSave(w http.ResponseWriter, r *http.Request) {
	if h.Librarian == nil {
		http.NotFound(w, r)
		return
	}
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		http.Redirect(w, r, "/login?next=/my/librarian", http.StatusFound)
		return
	}
	me, err := h.Store.GetUser(r.Context(), tok.UserID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	persona := r.FormValue("persona") // don't trim — formatting is intentional

	if name == "" {
		h.renderLibrarianError(w, r, me, name, persona,
			"名前は必須です", http.StatusBadRequest)
		return
	}
	if utf8.RuneCountInString(name) > librarianNameMaxRunes {
		h.renderLibrarianError(w, r, me, name, persona,
			"名前は50文字以内にしてください", http.StatusBadRequest)
		return
	}
	if utf8.RuneCountInString(persona) > librarianPersonaMaxRunes {
		h.renderLibrarianError(w, r, me, name, persona,
			"性格は2000文字以内にしてください", http.StatusBadRequest)
		return
	}

	// Token idempotency: mint only when the user doesn't already hold a
	// live personal-librarian token. The token row's existence is the
	// single source of truth — the workspace .kb.curlrc is written in
	// the same provisioning pass that mints the token, and a failed pass
	// revokes the fresh token, so "token exists" always implies "curlrc
	// was written".
	kbToken := ""
	has, err := h.Store.HasAPIToken(r.Context(), me.ID, personalLibrarianTokenName)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !has {
		kbToken, err = h.Store.CreateToken(r.Context(), me.ID,
			personalLibrarianTokenName, []string{"read", "write"}, nil)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	if err := h.Librarian.Provision(r.Context(), opencrab.ProvisionParams{
		AgentID:  personalLibrarianAgentID(me.ID),
		UserName: me.Name,
		Name:     name,
		Persona:  persona,
		KBToken:  kbToken,
	}); err != nil {
		if kbToken != "" {
			// Keep token⇔curlrc paired: the workspace write didn't
			// (necessarily) happen, so the fresh token must not survive —
			// otherwise the next save would skip the curlrc step forever.
			_ = h.Store.RevokeToken(r.Context(), kbToken)
		}
		h.renderLibrarianError(w, r, me, name, persona,
			"エージェント基盤への敷設に失敗しました: "+err.Error(), http.StatusBadGateway)
		return
	}

	if err := h.Store.UpsertUserLibrarian(r.Context(), &store.UserLibrarian{
		UserID:  me.ID,
		AgentID: personalLibrarianAgentID(me.ID),
		Name:    name,
		Persona: persona,
		Status:  "active",
	}); err != nil {
		h.renderLibrarianError(w, r, me, name, persona,
			"設定の保存に失敗しました: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// PRG: reload the settings page with a success banner. Preserve
	// ?token= for the form-auth path.
	dest := "/my/librarian?saved=1"
	if t := r.URL.Query().Get("token"); t != "" {
		dest += "&token=" + t
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// renderLibrarianError re-renders the settings form with an error
// banner, echoing the user's input so nothing typed is lost.
func (h *Handler) renderLibrarianError(w http.ResponseWriter, r *http.Request,
	me *store.User, name, persona, msg string, status int) {
	pc := h.renderCtx(r)
	pc.Title = "omoikane — 個人司書"
	if ul, err := h.Store.GetUserLibrarian(r.Context(), me.ID); err == nil {
		pc.MyLibrarian = ul
	} else if !errors.Is(err, store.ErrNotFound) {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pc.LibrarianName = name
	pc.LibrarianPersona = persona
	pc.LibrarianError = msg
	w.WriteHeader(status)
	h.render(w, "my_librarian", pc)
}
