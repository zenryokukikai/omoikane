package dashboard

// CSS class guard (issue #122) — the twin of the #119 defect: a class
// name written into a template's class="" attribute with no matching rule
// in the served stylesheet renders unstyled (the reported case was a
// "✏️ write here" link that was meant to be a button). Compilation and
// every existing test pass, so the defect ships until a human opens the
// page — on a phone, in #119's case. This guard catches it at commit time.
//
// Two directions, each with an explicit, reasoned allowlist so the guard
// starts GREEN on main and gets stricter only as gaps are filled:
//
//	Forward (TestTemplateClassesHaveCSS): every STATIC class token in a
//	  template class="" attribute must have a selector somewhere in the
//	  assembled `stylesheet`, unless it is listed in forwardKnownGaps.
//	  This is the check that would have failed on #119 / the .btn defect.
//
//	Reverse (TestStylesheetClassesAreUsed): every class SELECTOR in the
//	  stylesheet should be reachable from a template — as a literal token,
//	  via a dynamic `prefix-{{.Value}}` composition, or through an explicit
//	  allowlist for classes emitted by Go (markdown.go) or template JS.
//	  Whatever is left is dead CSS, listed in reverseKnownOrphans.
//
// Only *.html templates are walked: the one *.tmpl file is the agent
// skill markdown, which is never rendered to a styled HTML page.

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ---- allowlists ------------------------------------------------------

// forwardKnownGaps lists template classes that currently have NO CSS rule.
// Each is a KNOWN GAP tracked by #122 — a class someone wrote intending to
// style it and never did, NOT a class that is intentionally unstyled. The
// way to close one out is to add its CSS rule and DELETE its line here;
// the guard fails if a line here has gained a rule (stale) or is no longer
// used by any template, so the ledger cannot rot.
var forwardKnownGaps = map[string]string{
	"btn":                  "KNOWN GAP #122: buttons render as plain links (user-visible, top priority)",
	"entries-quick-filter": "KNOWN GAP #122: Quick views have no spacing/separators (folded into #120)",
	"badge-space":          "KNOWN GAP #122: space badge not visually distinct from the default badge",
	"entry-new-form":       "KNOWN GAP #122: new-entry form has no layout rule",
	"chat-status-filter":   "KNOWN GAP #122: chat status filter unstyled",
	"cmt-replyto":          "KNOWN GAP #122: reply-to indicator unstyled",
}

// reverseGoPrefixes are class-name PREFIXES composed outside any template
// class="" attribute, so the reverse check cannot see them as literals.
// Kept as prefixes (not per-value literals) so a new librarian role never
// needs a new allowlist line.
var reverseGoPrefixes = []string{
	"mention-", // markdown.go: `<span class="mention mention-<role>">` (@mention decoration)
}

// reverseExternalUse are whole class names produced by Go or by template
// JS rather than by a static class="" attribute. Not defects — the rule is
// real and reachable, just not from an HTML attribute the walker can see.
var reverseExternalUse = map[string]string{
	"mention":         "markdown.go: @mention span base class",
	"wiki":            "markdown.go: [[wiki link]] span base class",
	"wiki-broken":     "markdown.go: unresolved [[wiki link]] modifier",
	"attachment":      "markdown.go: attachment link base class",
	"attachment-file": "markdown.go: attachment file-link modifier",
	"talk-msg-sending": "talk.html JS: optimistic-echo row class set via element.className " +
		"(issue #49/#50)",
}

