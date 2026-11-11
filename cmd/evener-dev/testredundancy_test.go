package dev

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// redundancyFixture copies the testredundancy fixture package into a fresh
// directory, isolated from the repo's workspace and the developer's GOFLAGS.
func redundancyFixture(t *testing.T) string {
	t.Helper()
	t.Setenv("GOWORK", "off")
	isolateToolchainEnv(t)
	dir := filepath.Join(t.TempDir(), "calc")
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("..", "..", "internal", "devtool", "testredundancy", "testdata", "fixture"))); err != nil {
		t.Fatalf("copying fixture: %v", err)
	}
	return dir
}

// The command line's contract for a deletion batch: cover before and after,
// then proof exits nonzero exactly when the batch lost more statements than
// the noise band allows. A command line it cannot act on is a usage error.
func TestTestRedundancyProofFailsOutsideTheNoiseBand(t *testing.T) {
	dir := redundancyFixture(t)
	cache := filepath.Join(t.TempDir(), "cache")
	work := t.TempDir()
	run := func(args ...string) (int, string) {
		var stdout, stderr bytes.Buffer
		code := testRedundancyRun(t.Context(), append(args, "-pkg", dir, "-cache", cache), &stdout, &stderr)
		return code, stdout.String() + stderr.String()
	}
	before, after := filepath.Join(work, "before.cov"), filepath.Join(work, "after.cov")
	if code, out := run("cover", "-o", before); code != 0 {
		t.Fatalf("cover before: exit %d\n%s", code, out)
	}
	// Deleting TestSignZero loses Sign's return-zero statement, the one
	// statement no other fixture test reaches.
	testFile := filepath.Join(dir, "calc_test.go")
	src, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testFile, []byte(strings.Replace(string(src), "func TestSignZero(", "func signZero(", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out := run("cover", "-o", after); code != 0 {
		t.Fatalf("cover after: exit %d\n%s", code, out)
	}
	if code, out := run("proof", "-before", before, "-after", after, "-noise", "0"); code != 1 {
		t.Fatalf("proof with a lost statement and no noise band: exit %d, want 1\n%s", code, out)
	}
	if code, out := run("proof", "-before", before, "-after", after, "-noise", "1"); code != 0 {
		t.Fatalf("proof within the noise band: exit %d, want 0\n%s", code, out)
	}

	for _, args := range [][]string{
		{"unique"},
		{"mutate", "-spec", filepath.Join(work, "spec.json")},
		{"proof", "-before", before},
		{"no-such-command"},
	} {
		if code, out := run(args...); code != 2 {
			t.Errorf("%v: exit %d, want a usage error\n%s", args, code, out)
		}
	}
}

// A tests file lists one name per line; blank lines and comments carry none,
// and a file that names no test is an error rather than an empty batch.
func TestReadTestNamesSkipsBlanksAndComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "names.txt")
	if err := os.WriteFile(path, []byte("# batch one\nTestA\n\n  TestB  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	names, err := readTestNames(path)
	if err != nil {
		t.Fatalf("readTestNames: %v", err)
	}
	if strings.Join(names, ",") != "TestA,TestB" {
		t.Fatalf("names = %q", names)
	}
	if err := os.WriteFile(path, []byte("# nothing\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readTestNames(path); err == nil {
		t.Fatal("a file naming no tests was accepted")
	}
}
