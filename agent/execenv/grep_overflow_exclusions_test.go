package execenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/searchresult"
)

// TestGrepOverflowKeepsRipgrepsExclusions is M2: on a capture overflow the rg
// arm must keep ripgrep's own exclusion rules. Rerunning the native walker
// surfaced files ripgrep excludes — here one excluded by an ancestor
// .gitignore, which the native walker never reads because it is rooted at the
// search base (see loadIgnoreSet).
func TestGrepOverflowKeepsRipgrepsExclusions(t *testing.T) {
	// Not parallel: shrinks the env's capture cap via its seam.
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
	// The fake rg emits only the kept file's record, flooded past the cap — the
	// shape of a real rg that honored the ancestor .gitignore.
	record := []byte(filepath.Join(sub, "kept.txt") + "\x001:NEEDLE\n")
	rg, _ := floodFixture(t, t.TempDir(), "rg", record, 4096)
	capBytes := 256
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.outputCaptureCap = &capBytes
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

// TestGrepOverflowDropsAPartialFirstPathname is MEDIUM 3: the rg arm must not
// infer single-file mode from the absence of a NUL in the retained head. When
// the cap cuts inside the FIRST pathname and that pathname contains a newline,
// a NUL-absence heuristic treats the partial path as a single-file line and
// fabricates a result; the arm already knows the shape (grepTargetsOneFile).
func TestGrepOverflowDropsAPartialFirstPathname(t *testing.T) {
	// Not parallel: shrinks the env's capture cap.
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sub, "we\nird.txt")
	record := []byte(path + "\x001:NEEDLE\n")
	nl := strings.IndexByte(path, '\n')
	// Head lands after the pathname's newline but before its NUL, so no record
	// completes.
	capBytes := 2 * (nl + 2)
	rg, _ := floodFixture(t, t.TempDir(), "rg", record, 4096)
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.outputCaptureCap = &capBytes
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
	entries, notes := searchresult.Split(got)
	if strings.TrimSpace(entries) != "" {
		t.Fatalf("an overflow cut inside the first pathname fabricated entries: %q", entries)
	}
	if notes == "" {
		t.Fatalf("the overflow disclosed nothing: %q", got)
	}
}
