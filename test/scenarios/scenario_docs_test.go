package scenarios

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// staleStateRE matches any form of state/status being set or compared to the
// stale "processing" value (e.g. state=processing, "state": "processing",
// state='processing', status: processing). Use the canonical "active" value instead.
var staleStateRE = regexp.MustCompile(`(?i)(state|status)[=: '"]+processing`)

func TestScenarioDocsUseCanonicalActiveState(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(".", entry.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if m := staleStateRE.FindString(string(body)); m != "" {
			t.Fatalf("%s contains stale state form %q; use canonical active state", entry.Name(), m)
		}
	}
}
