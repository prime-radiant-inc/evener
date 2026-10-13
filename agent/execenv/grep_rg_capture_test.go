package execenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// floodFixture writes an executable that emits record bytes from a file whose
// size exceeds the 8 MiB capture cap, standing in for a ripgrep search whose
// stdout is a real record stream too large to hold.
func floodFixture(t *testing.T, dir, name string, record []byte, minBytes int) (
	string, string,
) {
	t.Helper()
	rec := filepath.Join(dir, name+".records")
	var buf bytes.Buffer
	for buf.Len() < minBytes {
		buf.Write(record)
	}
	if err := os.WriteFile(rec, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexec cat " + shellQuotePath(rec) + "\n"
	exe := filepath.Join(dir, name)
	writeExecFixture(t, exe, script)
	return exe, rec
}

// shellQuotePath single-quotes a fixture path for the sh fixture scripts; the
// paths the tests mint carry no quotes, so the simple wrap is exact.
func shellQuotePath(p string) string { return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'" }

// TestGrepArmsDiscloseByteTruncationIdentically runs the same fixture through
// both grep arms and requires the same byte-truncation disclosure. On main the
// rg arm captured its whole stdout unbounded and cut only by line count, so it
// never emitted grepRetainedBytes' note while the native arm did — the two arms
// disclosed the same omission differently. The fake rg floods past the 8 MiB
// capture cap, so the rg arm must discard the corrupted capture and rerun the
// native capped walker, whose note is the one both arms emit.
func TestGrepArmsDiscloseByteTruncationIdentically(t *testing.T) {
	// Not parallel: shrinks the package-wide grepRetainedBytes budget.
	root := t.TempDir()
	const files = 6
	// Each matching line is long, so a handful of retained lines already cross
	// the shrunken byte budget.
	line := strings.Repeat("n", 200) + "NEEDLE"
	for i := 1; i <= files; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%02d.txt", i)), []byte(line+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	origRetained := grepRetainedBytes
	grepRetainedBytes = 512
	t.Cleanup(func() { grepRetainedBytes = origRetained })

	// Native arm: ripgrep unavailable, so the walk itself applies the budget.
	nativeEnv := NewLocalExecutionEnvironment(root)
	defer nativeEnv.Cleanup()
	nativeEnv.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }
	nativeOut, err := nativeEnv.Grep(context.Background(), "NEEDLE", root, "", false, 1_000_000, "content")
	if err != nil {
		t.Fatalf("native arm: %v", err)
	}
	if !strings.Contains(nativeOut, "retained-output cap") {
		t.Fatalf("native arm did not disclose the byte truncation: %q", nativeOut)
	}

	// rg arm: a fake rg whose stdout is a valid record stream well past the cap.
	record := []byte(filepath.Join(root, "flood", "f.txt") + "\x001:NEEDLE\n")
	rg, _ := floodFixture(t, t.TempDir(), "rg", record, 9<<20)
	rgEnv := NewLocalExecutionEnvironment(root)
	defer rgEnv.Cleanup()
	rgEnv.lookPath = func(name string) (string, error) {
		if name != "rg" {
			return "", fmt.Errorf("unexpected lookup %q", name)
		}
		return rg, nil
	}
	rgOut, err := rgEnv.Grep(context.Background(), "NEEDLE", root, "", false, 1_000_000, "content")
	if err != nil {
		t.Fatalf("rg arm: %v", err)
	}
	if rgOut != nativeOut {
		t.Fatalf("grep arms disclosed byte truncation differently:\n rg     = %q\n native = %q", rgOut, nativeOut)
	}
}
