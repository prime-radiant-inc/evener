package tomlcheck

import (
	"os"
	"path/filepath"
	"testing"
)

func FuzzTOMLCheck(f *testing.F) {
	f.Add("working_dir = 1\n[good_section]\n")
	f.Add("bad-key = 1\n[bad-section]\n")
	f.Add("prompt = \"\"\"\nfree text here\n\"\"\"\n")
	f.Fuzz(func(t *testing.T, doc string) {
		if len(doc) > 8192 {
			doc = doc[:8192]
		}
		_ = toSnakeCase(doc)
		_ = isSnakeCase(doc)
		_ = isExcluded(doc)
		path := filepath.Join(t.TempDir(), "fuzz.toml")
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		// The oracle is "never panic"; any parse outcome is acceptable.
		_, _ = checkTOMLFile(path, "fuzz.toml")
	})
}
