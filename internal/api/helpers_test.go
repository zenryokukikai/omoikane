package api

import "encoding/json"

// jsonUnmarshal is a tiny shim so test files don't need to import
// encoding/json directly for the trivial case.
func jsonUnmarshal(raw []byte, into any) error {
	return json.Unmarshal(raw, into)
}
