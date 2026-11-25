//go:build linux || darwin

package execenv

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/searchresult"
)

// GrepConfinedFiles and grepNativeSkipping never search a file skip names, so
// the file neither appears in the output nor uses up the result cap; a file
// whose name merely starts like it is searched.
func TestGrepSkipping(t *testing.T) {
	t.Parallel()
	confinedRoot := t.TempDir()
	confined, err := NewConfinedFileEnvironment(confinedRoot, "scope")
	if err != nil {
		t.Fatal(err)
	}
	defer confined.Cleanup()
	plain := NewLocalExecutionEnvironment(t.TempDir())
	for _, tc := range []struct {
		name string
		env  *LocalExecutionEnvironment
		grep func(dir string, maxResults int, skip func(rel string) bool) (string, error)
	}{
		{
			name: "confined",
			env:  confined,
			grep: func(dir string, maxResults int, skip func(rel string) bool) (string, error) {
				return confined.GrepConfinedFiles(t.Context(), "needle", dir, "", false, maxResults, "files_with_matches", 0, skip)
			},
		},
		{
			name: "plain",
			env:  plain,
			grep: func(dir string, maxResults int, skip func(rel string) bool) (string, error) {
				return plain.grepNativeSkipping(t.Context(), "needle", dir, "", false, maxResults, "files_with_matches", 0, skip)
			},
		},
	} {
		dir := tc.env.WorkingDirectory()
		for _, file := range []string{"A.md", "A.md-1-x.md", "b.md"} {
			if err := os.WriteFile(filepath.Join(dir, file), []byte("needle\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		skip := func(rel string) bool { return rel == "A.md" }
		got, err := tc.grep(dir, 100, skip)
		if want := "A.md-1-x.md\nb.md"; err != nil || got != want {
			t.Fatalf("%s: got %q, %v; want %q", tc.name, got, err, want)
		}
		got, err = tc.grep(dir, 1, skip)
		if want := searchresult.WithNotes("A.md-1-x.md", grepTruncationNote(1)); err != nil || got != want {
			t.Fatalf("%s capped: got %q, %v; want %q", tc.name, got, err, want)
		}
	}
}
