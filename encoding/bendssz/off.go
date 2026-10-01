//go:build !bendssz

package bendssz

// Enabled reports whether this binary was built with the bendssz tag.
const Enabled = false

// Unmarshal is the stub of a build without the bendssz tag: it never handles a
// message, so the caller decodes with its own UnmarshalSSZ.
func Unmarshal(msg any, b []byte) (handled bool, err error) { return false, nil }

// Report returns a one-line summary of the counters; empty without the tag.
func Report() string { return "" }
