// Package enrich extracts structured metadata from entries at write time.
// Phase 1 extracts only tags (heuristic + provider stub). The interface is
// shaped so future phases can add symptoms/triggers/scope/relations etc.
// without breaking the API.
package enrich

import (
	"context"
	"log/slog"
	"strings"
	"unicode"
)

// CurrentVersion is the active enrichment-pipeline generation. Phase 2 = 2
// because the contract now includes symptoms + triggers + prohibited
// patterns + scope. Bumping the version makes the maintenance job (Phase
// 5) able to detect Phase-1-era entries that need re-enrichment.
const CurrentVersion = 2

// Trigger is one extracted trigger phrase with optional domain. Mirrors
// store.IndexedTrigger but kept here to avoid the store package depending
// on enrich.
type Trigger struct {
	Phrase string
	Domain string // preprocessing|training|inference|data|infra|other|"" (any)
}

type Result struct {
	Version            int
	Source             string // "llm" / "heuristic" / provider name
	Tags               []string
	Symptoms           []string
	Triggers           []Trigger
	ProhibitedPatterns []string
	Scope              map[string][]string
}

type Input struct {
	Type                string
	Title               string
	Body                string
	Symptom             string
	RootCause           string
	Resolution          string
	Prohibited          string
	AttemptedApproaches string
	ObservedBehavior    string
	Hypotheses          string
}

type Enricher interface {
	Enrich(ctx context.Context, in Input) (Result, error)
	Name() string
}

// New returns the configured enricher. In Phase 1 only the heuristic is
// wired up; provider keys exist to keep the configuration surface stable
// for later phases.
func New(providerName, model, apiKey, endpoint string, logger *slog.Logger) Enricher {
	if logger == nil {
		logger = slog.Default()
	}
	switch strings.ToLower(providerName) {
	case "", "none", "off", "heuristic":
		return heuristic{logger: logger}
	default:
		logger.Warn("unknown KB_LLM_PROVIDER, falling back to heuristic",
			"provider", providerName)
		return heuristic{logger: logger}
	}
}

// ---- heuristic implementation ----

type heuristic struct {
	logger *slog.Logger
}

func (h heuristic) Name() string { return "heuristic" }

func (h heuristic) Enrich(_ context.Context, in Input) (Result, error) {
	tags := heuristicTags(in)
	symptoms := extractSymptomPhrases(in)
	triggers := extractTriggerPhrases(in)
	scope := extractScope(in)
	prohibited := extractProhibitedPatterns(in)

	return Result{
		Version:            CurrentVersion,
		Source:             "heuristic",
		Tags:               tags,
		Symptoms:           symptoms,
		Triggers:           triggers,
		ProhibitedPatterns: prohibited,
		Scope:              scope,
	}, nil
}

// heuristicTags is the Phase-1 keyword-frequency tag extractor.
func heuristicTags(in Input) []string {
	text := strings.ToLower(in.Title + " " + in.Symptom + " " + in.Body)
	freq := map[string]int{}
	order := map[string]int{}
	pos := 0
	for _, tok := range tokenise(text) {
		if isStopWord(tok) {
			continue
		}
		if _, ok := order[tok]; !ok {
			order[tok] = pos
			pos++
		}
		freq[tok]++
	}
	type kv struct {
		tag   string
		count int
		first int
	}
	var kvs []kv
	for tag, n := range freq {
		kvs = append(kvs, kv{tag, n, order[tag]})
	}
	for i := 1; i < len(kvs); i++ {
		j := i
		for j > 0 && less(kvs[j], kvs[j-1]) {
			kvs[j], kvs[j-1] = kvs[j-1], kvs[j]
			j--
		}
	}
	out := make([]string, 0, 8)
	for i := 0; i < len(kvs) && len(out) < 8; i++ {
		out = append(out, kvs[i].tag)
	}
	return out
}

// extractSymptomPhrases splits the symptom / observed_behavior fields into
// sentence-ish phrases. The heuristic is intentionally simple: each
// non-empty trimmed line / period-delimited fragment becomes one symptom.
// The real LLM provider does this much better.
func extractSymptomPhrases(in Input) []string {
	candidates := splitToPhrases(in.Symptom + ". " + in.ObservedBehavior)
	out := make([]string, 0, len(candidates))
	seen := map[string]bool{}
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		// Symptoms must be informative — drop ultra-short fragments.
		if len(c) < 8 {
			continue
		}
		key := strings.ToLower(c)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
		if len(out) >= 6 {
			break
		}
	}
	return out
}

// triggerVerbs is a tiny vocabulary used to find "do X to Y"-style
// fragments in the body. The heuristic is a stopgap; an LLM provider can
// replace it.
var triggerVerbs = map[string]string{
	// verb → suggested domain (empty = unspecified)
	"modify":    "",
	"change":    "",
	"add":       "",
	"remove":    "",
	"update":    "",
	"tune":      "training",
	"adjust":    "training",
	"preprocess": "preprocessing",
	"infer":     "inference",
	"deploy":    "infra",
	"migrate":   "infra",
}

