package dashboard

import (
	"fmt"
	"html/template"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// ----------------------------------------------------------------------
// Small template-func helpers shared across pages (talkAgentName, trunc,
// ltime, deref) and the [[X-XXXX]] wiki-link rendering (wikiLinks +
// wikiHref). mentionRenderRE lives here too — its consumer is
// renderContent in markdown.go.
// ----------------------------------------------------------------------

// talkAgentName is the /talk responder's display name. The concrete
// persona name is deployment configuration (env), never code (#51).
func talkAgentName() string {
	if v := os.Getenv("KB_TALK_AGENT_NAME"); v != "" {
		return v
	}
	return "コンシェルジュ"
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ltime emits a <time> element the layout localizer rewrites into the
// viewer's timezone (#43). The text content is the UTC rendering — what
// a no-JS client (or a test) sees. Zero/nil times render as an empty
// string so optional fields don't show the epoch. Accepts time.Time or
// *time.Time — optional store fields are pointers and templates pass
// function arguments without auto-dereferencing.
func ltime(v any, p string) template.HTML {
	var t time.Time
	switch x := v.(type) {
	case time.Time:
		t = x
	case *time.Time:
		if x == nil {
			return ""
		}
		t = *x
	default:
		return ""
	}
	if t.IsZero() {
		return ""
	}
	layout := "2006-01-02 15:04"
	switch p {
	case "date":
		layout = "2006-01-02"
	case "t":
		layout = "15:04"
	case "ts":
		layout = "15:04:05"
	case "dts":
		layout = "2006-01-02 15:04:05"
	case "md":
		layout = "01-02 15:04"
	}
	u := t.UTC()
	return template.HTML(fmt.Sprintf("<time data-p=%q datetime=%q>%s</time>",
		p, u.Format(time.RFC3339), u.Format(layout)))
}

// deref unwraps a *float64 for template printf use. Returns 0 for nil.
func deref(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// wikiLinkRE matches [[X-XXXX]] and [[X-XXXX|alt text]] forms. The ID
// must start with one of the entry-type prefixes (T|D|X|L|I|M|F|E or H
// for hierarchy / SIT for situations / CL for clusters) followed by `-`
// and base32-ish alphanumerics.
var wikiLinkRE = regexp.MustCompile(`\[\[((?:T|D|X|L|I|M|F|E|H|SIT|CL|CASE|SM)-[A-Za-z0-9]+)(?:\|([^\]]+))?\]\]`)

// mentionRenderRE mirrors store.mentionRE — kept duplicated rather than
// imported so the dashboard package doesn't form a circular dep with
// store's regex internals. Roles must stay in sync.
var mentionRenderRE = regexp.MustCompile(
	`(^|[^A-Za-z0-9_])@(coordinator|cataloger|curator|detective|conservator|scout|summarizer|judge|human)\b`)

// wikiLinks renders `[[T-XXXX]]` references inside plain text fields as
// HTML anchors to the corresponding entry page. Tokens that don't match
// the entry-ID shape are left untouched. The output is HTML-escaped
// first so the function is XSS-safe when fed user content; this means
// the caller's template should pipe the result through `{{...}}` as
// `template.HTML` to surface the links.
func wikiLinks(text, token string) template.HTML {
	escaped := template.HTMLEscapeString(text)
	out := wikiLinkRE.ReplaceAllStringFunc(escaped, func(match string) string {
		groups := wikiLinkRE.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		id := groups[1]
		label := id
		if len(groups) >= 3 && groups[2] != "" {
			label = groups[2]
		}
		href := wikiHref(id, token)
		return `<a href="` + href + `" class="wiki">` + template.HTMLEscapeString(label) + `</a>`
	})
	return template.HTML(out)
}

// wikiHref routes an ID prefix to the right dashboard page. Entry IDs
// (T/D/X/L/I/M/F/E) go to `/entries/{id}`; H- to `/browse/{id}`; SIT- to
// `/situations/{id}`; CL- to `/clusters/{id}`. Anything else falls back
// to the entry page since unknown prefixes most likely came from a
// freshly-added entry type.
func wikiHref(id, token string) string {
	prefix := id
	if i := strings.IndexByte(id, '-'); i > 0 {
		prefix = id[:i]
	}
	var base string
	switch prefix {
	case "H":
		base = "/browse/" + id
	case "SIT":
		base = "/situations/" + id
	case "CL":
		base = "/clusters/" + id
	default:
		base = "/entries/" + id
	}
	if token != "" {
		base += "?token=" + url.QueryEscape(token)
	}
	return base
}
