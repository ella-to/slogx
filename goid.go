package slogx

import (
	"runtime"
	"strconv"
)

// goid returns the current goroutine's numeric id by parsing the first line
// of runtime.Stack.
//
// Implementation note: Go's standard library deliberately does not expose a
// goroutine id. Parsing runtime.Stack is the supported (if awkward) way to
// obtain one without go:linkname tricks to private runtime symbols.
//
// Cost: a single unformatted stack header read and a small integer parse.
// For slogx this runs once per slogx.Context() call (i.e. once per function
// entry) -- never on the per-log hot path -- so the cost is immaterial.
//
// Returns -1 if parsing fails (should not happen in practice).
func goid() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	// The first line is always: "goroutine N [status]:".
	const prefix = "goroutine "
	if n < len(prefix) {
		return -1
	}
	s := buf[len(prefix):n]
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return -1
	}
	id, err := strconv.ParseInt(string(s[:end]), 10, 64)
	if err != nil {
		return -1
	}
	return id
}