func extractTriggerPhrases(in Input) []Trigger {
	body := strings.ToLower(in.Title + " " + in.Resolution + " " + in.Prohibited + " " + in.Body)
	out := []Trigger{}
	seen := map[string]bool{}
	for verb, domain := range triggerVerbs {
		idx := 0
		for idx < len(body) {
			i := strings.Index(body[idx:], verb)
			if i < 0 {
				break
			}
			start := idx + i
			end := start + len(verb)
			// Capture the next 1-4 words as the object.
			rest := body[end:]
			object := captureNextWords(rest, 4)
			phrase := strings.TrimSpace(verb + " " + object)
			if phrase != "" && !seen[phrase] {
				seen[phrase] = true
				out = append(out, Trigger{Phrase: phrase, Domain: detectDomain(body, domain)})
				if len(out) >= 6 {
					return out
				}
			}
			idx = end
		}
	}
	return out
}

// captureNextWords returns up to n word-shaped tokens from s, joined with
// spaces.
func captureNextWords(s string, n int) string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		if r == ' ' || r == '\t' || r == '\n' {
			return true
		}
		// keep alphanumerics + hyphen, treat everything else as a separator
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-':
			return false
		}
		return true
	})
	if len(fields) > n {
		fields = fields[:n]
	}
	return strings.Join(fields, " ")
}

// detectDomain looks for explicit domain hints in the surrounding text.
// We split into tokens and check word-boundary matches so e.g.
// "datacenter" doesn't pollute the "data" bucket. The order of `domains`
// is "most specific first" so multi-word concepts win when present.
func detectDomain(text, hint string) string {
	if hint != "" {
		return hint
	}
	domains := []string{"preprocessing", "training", "inference", "infra", "data"}
	tokens := strings.FieldsFunc(text, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '-':
			return false
		}
		return true
	})
	tokenSet := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		tokenSet[t] = true
	}
	for _, d := range domains {
		if tokenSet[d] {
			return d
		}
	}
	return ""
}

// extractScope looks for explicit framework / hardware mentions in scope-y
// fields. Phase 1 only.
func extractScope(in Input) map[string][]string {
	text := strings.ToLower(in.Body + " " + in.Symptom + " " + in.ObservedBehavior)
	out := map[string][]string{}
	for _, fw := range []string{"pytorch", "tensorflow", "jax", "onnx", "tensorrt"} {
		if strings.Contains(text, fw) {
			out["frameworks"] = append(out["frameworks"], fw)
		}
	}
	for _, gpu := range []string{"h100", "a100", "v100", "rtx", "mi300", "tpu"} {
		if strings.Contains(text, gpu) {
			out["gpus"] = append(out["gpus"], strings.ToUpper(gpu))
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// extractProhibitedPatterns lifts each "DO NOT" / "禁止" / "MUST NOT" line
// out of the `prohibited` field as a separate pattern. Useful for the
// lookup_by_trigger pre-flight check.
func extractProhibitedPatterns(in Input) []string {
	if in.Prohibited == "" {
		return nil
	}
	lines := strings.Split(in.Prohibited, "\n")
	out := make([]string, 0, len(lines))
	seen := map[string]bool{}
	for _, l := range lines {
		l = strings.TrimSpace(l)
		l = strings.TrimPrefix(l, "- ")
		l = strings.TrimPrefix(l, "* ")
		if len(l) < 4 {
			continue
		}
		if seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

// splitToPhrases breaks on sentence-ending punctuation and newlines.
func splitToPhrases(s string) []string {
	if s == "" {
		return nil
	}
	out := []string{}
	cur := strings.Builder{}
	for _, r := range s {
		if r == '.' || r == '\n' || r == '。' || r == '!' || r == '?' {
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteRune(r)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func less(a, b struct {
	tag   string
	count int
	first int
}) bool {
	if a.count != b.count {
		return a.count > b.count
	}
	return a.first < b.first
}

func tokenise(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) < 3 || len(f) > 30 {
			continue
		}
		out = append(out, f)
	}
	return out
}

var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "this": true,
	"that": true, "from": true, "have": true, "has": true, "was": true,
	"were": true, "are": true, "but": true, "not": true, "you": true,
	"your": true, "our": true, "all": true, "any": true, "can": true,
	"its": true, "their": true, "they": true, "them": true, "those": true,
	"these": true, "into": true, "onto": true, "out": true, "over": true,
	"under": true, "between": true, "than": true, "then": true,
	"because": true, "before": true, "after": true, "while": true,
	"about": true, "above": true, "below": true, "should": true,
	"would": true, "could": true, "will": true, "shall": true, "may": true,
	"might": true, "just": true, "only": true, "also": true, "such": true,
	"some": true, "more": true, "most": true, "many": true, "few": true,
	"each": true, "every": true, "without": true, "within": true,
	"during": true, "since": true, "until": true, "upon": true,
	"http": true, "https": true, "www": true, "com": true, "org": true,
	"used": true, "use": true, "using": true, "via": true,
}

func isStopWord(s string) bool { return stopWords[s] }