// reverseKnownOrphans is dead CSS: a rule with no template, Go, or JS
// reference. #122 named only .filter-form; the sweep found more (a stale
// review-queue detail cluster, a home journal-today block, two uc-* rules).
// KNOWN GAP #122: remove the rule from styles_*.go and its line here to
// close one out. The guard fails if a line here becomes reachable again
// (someone started using it) or its rule was deleted (already cleaned up),
// so this list cannot silently hide a real orphan either.
var reverseKnownOrphans = map[string]string{
	"filter-form":            "KNOWN GAP #122: dead CSS, no template uses it (named in the issue)",
	"journal-today":          "KNOWN GAP #122: dead CSS, home journal-today block never rendered",
	"jt-label":               "KNOWN GAP #122: dead CSS, part of the journal-today block",
	"jt-title":               "KNOWN GAP #122: dead CSS, part of the journal-today block",
	"rev-chips-sym":          "KNOWN GAP #122: dead CSS, stale review-queue detail cluster",
	"rev-chips-trg":          "KNOWN GAP #122: dead CSS, stale review-queue detail cluster",
	"rev-id":                 "KNOWN GAP #122: dead CSS, stale review-queue detail cluster",
	"rev-list":               "KNOWN GAP #122: dead CSS, stale review-queue detail cluster",
	"rev-meta":               "KNOWN GAP #122: dead CSS, stale review-queue detail cluster",
	"rev-more":               "KNOWN GAP #122: dead CSS, stale review-queue detail cluster",
	"rev-row":                "KNOWN GAP #122: dead CSS, stale review-queue detail cluster",
	"rev-row-head":           "KNOWN GAP #122: dead CSS, stale review-queue detail cluster",
	"rev-tag":                "KNOWN GAP #122: dead CSS, stale review-queue detail cluster",
	"rev-title":              "KNOWN GAP #122: dead CSS, stale review-queue detail cluster",
	"uc-entry-summary-title": "KNOWN GAP #122: dead CSS, use-case summary title never emitted",
	"uc-samples":             "KNOWN GAP #122: dead CSS, use-case samples block never rendered",
}

// ---- extraction ------------------------------------------------------

