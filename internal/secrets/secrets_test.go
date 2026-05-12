package secrets

import (
	"strings"
	"testing"

	"github.com/kojira/omoikane/internal/config"
)

func TestDetectsGitHubToken(t *testing.T) {
	body := "the leak: ghp_1234567890abcdefghijKLMNOPQRSTUVWXYZ012 — please redact"
	f := Scan(Doc{Body: body})
	if len(f) == 0 {
		t.Fatal("expected github_token finding")
	}
	if f[0].Pattern != "github_token" || f[0].Field != "body" {
		t.Fatalf("unexpected finding: %+v", f[0])
	}
}

func TestDetectsAWSAccessKey(t *testing.T) {
	f := Scan(Doc{Symptom: "Use AKIAIOSFODNN7EXAMPLE here"})
	if len(f) == 0 || f[0].Pattern != "aws_access_key" {
		t.Fatalf("expected aws_access_key, got %+v", f)
	}
}

func TestDetectsPrivateKey(t *testing.T) {
	f := Scan(Doc{Body: "-----BEGIN OPENSSH PRIVATE KEY-----\n..."})
	if len(f) == 0 || f[0].Pattern != "private_key" {
		t.Fatalf("expected private_key, got %+v", f)
	}
}

func TestDetectsEmail(t *testing.T) {
	f := Scan(Doc{Body: "contact alice@example.com for help"})
	found := false
	for _, x := range f {
		if x.Pattern == "email" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected email finding, got %+v", f)
	}
}

func TestDetectsLuhnValidCard(t *testing.T) {
	// 4242 4242 4242 4242 — Stripe test card, Luhn valid
	f := Scan(Doc{Body: "use 4242 4242 4242 4242 to test"})
	found := false
	for _, x := range f {
		if x.Pattern == "credit_card" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected credit_card, got %+v", f)
	}
}

func TestRejectsTrivialNumberRuns(t *testing.T) {
	// 16 ones — passes the regex but fails the Luhn / all-same filter
	f := Scan(Doc{Body: "build hash 1111111111111111"})
	for _, x := range f {
		if x.Pattern == "credit_card" {
			t.Fatalf("trivial run should not match credit_card: %+v", x)
		}
	}
}

func TestCleanDocReturnsNoFindings(t *testing.T) {
	f := Scan(Doc{
		Title: "Mask preprocessing trap",
		Body:  "Use landmark-driven soft mask, not rectangular mask.",
	})
	if len(f) != 0 {
		t.Fatalf("clean doc should be empty, got %+v", f)
	}
}

func TestFindingDoesNotLeakValue(t *testing.T) {
	// The leaked token must not appear in the Finding struct's stringified
	// form (we only ever expose pattern/field/offset/length).
	leak := "ghp_1234567890abcdefghijKLMNOPQRSTUVWXYZ012"
	f := Scan(Doc{Body: leak})
	if len(f) == 0 {
		t.Fatal("nothing detected")
	}
	for _, x := range f {
		s := x.Pattern + x.Field
		if strings.Contains(s, leak) {
			t.Fatalf("finding leaks value: %+v", x)
		}
	}
}

func TestVerdictRejectInEnforceOnly(t *testing.T) {
	v := Verdict{
		Findings: []Finding{{Pattern: "github_token", Field: "body", Offset: 0, Length: 4}},
		Mode:     config.SecretsEnforce,
	}
	if !v.Reject() {
		t.Fatal("enforce mode + findings should reject")
	}
	v.Mode = config.SecretsWarn
	if v.Reject() {
		t.Fatal("warn mode should not reject")
	}
	v.Mode = config.SecretsOff
	if v.Reject() {
		t.Fatal("off mode should not reject")
	}
}

func TestLooksLikeCardEdgeCases(t *testing.T) {
	// Too short (less than 13 digits)
	if looksLikeCard("123") {
		t.Fatal("too short should not match")
	}
	// Too long (more than 19 digits)
	if looksLikeCard("12345678901234567890") {
		t.Fatal("too long should not match")
	}
	// Length 16, all-same digit — caught by allSame filter
	if looksLikeCard("2222222222222222") {
		t.Fatal("all-same should not match")
	}
	// Length 16, Luhn-invalid
	if looksLikeCard("1234567890123456") {
		t.Fatal("Luhn-invalid should not match")
	}
}

func TestEmptySummary(t *testing.T) {
	v := Verdict{}
	if v.Summary() != "" {
		t.Fatalf("empty summary: %q", v.Summary())
	}
}

func TestItoaZero(t *testing.T) {
	if got := itoa(0); got != "0" {
		t.Fatalf("itoa(0)=%q", got)
	}
	// itoa is internal and only called with positives in practice; we
	// exercise the negative branch directly for full coverage of the
	// defensive code that handles it.
	if got := itoa(-5); got != "-5" {
		t.Fatalf("itoa(-5)=%q", got)
	}
}

func TestVerdictSummary(t *testing.T) {
	v := Verdict{
		Findings: []Finding{
			{Pattern: "github_token"},
			{Pattern: "github_token"},
			{Pattern: "email"},
		},
	}
	s := v.Summary()
	if !strings.Contains(s, "github_token:2") || !strings.Contains(s, "email:1") {
		t.Fatalf("unexpected summary: %s", s)
	}
}
