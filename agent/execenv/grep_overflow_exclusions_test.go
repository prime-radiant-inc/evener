package execenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGrepOverflowKeepsRipgrepsExclusions is M2: when rg's output exceeds the
// retention budget the rg arm must keep ripgrep's own exclusion rules. Rerunning
// the native walker surfaced files ripgrep excludes — here one excluded by an
// ancestor .gitignore, which the native walker never reads because it is rooted
// at the search base (see loadIgnoreSet). The rg arm parses the bounded capture
// of ripgrep's own records, so an excluded file can never appear.
func TestGrepOverflowKeepsRipgrepsExclusions(t *testing.T) {
	// Not parallel: shrinks the package-wide grepRetainedBytes budget, which the
	// collector uses as its byte backstop.
	origRetained := grepRetainedBytes
	grepRetainedBytes = 256
	t.Cleanup(func() { grepRetainedBytes = origRetained })

	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "kept.txt"), []byte("NEEDLE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "excluded.txt"), []byte("NEEDLE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("excluded.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The fake rg emits only the kept file's record, flooded past the budget —
	// the shape of a real rg that honored the ancestor .gitignore.
	record := []byte(filepath.Join(sub, "kept.txt") + "\x001:NEEDLE\n")
	rg, _ := floodFixture(t, t.TempDir(), "rg", record, 4096)
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.lookPath = func(name string) (string, error) {
		if name != "rg" {
			return "", errors.New("unexpected lookup " + name)
		}
		return rg, nil
	}

	got, err := env.Grep(context.Background(), "NEEDLE", sub, "", false, 100, "content")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if strings.Contains(got, "excluded.txt") {
		t.Fatalf("an overflow result surfaced a file ripgrep excludes: %q", got)
	}
	if !strings.Contains(got, "retained-output cap") {
		t.Fatalf("an overflow result did not disclose the byte truncation: %q", got)
	}
}
