package execenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/searchresult"
)

// rgOrderFixture writes a fake rg that emits one content-mode `--null` record
// per path, in the order given — standing in for ripgrep's parallel
// file-completion order.
func rgOrderFixture(t *testing.T, dir string, paths []string) string {
	t.Helper()
	var buf bytes.Buffer
	for _, p := range paths {
		buf.WriteString(p)
		buf.WriteString("\x001:NEEDLE" + strings.Repeat("n", 200) + "\n")
	}
	rec := filepath.Join(dir, "records.bin")
	if err := os.WriteFile(rec, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "rg")
	writeExecFixture(t, exe, "#!/bin/sh\nexec cat "+shellQuotePath(rec)+"\n")
	return exe
}

// TestGrepOverflowIsBoundedAndDisclosedWhateverTheEmissionOrder replaces the
// round-5 determinism test. Cross-run determinism of a truncated SUBSET is
// explicitly not a product requirement (Jesse's 2026-10-14 ruling, project
// memory rg-collector-deletion-ruling.md: two identical over-cap greps may
// return different, equally bounded, equally disclosed subsets), so the
// emission order is no longer required to produce the same files. The bound and
// the disclosure are what must hold on every order: neither may retain the whole
// stream, and both must say that bytes were left out.
func TestGrepOverflowIsBoundedAndDisclosedWhateverTheEmissionOrder(t *testing.T) {
	// Not parallel: shrinks the package-wide byte budget.
	stubGrepRetainedBytes(t, 512)
	root := t.TempDir()
	const files = 12
	paths := make([]string, 0, files)
	for i := 1; i <= files; i++ {
		path := filepath.Join(root, fmt.Sprintf("f%02d.txt", i))
		if err := os.WriteFile(path, []byte("NEEDLE\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	ascending := append([]string(nil), paths...)
	descending := append([]string(nil), paths...)
	slices.Reverse(descending)

	run := func(rg string) string {
		env := NewLocalExecutionEnvironment(root)
		defer env.Cleanup()
		env.lookPath = func(name string) (string, error) {
			if name != "rg" {
				return "", errors.New("unexpected lookup " + name)
			}
			return rg, nil
		}
		out, err := env.Grep(context.Background(), "NEEDLE", root, "", false, 3, "content")
		if err != nil {
			t.Fatalf("Grep: %v", err)
		}
		return out
	}
	for _, tc := range []struct {
		name  string
		order []string
	}{
		{"ascending", ascending},
		{"descending", descending},
	} {
		out := run(rgOrderFixture(t, t.TempDir(), tc.order))
		entries, notes := searchresult.Split(out)
		if len(entries) > 2*grepRetainedBytes {
			t.Errorf("%s emission retained %d bytes of entries under a %d-byte budget",
				tc.name, len(entries), grepRetainedBytes)
		}
		if !strings.Contains(notes, "retained-output cap") {
			t.Errorf("%s emission did not disclose the byte truncation: %q", tc.name, out)
		}
	}
}
