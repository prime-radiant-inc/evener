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
// embedded snapshot. The machine-checked contracts, asserted on observable
// data rather than the heading copy a person reads: main emits its section
// markers without exiting, the fetched date it prints is the embedded
// snapshot's own, and every section's data equals the same snapshot and
// overlay recomputed here.
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

	// Reachability: the report is sectioned, and main must emit every section
	// it documents. The section-marker shape is the tool's output contract;
	// the words inside a heading are copy a person reads, not pinned here.
	if markers := strings.Count(report, "\n== "); markers < 4 {
		t.Errorf("report reached only %d section markers, want at least the 4 documented sections:\n%s", markers, report)
	}

	raw, meta, err := registry.EmbeddedSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	// The fetched date of the snapshot the report actually loaded.
	if want := meta.FetchedAt.Format("2006-01-02"); !strings.Contains(report, want) {
		t.Errorf("report does not carry the embedded snapshot's fetched date %s", want)
	}

	upstream, err := registry.FromModelsDev(raw)
	if err != nil {
		t.Fatal(err)
	}
	overlay, err := registry.ParseOverlay(registry.EmbeddedOverlay())
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]registry.Provider, len(upstream))
	for _, p := range upstream {
		byID[p.ID] = p
	}

	// Section: overlay rows upstream now covers. Every covered alias row the
	// same overlay and snapshot yield must appear in the report.
	for _, pid := range sortedKeys(overlay.Providers) {
		for _, mid := range sortedKeys(overlay.Providers[pid].Models) {
			om := overlay.Providers[pid].Models[mid]
			up, ok := byID[pid]
			if !ok {
				continue
			}
			if _, ok := up.Models[mid]; !ok || om.AliasOf == "" {
				continue
			}
			if !strings.Contains(report, pid+"/"+mid) {
				t.Errorf("report omits the covered overlay alias row %s/%s", pid, mid)
			}
		}
	}

	// Section: dangling overlay aliases. The report must carry every warning
	// the same offline load of the same snapshot and overlay produces.
	reg, err := registry.Load(registry.WithoutCache(), registry.WithNoUserLayer(), registry.WithOffline(true), registry.WithEnv(func(string) (string, bool) { return "", false }))
	if err != nil {
		t.Fatal(err)
	}
	for _, warn := range reg.Warnings() {
		if !strings.Contains(report, warn) {
			t.Errorf("report omits the registry warning %q", warn)
		}
	}

	// Section: upstream rows with output cap >= context window. The total it
	// prints must equal the count recomputed here from the same snapshot.
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

	// Section: overlay pins whose upstream value differs. Every differing pin
	// must appear in the report, named by its overlay field. The field names
	// are the overlay file format's machine tokens, not copy.
	for _, pid := range sortedKeys(overlay.Providers) {
		up, ok := byID[pid]
		if !ok {
			continue
		}
		for _, mid := range sortedKeys(overlay.Providers[pid].Models) {
			pin := overlay.Providers[pid].Models[mid].Caps
			upRow, ok := up.Models[mid]
			if !ok {
				continue
			}
			if pin.ContextWindow != nil && upRow.Caps.ContextWindow != nil && *pin.ContextWindow != *upRow.Caps.ContextWindow {
				if !strings.Contains(report, pid+"/"+mid+" context_window") {
					t.Errorf("report omits the %s/%s context-window pin diff (overlay %d, upstream %d)", pid, mid, *pin.ContextWindow, *upRow.Caps.ContextWindow)
				}
			}
			if pin.MaxOutputTokens != nil && upRow.Caps.MaxOutputTokens != nil && *pin.MaxOutputTokens != *upRow.Caps.MaxOutputTokens {
				if !strings.Contains(report, pid+"/"+mid+" max_output_tokens") {
					t.Errorf("report omits the %s/%s max-output-tokens pin diff (overlay %d, upstream %d)", pid, mid, *pin.MaxOutputTokens, *upRow.Caps.MaxOutputTokens)
				}
			}
		}
	}
}
