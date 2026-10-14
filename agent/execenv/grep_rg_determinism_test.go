package execenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// rgOrderFixture writes a fake rg that emits the given absolute paths as
// content-mode `--null` records, in the order given — standing in for ripgrep's
// parallel file-completion order.
func rgOrderFixture(t *testing.T, dir string, paths []string) string {
	t.Helper()
	var buf bytes.Buffer
	for _, p := range paths {
		buf.WriteString(p)
		buf.WriteString("\x001:NEEDLE\n")
	}
	rec := filepath.Join(dir, "records.bin")
	if err := os.WriteFile(rec, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "rg")
	writeExecFixture(t, exe, "#!/bin/sh\nexec cat "+shellQuotePath(rec)+"\n")
	return exe
}

// TestGrepOverflowRetentionIsEmissionOrderIndependent is MEDIUM (round 5):
// ripgrep emits matches in file-completion order, so a byte cap on the raw
// stream retains whatever prefix the emission happened to produce — two
// identical searches can return different files. The retained set must depend
// only on the corpus and the query, never on emission order.
//
// The capture cap is shrunk so the PRE-fix selection mechanism (a byte cap on
// the raw stream) trips and the red reproduces the defect; the collector that
// fixes it retains the earliest records in path order regardless of the cap.
func TestGrepOverflowRetentionIsEmissionOrderIndependent(t *testing.T) {
	// Not parallel: shrinks the env's capture cap.
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

	// A cap that keeps exactly two records' head, so the pre-fix retained set is
	// the first two files in EMISSION order.
	recordLen := len(paths[0] + "\x001:NEEDLE\n")
	capBytes := 4 * recordLen

	run := func(rg string) string {
		env := NewLocalExecutionEnvironment(root)
		defer env.Cleanup()
		env.outputCaptureCap = &capBytes
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
	ascOut := run(rgOrderFixture(t, t.TempDir(), ascending))
	descOut := run(rgOrderFixture(t, t.TempDir(), descending))
	if ascOut != descOut {
		t.Fatalf("retained results depend on ripgrep's emission order:\n ascending  = %q\n descending = %q", ascOut, descOut)
	}
}
