package store

// rowScanner is the subset of *sql.Rows that collectStrings needs. Defining
// it as an interface lets unit tests inject a mock that surfaces the
// otherwise-unreachable rows.Scan / rows.Err defensive branches in the
// row-iteration helpers below.
type rowScanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

// scanOne is the Scan-only subset shared by *sql.Row and *sql.Rows. Helpers
// that need to handle either a single-row QueryRow or a streaming Rows
// cursor (e.g. scanCase) accept this minimal contract.
type scanOne interface {
	Scan(dest ...any) error
}

// collectStrings reads a single string column out of a result set. It owns
// rows.Close() so callers don't repeat the defer. All defensive error paths
// (Scan failure, Err failure) are funnelled through this helper so that
// each pattern only needs one test (via mock rowScanner) instead of a
// dozen per-method tests.
func collectStrings(r rowScanner) ([]string, error) {
	defer r.Close()
	var out []string
	for r.Next() {
		var s string
		if err := r.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, r.Err()
}

// collectPairs reads two string columns (e.g. entry_id, tag) into a slice
// of {first, second} pairs. Same defensive-branch consolidation rationale
// as collectStrings.
type stringPair struct {
	First, Second string
}

func collectPairs(r rowScanner) ([]stringPair, error) {
	defer r.Close()
	var out []stringPair
	for r.Next() {
		var p stringPair
		if err := r.Scan(&p.First, &p.Second); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, r.Err()
}

// mapRows iterates a result set, invoking scan(cursor, &t) once per row to
// populate a freshly-zeroed T which is then appended to the output. The
// scan function takes the cursor as a parameter so its Scan call goes
// through the rowScanner interface — tests can pass a mock cursor whose
// Scan returns an error to surface caller-defined defensive branches.
//
// We use generics so this works for *Entry, *EntryHistory, Project, etc.
// without needing one helper per type.
func mapRows[T any](r rowScanner, scan func(s rowScanner, t *T) error) ([]T, error) {
	defer r.Close()
	var out []T
	for r.Next() {
		var t T
		if err := scan(r, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, r.Err()
}
