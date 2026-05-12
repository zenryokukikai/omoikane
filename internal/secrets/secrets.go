// Package secrets implements the write-time secret/PII scanner described in
// docs/design.md §12.3 and the SECRETS_DETECTED error in error-codes.md.
//
// The scanner runs on every write to entries (POST + PATCH). It examines a
// fixed set of fields, applies a curated set of regex patterns, and returns
// findings. Importantly, findings only report pattern name + position —
// **never the matched value** — so an error response can be logged without
// re-leaking the secret.
package secrets

import (
	"regexp"
	"strings"

	"github.com/kojira/omoikane/internal/config"
)

// Finding identifies one detected secret/PII occurrence.
type Finding struct {
	Pattern string `json:"pattern"`
	Field   string `json:"field"`
	Offset  int    `json:"offset"`
	Length  int    `json:"length"`
}

// Doc is the subset of an entry the scanner inspects. The API layer fills it
// before calling Scan().
type Doc struct {
	Title               string
	Body                string
	Symptom             string
	RootCause           string
	Resolution          string
	Prohibited          string
	AttemptedApproaches string
	ObservedBehavior    string
	Hypotheses          string
	Metadata            string // raw JSON text
}

type pattern struct {
	name string
	re   *regexp.Regexp
}

// patterns is a small, audited list. Order does not matter — every pattern
// scans every field. Each `name` is the value reported in `findings[].pattern`.
var patterns = []pattern{
	{"aws_access_key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"aws_secret_key_assignment", regexp.MustCompile(`(?i)(aws[_-]?secret[_-]?access[_-]?key)\s*[:=]\s*["']?([A-Za-z0-9/+=]{40})["']?`)},
	{"github_token", regexp.MustCompile(`\b(ghp|gho|ghs|ghr|github_pat)_[A-Za-z0-9_]{20,}\b`)},
	{"slack_token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)},
	{"private_key", regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH |PGP )?PRIVATE KEY-----`)},
	// Generic api-key-ish assignment. Require >= 20 chars after the operator,
	// excluding short config values like `key: abc`.
	{"generic_api_key", regexp.MustCompile(`(?i)(api[_-]?key|secret|access[_-]?token|auth[_-]?token)\s*[:=]\s*["']?([A-Za-z0-9_\-\.]{20,})["']?`)},
	{"email", regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)},
	// Credit card: 13-19 digit run, must pass Luhn (checked in fnLuhn below).
	{"credit_card_candidate", regexp.MustCompile(`\b(?:\d[ -]?){13,19}\b`)},
}

// Scan runs all patterns against all fields and returns findings.
// Empty findings = clean. The caller maps non-empty results to either:
//
//   - SecretsEnforce → 422 SECRETS_DETECTED (write rejected)
//   - SecretsWarn    → write proceeds, findings logged
//   - SecretsOff     → never call Scan
func Scan(d Doc) []Finding {
	type fielded struct {
		name string
		text string
	}
	fields := []fielded{
		{"title", d.Title},
		{"body", d.Body},
		{"symptom", d.Symptom},
		{"root_cause", d.RootCause},
		{"resolution", d.Resolution},
		{"prohibited", d.Prohibited},
		{"attempted_approaches", d.AttemptedApproaches},
		{"observed_behavior", d.ObservedBehavior},
		{"hypotheses", d.Hypotheses},
		{"metadata", d.Metadata},
	}
	var out []Finding
	for _, f := range fields {
		if f.text == "" {
			continue
		}
		for _, p := range patterns {
			locs := p.re.FindAllStringIndex(f.text, -1)
			for _, loc := range locs {
				if p.name == "credit_card_candidate" {
					raw := f.text[loc[0]:loc[1]]
					if !looksLikeCard(raw) {
						continue
					}
					out = append(out, Finding{
						Pattern: "credit_card", Field: f.name,
						Offset: loc[0], Length: loc[1] - loc[0],
					})
					continue
				}
				out = append(out, Finding{
					Pattern: p.name, Field: f.name,
					Offset: loc[0], Length: loc[1] - loc[0],
				})
			}
		}
	}
	return out
}

// looksLikeCard runs Luhn on the digits-only form. Rejects numbers that are
// obviously not cards (all same digit, sequential, etc.) to cut false-positives
// from things like log timestamps or version strings.
func looksLikeCard(raw string) bool {
	var digits []byte
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c >= '0' && c <= '9' {
			digits = append(digits, c)
		}
	}
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	// Reject trivially repeating digits.
	allSame := true
	for i := 1; i < len(digits); i++ {
		if digits[i] != digits[0] {
			allSame = false
			break
		}
	}
	if allSame {
		return false
	}
	// Luhn checksum.
	sum := 0
	alt := false
	for i := len(digits) - 1; i >= 0; i-- {
		n := int(digits[i] - '0')
		if alt {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		alt = !alt
	}
	return sum%10 == 0
}

// Verdict bundles the scan result with the configured mode so callers can
// make a single decision call instead of duplicating policy.
type Verdict struct {
	Findings []Finding
	Mode     config.SecretsMode
}

// Reject reports whether the API layer must refuse the write with HTTP 422.
func (v Verdict) Reject() bool {
	return len(v.Findings) > 0 && v.Mode == config.SecretsEnforce
}

// Summary is a compact one-line description suitable for the audit log,
// without leaking any matched values.
func (v Verdict) Summary() string {
	if len(v.Findings) == 0 {
		return ""
	}
	patternCount := map[string]int{}
	for _, f := range v.Findings {
		patternCount[f.Pattern]++
	}
	var parts []string
	for p, n := range patternCount {
		parts = append(parts, p+":"+itoa(n))
	}
	return strings.Join(parts, ",")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
