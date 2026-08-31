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
//
// CSS custom properties (issue #158) get the same treatment further down
// — same defect, one layer lower in the language:
//
//	Forward (TestCustomPropertiesAreDefined): every `var(--x)` in the
//	  stylesheet (or in a template's inline style) must have a `--x:`
//	  declaration. An undefined custom property is not an error in CSS:
//	  the ONE declaration using it is thrown away and the page renders
//	  with the property missing, so the page looks fine to anyone who has
//	  not seen the intended version. That is how eight `var(--bg-soft)`
//	  hover/selected fills shipped doing nothing.
//
//	Reverse (TestCustomPropertiesAreUsed): every `--x:` declaration must
//	  be referenced by some `var(--x)`. A token nobody reads is dead
//	  weight that invites a second, near-duplicate token later.
//
// Neither direction has an allowlist, because neither has an exception:
// a token is defined and used, or it is a defect. Add one only with a
// case that cannot be fixed instead.

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
// Empty is the goal state: every class a template writes now has a rule.
// The five #122 gaps (btn, badge-space, entry-new-form, chat-status-filter,
// cmt-replyto) were closed by giving each its CSS, not by relaxing the
// check. Add a line here only for a class that is INTENTIONALLY unstyled,
// with the reason; anything else is the defect this guard exists to catch.
var forwardKnownGaps = map[string]string{}

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

// ---- custom properties (issue #158) ----------------------------------

var (
	// A use: the token name right after `var(`. Fallback forms
	// (`var(--a, var(--b))`) match twice, once per `var(`, which is what
	// we want — both names have to exist.
	cssVarUseRE = regexp.MustCompile(`var\(\s*(--[A-Za-z0-9_-]+)`)
	// A definition: a `--name:` declaration, i.e. a token name that
	// starts a declaration (after `{`, after `;`, or at line start).
	// Anchoring on those three keeps `var(--x)` — where the name is
	// preceded by `(` and followed by `)` or `,` — from being read as
	// one. Comments are stripped before either regex runs.
	cssVarDefRE = regexp.MustCompile(`(?m)(?:^|[{;])\s*(--[A-Za-z0-9_-]+)\s*:`)
)

// customPropertyUses returns every `var(--x)` name found in the assembled
// stylesheet and in the templates (a template may carry a custom property
// in an inline style=""), mapped to the sources it appears in.
func customPropertyUses(t *testing.T) map[string]map[string]bool {
	t.Helper()
	out := map[string]map[string]bool{}
	add := func(name, src string) {
		if out[name] == nil {
			out[name] = map[string]bool{}
		}
		out[name][src] = true
	}
	for _, m := range cssVarUseRE.FindAllStringSubmatch(strippedStylesheet(), -1) {
		add(m[1], "stylesheet")
	}
	files, err := fs.Glob(templatesFS, "templates/*.html")
	if err != nil {
		t.Fatalf("glob templates: %v", err)
	}
	for _, f := range files {
		b, err := templatesFS.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range cssVarUseRE.FindAllStringSubmatch(string(b), -1) {
			add(m[1], strings.TrimPrefix(f, "templates/"))
		}
	}
	return out
}

// customPropertyDefs returns every custom property DECLARED in the
// stylesheet. Declarations anywhere count, not only :root — a token
// scoped to one selector is still defined for the rules under it.
func customPropertyDefs() map[string]bool {
	out := map[string]bool{}
	for _, m := range cssVarDefRE.FindAllStringSubmatch(strippedStylesheet(), -1) {
		out[m[1]] = true
	}
	return out
}

func strippedStylesheet() string {
	return cssCommentRE.ReplaceAllString(stylesheet, "")
}

// declarationsUsing returns up to `max` occurrences of `var(--name)` as
// "selector { declaration }", so a failure points at the rules that
// silently lost a property rather than just naming the token.
func declarationsUsing(name string, max int) []string {
	css := strippedStylesheet()
	needle := "var(" + name
	oneLine := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	var out []string
	for i := 0; len(out) < max; {
		j := strings.Index(css[i:], needle)
		if j < 0 {
			break
		}
		j += i
		// The declaration: back to the opening brace or previous
		// semicolon, forward to this declaration's terminator.
		declStart := strings.LastIndexAny(css[:j], "{;") + 1
		declEnd := strings.IndexAny(css[j:], ";}")
		if declEnd < 0 {
			declEnd = len(css) - j
		}
		// The selector: the text between the end of the previous rule and
		// the opening brace of this one.
		selEnd := strings.LastIndex(css[:j], "{")
		selStart := strings.LastIndexAny(css[:max2(selEnd, 0)], "}{") + 1
		sel := "?"
		if selEnd > selStart {
			sel = oneLine(css[selStart:selEnd])
		}
		out = append(out, sel+" { "+oneLine(css[declStart:j+declEnd])+" }")
		i = j + len(needle)
	}
	return out
}

func max2(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ---- forward ---------------------------------------------------------

func TestCustomPropertiesAreDefined(t *testing.T) {
	defs := customPropertyDefs()
	if len(defs) == 0 {
		t.Fatal("no `--token:` declarations found at all — the definition scanner is broken, " +
			"not the stylesheet; fix cssVarDefRE before trusting this test.")
	}
	for name, srcs := range customPropertyUses(t) {
		if defs[name] {
			continue
		}
		t.Errorf("custom property %q is USED (in %s) but never defined.\n"+
			"    CSS drops each declaration that reads it, so these render with the property missing:\n"+
			"      %s\n"+
			"    Fix: define %q in the :root block of internal/dashboard/styles_base.go,\n"+
			"    or point the uses at the existing token that carries the intended meaning.",
			name, sortedFiles(srcs), strings.Join(declarationsUsing(name, 3), "\n      "), name)
	}
}

// ---- reverse ---------------------------------------------------------

func TestCustomPropertiesAreUsed(t *testing.T) {
	uses := customPropertyUses(t)
	defs := customPropertyDefs()
	if len(uses) == 0 {
		t.Fatal("no `var(--token)` uses found at all — the use scanner is broken, not the " +
			"stylesheet; fix cssVarUseRE before trusting this test.")
	}
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if len(uses[name]) > 0 {
			continue
		}
		t.Errorf("custom property %q is DEFINED but never read by any var(%s).\n"+
			"    Fix: delete the declaration from internal/dashboard/styles_*.go, or use it —\n"+
			"    an unread token is what makes someone invent a second one beside it.",
			name, name)
	}
}
