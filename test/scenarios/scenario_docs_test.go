package scenarios

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScenarioDocsUseCanonicalActiveState(t *testing.T) {
	stale := []string{
		`[ "$state" = "processing" ]`,
		`state=processing`,
		`state= processing`,
		`state: processing`,
		`state == "processing"`,
		`state = "processing"`,
		`status=processing`,
		`status: processing`,
		`{ state: "processing"`,
		`data-state="processing"`,
	}

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
		text := string(body)
		for _, needle := range stale {
			if strings.Contains(text, needle) {
				t.Fatalf("%s contains stale state assertion %q; use canonical active", entry.Name(), needle)
			}
		}
	}
}
