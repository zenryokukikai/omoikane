package dashboard

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

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

// GateRegistrar is what the save flow needs from the external gate
// provisioner (issue #104, V3 contract): the per-user instance PUT (the
// V3 admin plane has no kind/schema registration). EnsureInstance
// answers ("", nil) when registration was skipped (no subject mapping
// yet) — the save proceeds without a gate instance.
type GateRegistrar interface {
	EnsureInstance(ctx context.Context, agentID, existingInstanceID string) (string, error)
}

// personalLibrarianTokenName is the api_tokens.name of the kb token
// issued to a user's personal librarian. Its existence (per user) is
// the idempotency check — re-saving never mints a second token.
const personalLibrarianTokenName = "personal-librarian"

const (
	librarianNameMaxRunes    = 50
	librarianPersonaMaxRunes = 2000
	librarianIconMaxRunes    = 8 // text icon: an emoji (ZWJ sequences included), not a sentence
	// Uploaded icon image cap. Must stay comfortably under the server's
	// whole-body limit (KB_REQUEST_BODY_MAX, default 1MB) — the body
	// also carries the persona text and multipart framing, and a file
	// at the whole-body limit would die in LimitBody with a raw 400
	// before this handler's friendly message could run.
	librarianIconImageMax = 512 << 10
)

