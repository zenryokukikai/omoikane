package dashboard

import (
	"bytes"
	"context"
	"html/template"
	"net/url"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"

	"github.com/kojira/omoikane/internal/store"
)

// md is the shared goldmark instance. Configured to:
//   - allow GitHub-Flavoured-Markdown extensions (tables, strikethrough,
//     task lists, autolinks)
//   - keep raw HTML disabled (goldmark default — html.WithUnsafe NOT
//     set) so user-supplied content can't inject `<script>` tags
//   - auto-generate slugged heading IDs for in-page anchors
var md = goldmark.New(
	goldmark.WithExtensions(
		extension.GFM,
	),
	goldmark.WithParserOptions(
		parser.WithAutoHeadingID(),
	),
	goldmark.WithRendererOptions(
		html.WithHardWraps(),
	),
)

// renderMarkdown converts the input markdown to safe HTML. Errors fall
// back to a plain HTML-escaped string so a parser glitch never blanks
// the page.
func renderMarkdown(text string) template.HTML {
	var buf bytes.Buffer
	if err := md.Convert([]byte(text), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(text))
	}
	return template.HTML(buf.String())
}

// renderContent is the "full pipeline" used by dashboard templates for
// entry bodies and chat messages:
//
//  1. Markdown render (escapes inline HTML, produces safe HTML)
//  2. Wiki-link transform on the output (`[[T-XXX]]` → `<a class="wiki">`)
//  3. @mention decoration on the output (`@curator` → `<span>`)
//  4. Attachment unfurl: `<img src="attached:a-xxx">` produced by
//     goldmark from `![caption](attached:a-xxx)` markdown is rewritten
//     to an inline `<img>` / `<video>` / `<audio>` / download link
//     based on the attachment's stored mime type.
//
// The wiki and mention regexes match plain `[[…]]` and `@<role>`
// substrings; goldmark passes those through unchanged because brackets
// and `@` aren't special HTML or markdown punctuation in those forms.
//
// `s` is used for the attachment unfurl step. When nil (e.g. in unit
// tests that don't care about attachments), attached: refs are left
// as-is — the test sees the raw `attached:a-xxx` token rather than
// failing.
func renderContent(text, token string, s *store.Store) template.HTML {
	out := string(renderMarkdown(text))

	// Wiki links — operates on the rendered HTML. wikiLinkRE is safe
	// to run here because [[…]] is preserved verbatim by goldmark.
	out = wikiLinkRE.ReplaceAllStringFunc(out, func(match string) string {
		groups := wikiLinkRE.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		id := groups[1]
		label := id
		if len(groups) >= 3 && groups[2] != "" {
			label = groups[2]
		}
		return `<a href="` + wikiHref(id, token) + `" class="wiki">` +
			template.HTMLEscapeString(label) + `</a>`
	})

	// @mentions — same approach.
	out = mentionRenderRE.ReplaceAllStringFunc(out, func(match string) string {
		groups := mentionRenderRE.FindStringSubmatch(match)
		if len(groups) < 3 {
			return match
		}
		prefix, role := groups[1], groups[2]
		return prefix + `<span class="mention mention-` + role + `">@` + role + `</span>`
	})

	// Attachment unfurl. `![cap](attached:a-xxx)` ⇒ goldmark renders
	// `<img src="attached:a-xxx" alt="cap">`. We swap based on mime.
	if s != nil {
		out = attachedImgRE.ReplaceAllStringFunc(out, func(match string) string {
			groups := attachedImgRE.FindStringSubmatch(match)
			if len(groups) < 3 {
				return match
			}
			id, alt := groups[1], groups[2]
			a, err := s.GetAttachment(context.Background(), id)
			if err != nil {
				// Unknown attachment id: render as a muted placeholder
				// rather than break the page. Lets the human spot the
				// dangling reference.
				return `<span class="muted">[missing attachment ` +
					template.HTMLEscapeString(id) + `]</span>`
			}
			return attachmentHTML(a, alt, token)
		})
	}

	return template.HTML(out)
}

// attachedImgRE matches an `<img src="attached:a-xxx" alt="...">` tag
// as emitted by goldmark for `![cap](attached:a-xxx)` markdown. We
// capture the attachment id and the alt text. The regex is lenient
// about other attribute order and whitespace because goldmark's exact
// output formatting isn't part of its public contract.
var attachedImgRE = regexp.MustCompile(
	`<img\s+src="attached:(a-[0-9a-fA-F]+)"(?:\s+alt="([^"]*)")?[^>]*>`)

// attachmentHTML returns the inline HTML for an attachment, chosen by
// mime type. alt is the markdown image caption (may be empty); we
// prefer it over the stored caption when present because the writer
// chose it for this specific reference, but fall back to the stored
// caption so a bare `![](attached:a-xxx)` still has agent-readable
// metadata in the rendered page.
//
// `token` is the dashboard's request token (passed via ?token= on
// the parent page URL). When non-empty it gets appended to the
// attachment content URL so the browser's <img>/<video> fetch can
// authenticate without a session cookie — necessary for users who
// load the dashboard via `?token=` instead of the OAuth cookie
// path.
func attachmentHTML(a *store.Attachment, altFromMarkdown, token string) string {
	src := "/v1/attachments/" + a.ID + "/content"
	if token != "" {
		src += "?token=" + url.QueryEscape(token)
	}
	caption := altFromMarkdown
	if caption == "" {
		caption = a.Caption
	}
	escCap := template.HTMLEscapeString(caption)
	switch {
	case strings.HasPrefix(a.Mime, "image/"):
		return `<figure class="attachment attachment-image">` +
			`<img src="` + src + `" alt="` + escCap +
			`" loading="lazy" data-attachment-id="` + a.ID + `">` +
			`<figcaption>` + escCap + `</figcaption></figure>`
	case strings.HasPrefix(a.Mime, "video/"):
		return `<figure class="attachment attachment-video">` +
			`<video src="` + src + `" controls preload="metadata"` +
			` data-attachment-id="` + a.ID + `"></video>` +
			`<figcaption>` + escCap + `</figcaption></figure>`
	case strings.HasPrefix(a.Mime, "audio/"):
		return `<figure class="attachment attachment-audio">` +
			`<audio src="` + src + `" controls preload="metadata"` +
			` data-attachment-id="` + a.ID + `"></audio>` +
			`<figcaption>` + escCap + `</figcaption></figure>`
	default:
		// Non-renderable type — download link with caption + size.
		// Browsers will still try to display PDFs / text inline via
		// Content-Disposition: inline (set by the API), but we don't
		// embed a viewer here.
		label := caption
		if label == "" {
			if a.Filename != "" {
				label = a.Filename
			} else {
				label = a.ID
			}
		}
		return `<a class="attachment attachment-file" href="` + src +
			`" data-attachment-id="` + a.ID + `">📎 ` +
			template.HTMLEscapeString(label) + `</a>`
	}
}
