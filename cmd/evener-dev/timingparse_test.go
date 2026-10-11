package dev

import (
	"slices"
	"strings"
	"testing"
)

// rowsContain reports whether any row equals want exactly.
func rowsContain(rows []string, want string) bool {
	return slices.Contains(rows, want)
}

// TestParseGoTestJSONPrefersPackageWallTime pins issue #172's last live defect:
// the per-package SUM must be the package's own wall time, read from the
// package-level terminal event, not the sum of top-level test Elapsed. Under
// t.Parallel those tests overlap, so summing their Elapsed counts the same
// wall-clock second more than once. Two 0.3s parallel tests plus a 0.05s subtest
// sum to 0.65s while the package terminal event reports 0.45s; the SUM must be
// 0.45s.
func TestParseGoTestJSONPrefersPackageWallTime(t *testing.T) {
	stream := strings.Join([]string{
		`{"Action":"run","Package":"example.com/mod","Test":"TestParallelA"}`,
		`{"Action":"pass","Package":"example.com/mod","Test":"TestParallelA","Elapsed":0.3}`,
		`{"Action":"pass","Package":"example.com/mod","Test":"TestParallelB","Elapsed":0.3}`,
		`{"Action":"pass","Package":"example.com/mod","Test":"TestChild/child","Elapsed":0.05}`,
		`{"Action":"pass","Package":"example.com/mod","Elapsed":0.45}`,
	}, "\n") + "\n"

	rows, err := parseGoTestJSON(strings.NewReader(stream), []string{"example.com/mod"})
	if err != nil {
		t.Fatalf("parseGoTestJSON: %v", err)
	}
	if !rowsContain(rows, "SUM\texample.com/mod\t0.45") {
		t.Fatalf("no SUM row with the package's wall time (0.45); rows:\n%s", strings.Join(rows, "\n"))
	}
	if !rowsContain(rows, "TEST\texample.com/mod\tTestChild/child\t0.05") {
		t.Fatalf("subtest row missing, so the ceiling check cannot see it; rows:\n%s", strings.Join(rows, "\n"))
	}
	for _, row := range rows {
		if strings.HasPrefix(row, "SUM\texample.com/mod\t") && strings.Contains(row, "0.65") {
			t.Fatalf("SUM is the summed test Elapsed (0.65), not the package wall time; rows:\n%s", strings.Join(rows, "\n"))
		}
	}
}

// TestParseGoTestJSONRefusesAPackageSeenOnlyOnStart pins the terminal-event
// rule: a `start` event is progress, not a result, so a package the stream only
// ever started has produced no duration and must fail the completeness check
// rather than count as seen.
func TestParseGoTestJSONRefusesAPackageSeenOnlyOnStart(t *testing.T) {
	stream := `{"Action":"start","Package":"example.com/mod"}` + "\n"

	_, err := parseGoTestJSON(strings.NewReader(stream), []string{"example.com/mod"})
	if err == nil {
		t.Fatal("a stream that only starts the package must fail the completeness check")
	}
	if !strings.Contains(err.Error(), "produced no terminal event") {
		t.Fatalf("refusal does not name the missing terminal event: %v", err)
	}
}

// TestParseGoTestJSONRecordsAPackageWithNoTests pins the other half of the
// completeness contract: a package with no test files ends with a terminal
// `skip` and carries no Elapsed, so it earns a PKG row (seeding it at 0 for the
// budget) but no SUM row.
func TestParseGoTestJSONRecordsAPackageWithNoTests(t *testing.T) {
	stream := `{"Action":"start","Package":"example.com/empty"}` + "\n" +
		`{"Action":"skip","Package":"example.com/empty"}` + "\n"

	rows, err := parseGoTestJSON(strings.NewReader(stream), []string{"example.com/empty"})
	if err != nil {
		t.Fatalf("parseGoTestJSON: %v", err)
	}
	if !rowsContain(rows, "PKG\texample.com/empty") {
		t.Fatalf("no PKG row for a package with no tests; rows:\n%s", strings.Join(rows, "\n"))
	}
	for _, row := range rows {
		if strings.HasPrefix(row, "SUM\texample.com/empty") {
			t.Fatalf("a package with no reported Elapsed must not get a SUM row; rows:\n%s", strings.Join(rows, "\n"))
		}
	}
}

// TestParseGoTestJSONSkipsMalformedLines pins that one truncated or malformed
// line does not abort the parse: the stream's completeness is proven by the
// package inventory, not by every line being valid JSON.
func TestParseGoTestJSONSkipsMalformedLines(t *testing.T) {
	stream := "{not json}\n" +
		`{"Action":"pass","Package":"example.com/mod","Elapsed":0.2}` + "\n"

	rows, err := parseGoTestJSON(strings.NewReader(stream), []string{"example.com/mod"})
	if err != nil {
		t.Fatalf("parseGoTestJSON: %v", err)
	}
	if !rowsContain(rows, "SUM\texample.com/mod\t0.2") {
		t.Fatalf("valid line after a malformed one was not measured; rows:\n%s", strings.Join(rows, "\n"))
	}
}
