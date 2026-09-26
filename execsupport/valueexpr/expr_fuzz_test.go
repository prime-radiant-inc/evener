package valueexpr

import (
	"testing"
)

// The scanner's error vocabulary: every syntax error must be one of these
// fixed strings, which is how the no-echo guarantee holds by construction —
// a message that cannot vary cannot interpolate a pasted secret.
var scanErrors = map[string]bool{
	"unterminated ${ in value": true,
	"invalid environment variable name in ${...} reference: must start with a letter or underscore, then only letters, digits, or underscores": true,
	"unterminated $( in value":  true,
	"empty $( command in value": true,
}

// FuzzValueexprScan drives the scanner with arbitrary input: no value may
// panic it, every syntax error must be one of the fixed vocabulary strings
// (so nothing from the value can reach the message), and a value that scans
// clean must scan clean again with the same inventory — the scanner is a
// pure function of the value.
//
// It never calls Expand: a fuzz input can carry arbitrary command text, and
// expanding it would run it.
func FuzzValueexprScan(f *testing.F) {
	f.Add("Bearer $KEY ${OTHER} $$ $(echo hi)")
	f.Add("${sk-live-abc123}")
	f.Add("$(")
	f.Add("$$")
	f.Add("$$(escaped) literal")
	f.Add("${A:-$B} $(a (b) c) $D $")
	f.Add("a\x00b$(c)d")
	f.Add("${:-}")
	f.Add("$ $(a) ${")
	f.Fuzz(func(t *testing.T, value string) {
		first, err := Scan(value)
		if err != nil {
			if !scanErrors[err.Error()] {
				t.Fatalf("error outside the fixed vocabulary: %v", err)
			}
			return
		}
		second, err2 := Scan(value)
		if err2 != nil {
			t.Fatalf("Scan succeeded once and failed once on %q: %v", value, err2)
		}
		if first.Literal != second.Literal || len(first.Refs) != len(second.Refs) || len(first.Commands) != len(second.Commands) {
			t.Fatalf("Scan is not a pure function of %q: %+v vs %+v", value, first, second)
		}
	})
}

// FuzzValueexprTokenExpiry drives the JWT exp parser: any text must parse
// without panicking, and a successful parse always yields a positive epoch —
// a non-positive exp counts as no claim at all.
func FuzzValueexprTokenExpiry(f *testing.F) {
	f.Add("a.eyJhbGciOiJub25lIn0.c2ln")
	f.Add("x.eyJleHAiOjE4MDAwMDAwMDB9.y")
	f.Add("x.eyJleHAiOi0xfQ.y")
	f.Add("x...y")
	f.Add(".")
	f.Add("ey.ey.ey")
	f.Fuzz(func(t *testing.T, value string) {
		exp, ok := tokenExpiry(value)
		if !ok {
			return
		}
		if exp.Unix() <= 0 {
			t.Fatalf("tokenExpiry(%q) = %v; want a positive epoch", value, exp)
		}
	})
}
