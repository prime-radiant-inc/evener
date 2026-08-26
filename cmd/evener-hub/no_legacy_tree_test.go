package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoLegacyTreeRoute asserts that no Go file in cmd/evener-hub/ (excluding
// this file and navigation_benchmark_test.go, which retains the frozen
// baseline) references /api/tree as a route, appwire.NotifyEvenerTreeChanged,
// or hubapi.TreeResponse. These are the legacy tree-endpoint artifacts
// retired by Task 14 (R50: zero legacy now).
func TestNoLegacyTreeRoute(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		// Skip this file and the benchmark baseline fixture.
		if name == "no_legacy_tree_test.go" || name == "navigation_benchmark_test.go" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(data)
		// Allow references in comments (lines starting with //).
		for _, line := range strings.Split(src, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if strings.Contains(line, `"/api/tree"`) {
				t.Errorf("%s: legacy /api/tree route reference: %s", name, strings.TrimSpace(line))
			}
			if strings.Contains(line, "appwire.NotifyEvenerTreeChanged") {
				t.Errorf("%s: appwire.NotifyEvenerTreeChanged reference: %s", name, strings.TrimSpace(line))
			}
			if strings.Contains(line, "hubapi.TreeResponse") {
				t.Errorf("%s: hubapi.TreeResponse reference: %s", name, strings.TrimSpace(line))
			}
		}
	}
}
