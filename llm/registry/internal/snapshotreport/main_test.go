package main

import (
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// TestReportRunsAgainstEmbeddedSnapshot runs the report the refresh script
// prints for a human (scripts/ops/refresh-model-catalog.sh) against the real
// embedded snapshot. The machine-checked contracts: it reaches every section
// without exiting, it reports the fetched date of the snapshot it actually
// loaded, and the output-cap total it prints equals the count recomputed here
// from the same snapshot.
func TestReportRunsAgainstEmbeddedSnapshot(t *testing.T) {
	// Not parallel: swaps process-wide os.Stdout while main runs.
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	main()
	_ = w.Close()
	os.Stdout = oldStdout

	reportBytes, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	report := string(reportBytes)

	sections := []string{
		"overlay rows upstream now covers",
		"dangling overlay aliases",
		"output cap >= context window",
		"overlay pins whose upstream value differs",
	}
	at := 0
	for _, section := range sections {
		i := strings.Index(report[at:], section)
		if i < 0 {
			t.Errorf("report never reaches its section on %q", section)
			continue
		}
		at += i
	}

	raw, meta, err := registry.EmbeddedSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	fetchedLine := regexp.MustCompile(`(?m)^snapshot fetched (\d{4}-\d{2}-\d{2}) \(etag .*\)$`).
		FindStringSubmatch(report)
	if fetchedLine == nil {
		t.Fatalf("report has no fetched line:\n%s", report)
	}
	if want := meta.FetchedAt.Format("2006-01-02"); fetchedLine[1] != want {
		t.Errorf("report fetched date = %s, want the embedded snapshot's %s", fetchedLine[1], want)
	}

	upstream, err := registry.FromModelsDev(raw)
	if err != nil {
		t.Fatal(err)
	}
	wantJunk := 0
	for _, p := range upstream {
		for _, m := range p.Models {
			c := m.Caps
			if c.ContextWindow != nil && c.MaxOutputTokens != nil && *c.MaxOutputTokens >= *c.ContextWindow {
				wantJunk++
			}
		}
	}
	totalLine := regexp.MustCompile(`(?m)^  total: (\d+)$`).FindStringSubmatch(report)
	if totalLine == nil {
		t.Fatalf("report has no output-cap total line:\n%s", report)
	}
	var gotJunk int
	gotJunk, err = strconv.Atoi(totalLine[1])
	if err != nil {
		t.Fatalf("output-cap total %q is not a number", totalLine[1])
	}
	if gotJunk != wantJunk {
		t.Errorf("report output-cap total = %d, want %d from the same snapshot", gotJunk, wantJunk)
	}
}
