//go:build linux || darwin

package execenv

import (
	"os"
	"path/filepath"
	"testing"
)

// GrepSkipping never searches a file its skip names, with or without the
// sandbox, so the file neither appears in the output nor uses up the result
// cap; a file whose name merely starts like it is searched.
func TestGrepSkipping(t *testing.T) {
	t.Parallel()
	confinedRoot := t.TempDir()
	confined, err := NewConfinedFileEnvironment(confinedRoot, "scope")
	if err != nil {
		t.Fatal(err)
	}
	defer confined.Cleanup()
	plain := t.TempDir()
	for name, env := range map[string]*LocalExecutionEnvironment{
		"confined": confined,
		"plain":    NewLocalExecutionEnvironment(plain),
	} {
		dir := env.WorkingDirectory()
		for _, file := range []string{"A.md", "A.md-1-x.md", "b.md"} {
			if err := os.WriteFile(filepath.Join(dir, file), []byte("needle\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		skip := func(rel string) bool { return rel == "A.md" }
		got, err := env.GrepSkipping(t.Context(), "needle", dir, "", false, 100, "files_with_matches", 0, skip)
		if want := "A.md-1-x.md\nb.md"; err != nil || got != want {
			t.Fatalf("%s: got %q, %v; want %q", name, got, err, want)
		}
		got, err = env.GrepSkipping(t.Context(), "needle", dir, "", false, 1, "files_with_matches", 0, skip)
		if want := "A.md-1-x.md"; err != nil || got != want {
			t.Fatalf("%s capped: got %q, %v; want %q", name, got, err, want)
		}
	}
}
