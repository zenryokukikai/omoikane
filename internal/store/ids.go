package store

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"strings"
)

// typePrefix maps an entry type to its ID prefix.
//   trap → T,  decision → D,  design → X,  lesson → L,  incident → I,
//   note → N,  librarian_meta → M,  external_finding → F.
func typePrefix(t string) string {
	switch EntryType(t) {
	case TypeTrap:
		return "T"
	case TypeDecision:
		return "D"
	case TypeDesign:
		return "X"
	case TypeLesson:
		return "L"
	case TypeIncident:
		return "I"
	case TypeNote:
		return "N"
	case TypeLibrarianMeta:
		return "M"
	case TypeExternalFinding:
		return "F"
	default:
		return "E"
	}
}

// randRead is overridable in tests so the otherwise-impossible
// crypto/rand failure path is exercisable, and the ID-collision retry
// branch can be forced.
var randRead = rand.Read

// newEntryID returns a fresh entry ID of the form <PFX>-<6 base32 chars>.
// Random IDs avoid race conditions a global counter would suffer; collision
// probability is ~1 in 33M for 6 base32 chars and we retry at the SQL layer.
func newEntryID(entryType string) (string, error) {
	var buf [4]byte
	if _, err := randRead(buf[:]); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	enc := strings.ToUpper(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf[:]))
	if len(enc) > 6 {
		enc = enc[:6]
	}
	return typePrefix(entryType) + "-" + enc, nil
}

// newLibrarianID returns a fresh ID of the form <prefix>-<8 hex chars>.
// Despite the historical name it is the package-wide minter for
// non-entry IDs across several domains: librarian instances, chat
// threads/messages, tasks, quartets, findings, spaces, groups, entry
// comments, directives, open-work tasks, and webhooks.
func newLibrarianID(prefix string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return prefix + "-" + hex.EncodeToString(b[:])
}
