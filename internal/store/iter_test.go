package store

import (
	"errors"
	"testing"
)

// mockScanner implements rowScanner with configurable failure modes used to
// drive the defensive branches in collectStrings/collectPairs.
type mockScanner struct {
	values     []any // alternating per Scan call
	idx        int
	limit      int
	scanErr    error
	errOnIndex int // when idx == this, Scan returns scanErr
	finalErr   error
	closed     bool
}

func (m *mockScanner) Next() bool {
	if m.idx >= m.limit {
		return false
	}
	return true
}

func (m *mockScanner) Scan(dest ...any) error {
	if m.scanErr != nil && m.idx == m.errOnIndex {
		m.idx++
		return m.scanErr
	}
	// fill dest pointers from values
	for i, d := range dest {
		if m.idx*len(dest)+i >= len(m.values) {
			break
		}
		v := m.values[m.idx*len(dest)+i]
		switch dp := d.(type) {
		case *string:
			*dp = v.(string)
		}
	}
	m.idx++
	return nil
}

func (m *mockScanner) Err() error      { return m.finalErr }
func (m *mockScanner) Close() error    { m.closed = true; return nil }
func (m *mockScanner) wasClosed() bool { return m.closed }

func TestCollectStringsSuccess(t *testing.T) {
	m := &mockScanner{
		values: []any{"a", "b", "c"},
		limit:  3,
	}
	out, err := collectStrings(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 || out[0] != "a" || out[2] != "c" {
		t.Fatalf("out=%v", out)
	}
	if !m.wasClosed() {
		t.Fatal("Close not called")
	}
}

func TestCollectStringsScanError(t *testing.T) {
	m := &mockScanner{
		values:     []any{"a", "b", "c"},
		limit:      3,
		scanErr:    errors.New("scan failed"),
		errOnIndex: 1,
	}
	_, err := collectStrings(m)
	if err == nil {
		t.Fatal("expected error")
	}
	if !m.wasClosed() {
		t.Fatal("Close not called on error")
	}
}

func TestCollectStringsErrError(t *testing.T) {
	m := &mockScanner{
		values:   []any{"a"},
		limit:    1,
		finalErr: errors.New("iteration error"),
	}
	_, err := collectStrings(m)
	if err == nil {
		t.Fatal("expected final error")
	}
}

func TestCollectPairsSuccess(t *testing.T) {
	m := &mockScanner{
		values: []any{"k1", "v1", "k2", "v2"},
		limit:  2,
	}
	out, err := collectPairs(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].First != "k1" || out[1].Second != "v2" {
		t.Fatalf("out=%v", out)
	}
}

func TestCollectPairsScanError(t *testing.T) {
	m := &mockScanner{
		values:     []any{"k1", "v1"},
		limit:      1,
		scanErr:    errors.New("pair scan failed"),
		errOnIndex: 0,
	}
	if _, err := collectPairs(m); err == nil {
		t.Fatal("expected error")
	}
}

func TestCollectPairsErrError(t *testing.T) {
	m := &mockScanner{
		values:   []any{"k", "v"},
		limit:    1,
		finalErr: errors.New("pair iter error"),
	}
	if _, err := collectPairs(m); err == nil {
		t.Fatal("expected error")
	}
}
