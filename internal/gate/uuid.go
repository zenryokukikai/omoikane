package gate

// UUIDv7 generation for new admin-plane ids (spec §3.2: instance and
// binding PUT paths are UUIDv7). Implemented locally per RFC 9562 §5.7
// rather than adding a dependency — the repo has no uuid module and the
// admin plane only needs generation + canonical-form checks.

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// NewUUIDv7 returns a canonical lowercase UUIDv7 (RFC 9562): 48-bit
// Unix-millisecond timestamp, version 7, IETF variant, 74 random bits.
func NewUUIDv7() string {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	// crypto/rand.Read never returns a partial read without an error;
	// an error here means the OS entropy source is broken, which is not
	// recoverable — panic loudly rather than mint a predictable id.
	if _, err := rand.Read(b[6:]); err != nil {
		panic("gate: crypto/rand failed: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x70 // version 7
	b[8] = (b[8] & 0x3f) | 0x80 // IETF variant (10xx)

	var s [36]byte
	hex.Encode(s[0:8], b[0:4])
	s[8] = '-'
	hex.Encode(s[9:13], b[4:6])
	s[13] = '-'
	hex.Encode(s[14:18], b[6:8])
	s[18] = '-'
	hex.Encode(s[19:23], b[8:10])
	s[23] = '-'
	hex.Encode(s[24:36], b[10:16])
	return string(s[:])
}

// isUUIDv7 reports a canonical lowercase UUID whose version nibble is 7
// and whose variant is the IETF 10xx form.
func isUUIDv7(s string) bool {
	if !isCanonicalUUID(s) {
		return false
	}
	if s[14] != '7' {
		return false
	}
	switch s[19] {
	case '8', '9', 'a', 'b':
		return true
	}
	return false
}
