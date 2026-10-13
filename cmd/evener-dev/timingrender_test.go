package dev

import (
	"strings"
	"testing"
)

// TestRenderGoTestJSONPrintsOutputEvents pins the render transform: every
// Output event's text is written in stream order, and the non-output events a
// `go test -json` stream also carries (run/pass/start) contribute nothing. The
// rendered text is the human log the gate keeps for its failure dump, so a run
// that captured durations from -json leaves the same diagnostics a plain
// `go test` run would (issue #144).
func TestRenderGoTestJSONPrintsOutputEvents(t *testing.T) {
	stream := strings.Join([]string{
		`{"Action":"start","Package":"example.com/mod"}`,
		`{"Action":"run","Package":"example.com/mod","Test":"TestOne"}`,
		`{"Action":"output","Package":"example.com/mod","Test":"TestOne","Output":"=== RUN   TestOne\n"}`,
		`{"Action":"output","Package":"example.com/mod","Test":"TestOne","Output":"    x_test.go:5: hello\n"}`,
		`{"Action":"output","Package":"example.com/mod","Test":"TestOne","Output":"--- PASS: TestOne (0.01s)\n"}`,
		`{"Action":"pass","Package":"example.com/mod","Test":"TestOne","Elapsed":0.01}`,
		`{"Action":"output","Package":"example.com/mod","Output":"ok  \texample.com/mod\t0.01s\n"}`,
		`{"Action":"pass","Package":"example.com/mod","Elapsed":0.01}`,
	}, "\n") + "\n"

	var got strings.Builder
	if err := renderGoTestJSON(strings.NewReader(stream), &got); err != nil {
		t.Fatalf("renderGoTestJSON: %v", err)
	}
	want := "=== RUN   TestOne\n" +
		"    x_test.go:5: hello\n" +
		"--- PASS: TestOne (0.01s)\n" +
		"ok  \texample.com/mod\t0.01s\n"
	if got.String() != want {
		t.Fatalf("rendered log =\n%q\nwant\n%q", got.String(), want)
	}
}

// TestRenderGoTestJSONPrintsBuildOutputEvents pins issue #144 review finding 1:
// the pinned toolchain reports a package's compiler diagnostics as
// `build-output` events, which carry an ImportPath rather than a Package and
// which `go test` writes nowhere else — stderr stays empty for them. A
// timing-enabled gate captures the `-json` stream in place of the console, so
// those events are the only copy of the compiler error; a renderer that reads
// only `output` turns a build failure into a bare `[build failed]` line with no
// explanation.
func TestRenderGoTestJSONPrintsBuildOutputEvents(t *testing.T) {
	stream := strings.Join([]string{
		`{"ImportPath":"example.com/mod [example.com/mod.test]","Action":"build-output","Output":"# example.com/mod [example.com/mod.test]\n"}`,
		`{"ImportPath":"example.com/mod [example.com/mod.test]","Action":"build-output","Output":"./broken_test.go:6:6: undefined: undefinedSymbol\n"}`,
		`{"ImportPath":"example.com/mod [example.com/mod.test]","Action":"build-fail"}`,
		`{"Time":"2026-01-01T00:00:00Z","Action":"output","Package":"example.com/mod","Output":"FAIL\texample.com/mod [build failed]\n"}`,
	}, "\n") + "\n"

	var got strings.Builder
	if err := renderGoTestJSON(strings.NewReader(stream), &got); err != nil {
		t.Fatalf("renderGoTestJSON: %v", err)
	}
	want := "# example.com/mod [example.com/mod.test]\n" +
		"./broken_test.go:6:6: undefined: undefinedSymbol\n" +
		"FAIL\texample.com/mod [build failed]\n"
	if got.String() != want {
		t.Fatalf("rendered build-failure log =\n%q\nwant\n%q", got.String(), want)
	}
}

// TestRenderGoTestJSONSkipsMalformedLines pins that one unreadable line costs
// one log line, not the whole log: a truncated stream still renders the events
// around it, because a lost line is no worse than a malformed one and the run
// still needs its log.
func TestRenderGoTestJSONSkipsMalformedLines(t *testing.T) {
	stream := `{"Action":"output","Package":"p","Output":"kept-before\n"}` + "\n" +
		`{not json` + "\n" +
		`{"Action":"output","Package":"p","Output":"kept-after\n"}` + "\n"

	var got strings.Builder
	if err := renderGoTestJSON(strings.NewReader(stream), &got); err != nil {
		t.Fatalf("renderGoTestJSON: %v", err)
	}
	if got.String() != "kept-before\nkept-after\n" {
		t.Fatalf("rendered log = %q, want the two readable lines", got.String())
	}
}

// TestRunTimingRenderRequiresJSONPath pins the usage refusal: with no --json
// there is nothing to render, so it exits 2 rather than printing an empty log
// that would look like a green run with no output.
func TestRunTimingRenderRequiresJSONPath(t *testing.T) {
	var out, errOut strings.Builder
	if code := runTimingRender(nil, &out, &errOut); code != 2 {
		t.Fatalf("runTimingRender with no --json = %d, want 2", code)
	}
}
