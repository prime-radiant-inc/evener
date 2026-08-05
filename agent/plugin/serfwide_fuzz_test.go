package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzDiscoverSerfwideFrontmatter fuzzes command-file content and filenames
// through serf-wide discovery: no panics, bare keys only, every rejected
// file produces a warning.
func FuzzDiscoverSerfwideFrontmatter(f *testing.F) {
	f.Add("review", "body")
	f.Add("a b", "---\nmodel: x\n---\n!`ls`")
	f.Add("p:q", "---\n[bad\n---\nx")
	f.Add("", "body")
	f.Fuzz(func(t *testing.T, name, content string) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "serf", "commands")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0644); err != nil {
			t.Skip() // filename not representable on disk
		}
		got, warnings := DiscoverSerfWideCommands(nil)
		for key := range got {
			if strings.ContainsAny(key, ": \t\n") || key == "" {
				t.Fatalf("bad key %q escaped the guards", key)
			}
		}
		if len(got) == 0 && len(warnings) == 0 && name != "" && filepath.Base(name) == name {
			t.Fatalf("valid-looking file %q produced neither command nor warning", name)
		}
	})
}