var (
	// Both quote styles: the repo writes class="…" throughout, but a
	// single-quoted attribute would otherwise slip past the guard with
	// NO error — a silent false negative is exactly the failure this
	// test exists to prevent, so it must not have a blind spot of its own.
	classAttrRE = regexp.MustCompile(`class="([^"]*)"|class='([^']*)'`)
	// tmplActionRE is deliberately non-greedy: dashboard templates never
	// nest }} inside a {{ ... }} action, so shortest-match is correct.
	tmplActionRE = regexp.MustCompile(`\{\{.*?\}\}`)
	cssClassRE   = regexp.MustCompile(`\.([A-Za-z_][A-Za-z0-9_-]*)`)
	cssCommentRE = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

// tmplControlKeywords are actions that emit NO adjacent text, so they can
// be collapsed to whitespace — the classes on either side stand alone
// (`a{{if x}} b{{end}}` is the two classes `a` and `b`). Everything else
// ({{.Field}}, {{$x}}, pipelines) substitutes dynamic text, so the token
// it sits in is only partly static and cannot be verified whole.
var tmplControlKeywords = map[string]bool{
	"if": true, "else": true, "end": true, "range": true, "with": true,
	"template": true, "block": true, "define": true, "break": true, "continue": true,
}

// dynamicSentinel marks where a value substitution produced dynamic text.
// A byte that cannot appear in a class name so it never collides.
const dynamicSentinel = "\x00"

// neutralizeActions rewrites {{ ... }} actions inside one class="" value:
// control actions become a space (their neighbours are standalone classes),
// value substitutions become the sentinel (the token is partly dynamic).
func neutralizeActions(val string) string {
	return tmplActionRE.ReplaceAllStringFunc(val, func(action string) string {
		inner := strings.TrimSpace(action[2 : len(action)-2])
		first := inner
		if i := strings.IndexAny(inner, " \t"); i >= 0 {
			first = inner[:i]
		}
		if tmplControlKeywords[first] {
			return " "
		}
		return dynamicSentinel
	})
}

// templateClasses walks the embedded *.html templates and returns:
//   - static: class token -> sorted template files that use it (fully
//     static tokens only)
//   - prefixes: the static prefix of every partly-dynamic token
//     (`badge-status-{{.X}}` -> `badge-status-`), used by the reverse
//     check to treat `.badge-status-OPEN` etc. as reachable.
func templateClasses(t *testing.T) (static map[string]map[string]bool, prefixes map[string]bool) {
	t.Helper()
	static = map[string]map[string]bool{}
	prefixes = map[string]bool{}
	files, err := fs.Glob(templatesFS, "templates/*.html")
	if err != nil {
		t.Fatalf("glob templates: %v", err)
	}
	for _, f := range files {
		b, err := templatesFS.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		base := strings.TrimPrefix(f, "templates/")
		for _, m := range classAttrRE.FindAllStringSubmatch(string(b), -1) {
			// Group 1 is the double-quoted body, group 2 the single-quoted
			// one; exactly one is non-empty per match.
			attr := m[1]
			if attr == "" {
				attr = m[2]
			}
			for _, tok := range strings.Fields(neutralizeActions(attr)) {
				if strings.Contains(tok, dynamicSentinel) {
					if pre := tok[:strings.Index(tok, dynamicSentinel)]; pre != "" {
						prefixes[pre] = true
					}
					continue
				}
				if static[tok] == nil {
					static[tok] = map[string]bool{}
				}
				static[tok][base] = true
			}
		}
	}
	return static, prefixes
}

// stylesheetClasses returns every class selector present in the assembled
// `stylesheet` (comments stripped first so `.foo` inside a /* */ note is
// not mistaken for a rule). Compound/descendant selectors are handled for
// free: the global match sees `checkbox-inline` in
// `.entries-filter label.checkbox-inline`.
func stylesheetClasses() map[string]bool {
	css := cssCommentRE.ReplaceAllString(stylesheet, "")
	out := map[string]bool{}
	for _, m := range cssClassRE.FindAllStringSubmatch(css, -1) {
		out[m[1]] = true
	}
	return out
}

func sortedFiles(set map[string]bool) string {
	fs := make([]string, 0, len(set))
	for f := range set {
		fs = append(fs, f)
	}
	sort.Strings(fs)
	return strings.Join(fs, ", ")
}

// ---- forward ---------------------------------------------------------

func TestTemplateClassesHaveCSS(t *testing.T) {
	static, _ := templateClasses(t)
	css := stylesheetClasses()

	for class, files := range static {
		if css[class] {
			continue
		}
		if _, ok := forwardKnownGaps[class]; ok {
			continue
		}
		t.Errorf("template class %q (used in %s) has NO rule in the served stylesheet.\n"+
			"    Fix: add a `.%s { ... }` rule in internal/dashboard/styles_*.go,\n"+
			"    or, if it is intentionally unstyled, add %q to forwardKnownGaps with a reason.",
			class, sortedFiles(files), class, class)
	}

	// The ledger must not rot: an entry that gained a rule, or that no
	// template uses any more, has to be removed.
	for class := range forwardKnownGaps {
		if css[class] {
			t.Errorf("forwardKnownGaps entry %q now HAS a CSS rule — remove it from the allowlist "+
				"(the gap is closed).", class)
		}
		if _, used := static[class]; !used {
			t.Errorf("forwardKnownGaps entry %q is no longer used by any template — remove the stale "+
				"allowlist line.", class)
		}
	}
}

// ---- reverse ---------------------------------------------------------

func TestStylesheetClassesAreUsed(t *testing.T) {
	static, prefixes := templateClasses(t)
	css := stylesheetClasses()

	reachable := func(class string) bool {
		if _, ok := static[class]; ok {
			return true
		}
		for p := range prefixes {
			if len(class) > len(p) && strings.HasPrefix(class, p) {
				return true
			}
		}
		for _, p := range reverseGoPrefixes {
			if len(class) > len(p) && strings.HasPrefix(class, p) {
				return true
			}
		}
		if _, ok := reverseExternalUse[class]; ok {
			return true
		}
		return false
	}

	for class := range css {
		if reachable(class) {
			continue
		}
		if _, ok := reverseKnownOrphans[class]; ok {
			continue
		}
		t.Errorf("stylesheet class %q is DEAD CSS — no template, Go source, or template JS uses it.\n"+
			"    Fix: delete the `.%s` rule from internal/dashboard/styles_*.go,\n"+
			"    or, if it is used somewhere the walker cannot see, add %q to reverseExternalUse "+
			"(with the producer) or reverseKnownOrphans (with a reason).",
			class, class, class)
	}

	// Keep the orphan ledger honest: an entry that became reachable, or
	// whose rule was already deleted, must be removed.
	for class := range reverseKnownOrphans {
		if reachable(class) {
			t.Errorf("reverseKnownOrphans entry %q is reachable again — remove it from the allowlist.", class)
		}
		if !css[class] {
			t.Errorf("reverseKnownOrphans entry %q has no rule any more (cleaned up) — remove the stale "+
				"allowlist line.", class)
		}
	}
	for class := range reverseExternalUse {
		if !css[class] {
			t.Errorf("reverseExternalUse entry %q has no rule any more — remove the stale allowlist line.", class)
		}
	}
}
