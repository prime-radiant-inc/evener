package dev

import (
	"strings"
	"testing"
)

// FuzzParseGoTestJSON drives the parser over arbitrary `go test -json` text. The
// oracle is floor "no panic" plus, for streams that parse cleanly: every
// expected package has a PKG row (the completeness contract), and the parse is
// deterministic. A non-nil error must therefore be the completeness refusal and
// nothing else, so an unexpected failure names its own cause rather than hiding
// behind "no panic". This is the fuzz target the static gap gate requires of a
// package that parses (`go test -json`) with encoding/json, mirroring
// cmd/evener-fuzzregistry's FuzzParseRegistry.
func FuzzParseGoTestJSON(f *testing.F) {
	f.Add(`{"Action":"pass","Package":"example.com/mod","Elapsed":0.45}`)
	f.Add(`{"Action":"pass","Package":"example.com/mod","Test":"TestA","Elapsed":0.3}`)
	f.Add(`{"Action":"start","Package":"example.com/mod"}`)
	f.Add(`{"Action":"skip","Package":"example.com/empty"}`)
	f.Add("{not json}\n")
	f.Add("")
	f.Add(`{"Action":"pass","Package":"example.com/mod","Test":"a/b/c","Elapsed":1e300}` + "\n" +
		`{"Action":"pass","Package":"example.com/mod","Elapsed":-1}`)
	f.Add(`{"Action":"pass","Package":"example.com/mod","Elapsed":"not-a-number"}`)

	f.Fuzz(func(t *testing.T, stream string) {
		expected := []string{"example.com/mod"}
		rows, err := parseGoTestJSON(strings.NewReader(stream), expected)
		if err != nil {
			if !strings.Contains(err.Error(), "produced no terminal event") {
				t.Fatalf("parse failed for a reason other than a missing terminal event: %v", err)
			}
			return
		}
		if !rowsContain(rows, "PKG\texample.com/mod") {
			t.Fatalf("clean parse has no PKG row for the expected package; rows:\n%s", strings.Join(rows, "\n"))
		}
		again, err := parseGoTestJSON(strings.NewReader(stream), expected)
		if err != nil {
			t.Fatalf("second parse of identical input errored: %v", err)
		}
		if strings.Join(rows, "\n") != strings.Join(again, "\n") {
			t.Fatalf("parse is non-deterministic:\nfirst:  %v\nsecond: %v", rows, again)
		}
	})
}
