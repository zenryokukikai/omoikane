package dashboard

import (
	"context"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/dist/samples"
	"github.com/zenryokukikai/omoikane/internal/store"
	"github.com/zenryokukikai/omoikane/internal/version"
)

// ----------------------------------------------------------------------
// Template plumbing: page-template construction (New/newFromFS + the
// shared FuncMap) and the response helpers every page handler funnels
// through (render, css, sample-helper serving).
// ----------------------------------------------------------------------

// New parses one *template.Template *per page* — html/template has no
// per-file scoping, so a single ParseFS over all pages would let the last
// `{{define "content"}}` win for every page.
func New(s *store.Store, open bool) (*Handler, error) {
	return newFromFS(s, open, templatesFS)
}

// newFromFS is the testable form of New. Tests inject a broken fs.FS to
// exercise the error-return branch that the embedded templatesFS can never
// actually hit.
func newFromFS(s *store.Store, open bool, fsys fs.FS) (*Handler, error) {
	funcs := template.FuncMap{
		"trunc": trunc,
		// replyAuthor names the author of the comment a reply points
		// at, for the ↪ indicator inside threads (reply-to-reply).
		"replyAuthor": func(all []*store.EntryComment, id string) string {
			for _, c := range all {
				if c.ID == id {
					return c.AuthorName
				}
			}
			return ""
		},
		"urlq":       url.QueryEscape,
		"langSwitch": langSwitch,
		// navURL builds a safe template.URL for an internal nav link with
		// optional token + lang query params. Sidesteps html/template's URL-
		// context analyzer (which rejects dynamic values in {{if}}-built
		// query strings as ambiguous). path must be a static prefix; the
		// caller threads token and lang through here.
		"navURL": func(path, token, lang string) template.URL {
			qs := url.Values{}
			if token != "" {
				qs.Set("token", token)
			}
			if lang != "" {
				qs.Set("lang", lang)
			}
			if len(qs) == 0 {
				return template.URL(path)
			}
			return template.URL(path + "?" + qs.Encode())
		},
		"deref":     deref,
		"wikiLinks": wikiLinks,
		// markdown + wiki + mentions + attachment unfurl in one shot;
		// preferred renderer for entry bodies and chat messages.
		// Captures `s` so attachment refs can be resolved at render
		// time without threading the store through every template
		// invocation. Takes the request context (templates pass $.Ctx)
		// so the store lookups inside (EntriesExist / GetAttachment)
		// run under the viewer's space visibility — a background
		// context here would be an unrestricted bypass (issue #60
		// slice 5).
		"renderContent": func(ctx context.Context, text, token string) template.HTML {
			return renderContent(ctx, text, token, s)
		},
		// ltime renders a timestamp as a <time> element that layout.html's
		// localizer script rewrites into the viewer's timezone (#43). The
		// UTC-formatted text is the no-JS fallback. p picks the shape:
		// date / dt / t / dts / md (see localizer for the exact formats).
		"ltime":         ltime,
		"talkAgentName": talkAgentName,
		// appVersion lets layout.html's footer show the running version
		// on every page without threading it through each handler's data.
		"appVersion": version.String,
		// assetVersion busts the browser CSS cache on each deploy. The
		// stylesheet is served with a 4h max-age, so without a changing
		// URL a redeploy leaves stale CSS in browsers. Tie the URL to the
		// build (git SHA, or app semver for un-stamped local builds).
		"assetVersion": func() string {
			if version.Build != "" && version.Build != "dev" {
				return version.Build
			}
			return version.App
		},
		// isJournal reports whether an entry is a summarizer daily journal,
		// so the entry page can render it as a clean reading sheet.
		"isJournal": func(e *store.Entry) bool { return metaKind(e) == "daily_journal" },
		// journalDate pulls metadata.journal_date off a daily-journal entry
		// (falls back to the created date) for the journal index.
		"journalDate": func(e *store.Entry) string {
			if len(e.Metadata) > 0 {
				var m struct {
					JournalDate string `json:"journal_date"`
				}
				if json.Unmarshal(e.Metadata, &m) == nil && m.JournalDate != "" {
					return m.JournalDate
				}
			}
			return e.CreatedAt.Format("2006-01-02")
		},
		// journalPosted shows when a journal was actually written, in JST,
		// so a reader can tell "this morning's journal" from an older one —
		// the journal *covers* the previous day but is *posted* the next
		// morning, and the index date alone hides that distinction.
		"journalPosted": func(e *store.Entry) string {
			jst := time.FixedZone("JST", 9*60*60)
			return e.CreatedAt.In(jst).Format("2006-01-02 15:04")
		},
	}
	pages := map[string]*template.Template{}
	for _, name := range []string{"home", "journal", "project", "entry", "entry_history", "search",
		"review_queue", "clusters", "cluster", "situations", "situation",
		"browse", "browse_node", "index", "lookup", "use_case", "entries", "entry_new",
		"chat_threads", "chat_thread", "talk", "bookmarks", "directives", "login", "claim", "agents", "profile",
		"members", "member_claim", "admin_spaces", "my_librarian"} {
		t, err := template.New(name).Funcs(funcs).ParseFS(fsys,
			"templates/layout.html",
			"templates/"+name+".html")
		if err != nil {
			return nil, err
		}
		pages[name] = t
	}
	return &Handler{Store: s, Open: open, pages: pages}, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func (h *Handler) css(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write([]byte(stylesheet))
}

// serveSampleHelper serves one of the sample helper shell scripts
// embedded in the binary. Public (no auth required) — the scripts
// are MIT-licensed sample code, not credentials.
//
// Only files ending in .sh under agent-helpers/ are served; anything
// else returns 404. The chi {name} param is path-cleaned to defend
// against `../` traversal attempts.
func (h *Handler) serveSampleHelper(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	// Defence in depth: chi already prevents `..` in segment values,
	// but path.Clean + suffix check is a cheap second layer.
	if name != path.Base(name) || !strings.HasSuffix(name, ".sh") {
		http.NotFound(w, r)
		return
	}
	data, err := samples.AgentHelpers.ReadFile("agent-helpers/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(data)
}

func (h *Handler) render(w http.ResponseWriter, page string, data any) {
	tpl, ok := h.pages[page]
	if !ok {
		http.Error(w, "no such page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Pages carry live-update JS that evolves with deploys; no-cache
	// makes every (re)load revalidate so stale scripts can't linger.
	w.Header().Set("Cache-Control", "no-cache")
	if err := tpl.ExecuteTemplate(w, "layout", data); err != nil {
		_, _ = w.Write([]byte("<pre>template error: " + template.HTMLEscapeString(err.Error()) + "</pre>"))
	}
}
