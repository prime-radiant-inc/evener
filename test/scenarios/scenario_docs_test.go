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

func TestInlineOutputImageScenarioCardsExist(t *testing.T) {
	for _, id := range []string{
		"read-image-tool-result-inline",
		"written-image-inline-after-reload",
		"shell-generated-image-path-inline",
		"unsafe-image-path-ignored",
		"output-image-lightbox-and-pane",
	} {
		path := id + ".md"
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("required scenario card %q missing at %s: %v", id, path, err)
		}
		if !strings.HasPrefix(string(body), "# "+id+":") {
			t.Fatalf("%s must start with canonical scenario heading %q", path, "# "+id+":")
		}
	}
}