// librarianIconMimes is the allow-list for uploaded icon images,
// matched against http.DetectContentType's sniff of the actual bytes
// (the client-declared type is never trusted).
var librarianIconMimes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

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
		pc.LibrarianIcon = ul.Icon
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
	// The form is multipart now (icon image upload); urlencoded posts
	// (older cached pages, tests without a file) still parse fine.
	if err := r.ParseMultipartForm(librarianIconImageMax + 64*1024); err != nil {
		// The server-wide body cap (LimitBody) fires before our
		// per-file check can — turn it into the friendly size message
		// instead of a raw "request body too large".
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			h.renderLibrarianError(w, r, me, "", "", "",
				"送信サイズが大きすぎます(アイコン画像は512KBまでにしてください)", http.StatusRequestEntityTooLarge)
			return
		}
		if !errors.Is(err, http.ErrNotMultipart) {
			http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	name := strings.TrimSpace(r.FormValue("name"))
	persona := r.FormValue("persona") // don't trim — formatting is intentional
	icon := strings.TrimSpace(r.FormValue("icon"))

	if name == "" {
		h.renderLibrarianError(w, r, me, name, persona, icon,
			"名前は必須です", http.StatusBadRequest)
		return
	}
	if utf8.RuneCountInString(name) > librarianNameMaxRunes {
		h.renderLibrarianError(w, r, me, name, persona, icon,
			"名前は50文字以内にしてください", http.StatusBadRequest)
		return
	}
	if utf8.RuneCountInString(persona) > librarianPersonaMaxRunes {
		h.renderLibrarianError(w, r, me, name, persona, icon,
			"性格は2000文字以内にしてください", http.StatusBadRequest)
		return
	}
	if utf8.RuneCountInString(icon) > librarianIconMaxRunes || strings.ContainsFunc(icon, unicode.IsControl) {
		h.renderLibrarianError(w, r, me, name, persona, icon,
			"アイコンは絵文字1つ程度(8文字以内)にしてください", http.StatusBadRequest)
		return
	}

	// Validate the uploaded icon image (if any) BEFORE mutating
	// anything: type is sniffed from the bytes, never taken from the
	// client's declared content type.
	// Only a multipart post can carry a file — FormFile on a urlencoded
	// post errors with ErrNotMultipart, not ErrMissingFile.
	var iconImg []byte
	var iconMime string
	switch f, _, err := r.FormFile("icon_file"); {
	case r.MultipartForm == nil || errors.Is(err, http.ErrMissingFile):
		// urlencoded post, or multipart without a file — nothing to do.
	case err != nil:
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	default:
		data, rerr := io.ReadAll(io.LimitReader(f, librarianIconImageMax+1))
		f.Close()
		if rerr != nil {
			h.renderLibrarianError(w, r, me, name, persona, icon,
				"アイコン画像の読み取りに失敗しました", http.StatusBadRequest)
			return
		}
		if len(data) > librarianIconImageMax {
			h.renderLibrarianError(w, r, me, name, persona, icon,
				"アイコン画像は512KBまでにしてください", http.StatusBadRequest)
			return
		}
		if len(data) > 0 {
			m := http.DetectContentType(data)
			if !librarianIconMimes[m] {
				h.renderLibrarianError(w, r, me, name, persona, icon,
					"アイコン画像は PNG / JPEG / GIF / WebP にしてください", http.StatusBadRequest)
				return
			}
			iconImg, iconMime = data, m
		}
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
		AgentID: personalLibrarianAgentID(me.ID),
		// The librarian's owner is the signed-in user who saved it
		// (issue #137) — their kb user id, the same identity the
		// gateway stamps on their messages, taken from the auth
		// context like the agent id above, never from the form.
		OwnerID:  me.ID,
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
		h.renderLibrarianError(w, r, me, name, persona, icon,
			"エージェント基盤への敷設に失敗しました: "+err.Error(), http.StatusBadGateway)
		return
	}

	// External gate registration (issue #104 G2) — AFTER successful
	// opencrab provisioning, before the row upsert. Failures here do
	// NOT revoke a fresh kb token: Provision already wrote the curlrc,
	// so the token⇔curlrc pairing holds. A skip (resolver still
	// upstream work) returns an empty id and the save proceeds.
	gateInstanceID := ""
	if h.Gate != nil {
		existing := ""
		if ul, err := h.Store.GetUserLibrarian(r.Context(), me.ID); err == nil {
			existing = ul.GateInstanceID
		}
		id, err := h.Gate.EnsureInstance(r.Context(), personalLibrarianAgentID(me.ID), existing)
		if err != nil {
			h.renderLibrarianError(w, r, me, name, persona, icon,
				"外部ゲートへの登録に失敗しました: "+err.Error(), http.StatusBadGateway)
			return
		}
		if id != existing {
			gateInstanceID = id
		}
	}

	if err := h.Store.UpsertUserLibrarian(r.Context(), &store.UserLibrarian{
		UserID:  me.ID,
		AgentID: personalLibrarianAgentID(me.ID),
		Name:    name,
		Persona: persona,
		Status:  "active",
		Icon:    icon,
	}); err != nil {
		h.renderLibrarianError(w, r, me, name, persona, icon,
			"設定の保存に失敗しました: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Persist the freshly registered gate instance id (the row surely
	// exists after the upsert above).
	if gateInstanceID != "" {
		if err := h.Store.SetUserLibrarianGateInstance(r.Context(), me.ID, gateInstanceID); err != nil {
			h.renderLibrarianError(w, r, me, name, persona, icon,
				"ゲート登録の保存に失敗しました: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Icon image last — the row surely exists now. A fresh upload wins
	// over the clear checkbox (both checked = replace).
	if r.FormValue("icon_image_clear") != "" && iconImg == nil {
		if err := h.Store.ClearUserLibrarianIconImage(r.Context(), me.ID); err != nil {
			h.renderLibrarianError(w, r, me, name, persona, icon,
				"アイコン画像の削除に失敗しました: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if iconImg != nil {
		if err := h.Store.SetUserLibrarianIconImage(r.Context(), me.ID, iconImg, iconMime); err != nil {
			h.renderLibrarianError(w, r, me, name, persona, icon,
				"アイコン画像の保存に失敗しました: "+err.Error(), http.StatusInternalServerError)
			return
		}
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
	me *store.User, name, persona, icon, msg string, status int) {
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
	pc.LibrarianIcon = icon
	pc.LibrarianError = msg
	w.WriteHeader(status)
	h.render(w, "my_librarian", pc)
}

// librarianIconImage serves the uploaded icon image. The personal
// librarian is private to its user, so only the owner (and an admin)
// may fetch it — everyone else gets the uniform 404.
func (h *Handler) librarianIconImage(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userID")
	tok := auth.FromContext(r.Context())
	if tok == nil || userID == "" || (tok.UserID != userID && !isAdmin(r)) {
		http.NotFound(w, r)
		return
	}
	img, mime, err := h.Store.GetUserLibrarianIconImage(r.Context(), userID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime)
	// The mime came from our own byte sniff at upload, but forbid the
	// browser from re-sniffing anyway (image/HTML polyglots).
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Private (auth-gated) + long max-age: the URL carries ?v=<icon_ver>
	// so replacements bust the cache by changing the URL, not by expiry.
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(img)
}
