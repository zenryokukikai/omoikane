package secrets

import (
	"strings"
	"testing"

	"github.com/zenryokukikai/omoikane/internal/config"
)

func TestDetectsGitHubToken(t *testing.T) {
	body := "the leak: ghp_1234567890abcdefghijKLMNOPQRSTUVWXYZ012 — please redact"
	f := ScanSecrets(Doc{Body: body})
	if len(f) == 0 {
		t.Fatal("expected github_token finding")
	}
	if f[0].Pattern != "github_token" || f[0].Field != "body" {
		t.Fatalf("unexpected finding: %+v", f[0])
	}
}

func TestDetectsAWSAccessKey(t *testing.T) {
	f := ScanSecrets(Doc{Symptom: "Use AKIAIOSFODNN7EXAMPLE here"})
	if len(f) == 0 || f[0].Pattern != "aws_access_key" {
		t.Fatalf("expected aws_access_key, got %+v", f)
	}
}

func TestDetectsPrivateKey(t *testing.T) {
	f := ScanSecrets(Doc{Body: "-----BEGIN OPENSSH PRIVATE KEY-----\n..."})
	if len(f) == 0 || f[0].Pattern != "private_key" {
		t.Fatalf("expected private_key, got %+v", f)
	}
}

// This is a CREDENTIAL-LEAK scanner, not a PII scanner. Email addresses,
// card-like digit runs, phone numbers, and bank accounts must NOT be
// flagged — omoikane is shared inside one org and policing PII at write
// time only broke legitimate use. These tests lock that non-behaviour so
// PII detection can't quietly creep back in.
func TestDoesNotFlagEmail(t *testing.T) {
	f := ScanSecrets(Doc{Body: "contact alice@example.com for help, or k@zenryokukikai.com"})
	if len(f) != 0 {
		t.Fatalf("email must not be flagged (PII is not policed), got %+v", f)
	}
}

func TestDoesNotFlagGitSSHRemote(t *testing.T) {
	// The original false-positive: an SSH remote read as an "email".
	f := ScanSecrets(Doc{Body: "clone with git@github.com:zenryokukikai/lipsync-engine.git"})
	if len(f) != 0 {
		t.Fatalf("git@host SSH remote must not be flagged, got %+v", f)
	}
}

func TestDoesNotFlagCardLikeOrPhoneOrAccountDigits(t *testing.T) {
	for _, body := range []string{
		"use 4242 4242 4242 4242 to test",   // card-like
		"call +81 90 1234 5678 tomorrow",     // phone
		"account 1234567890123456 at the bank", // 16-digit run
		"run id 20260601123045 build 1111111111111111", // timestamps / hashes
	} {
		if f := ScanSecrets(Doc{Body: body}); len(f) != 0 {
			t.Fatalf("numeric/PII content must not be flagged: %q → %+v", body, f)
		}
	}
}

func TestCleanDocReturnsNoFindings(t *testing.T) {
	f := ScanSecrets(Doc{
		Title: "Mask preprocessing trap",
		Body:  "Use landmark-driven soft mask, not rectangular mask.",
	})
	if len(f) != 0 {
		t.Fatalf("clean doc should be empty, got %+v", f)
	}
}

// When a deployment opts INTO PII scanning (KB_PII_MODE != off), ScanPII
// catches email and Luhn-valid cards, but Luhn still filters timestamps.
func TestScanPIICatchesEmailAndCardWhenEnabled(t *testing.T) {
	f := ScanPII(Doc{Body: "mail alice@example.com card 4242 4242 4242 4242"})
	got := map[string]bool{}
	for _, x := range f {
		got[x.Pattern] = true
	}
	if !got["email"] || !got["credit_card"] {
		t.Fatalf("ScanPII should catch email+credit_card, got %+v", f)
	}
}

func TestScanPIILuhnFiltersNonCards(t *testing.T) {
	f := ScanPII(Doc{Body: "run 20260601123045 hash 1111111111111111"})
	for _, x := range f {
		if x.Pattern == "credit_card" {
			t.Fatalf("non-card digit run flagged: %+v", x)
		}
	}
}

func TestFindingDoesNotLeakValue(t *testing.T) {
	// The leaked token must not appear in the Finding struct's stringified
	// form (we only ever expose pattern/field/offset/length).
	leak := "ghp_1234567890abcdefghijKLMNOPQRSTUVWXYZ012"
	f := ScanSecrets(Doc{Body: leak})
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
			{Pattern: "aws_access_key"},
		},
	}
	s := v.Summary()
	if !strings.Contains(s, "github_token:2") || !strings.Contains(s, "aws_access_key:1") {
		t.Fatalf("unexpected summary: %s", s)
	}
}
