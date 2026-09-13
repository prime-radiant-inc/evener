package dev

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shardedTestCount sums the "(N tests)" figures the PASS lines report, the
// number of tests the run actually placed into shards.
func shardedTestCount(t *testing.T, out string) int {
	t.Helper()
	total := 0
	for line := range strings.SplitSeq(out, "\n") {
		var shard, n int
		var seconds float64
		if _, err := fmt.Sscanf(line, "PASS  agent:%d %fs (%d tests)", &shard, &seconds, &n); err == nil {
			total += n
		}
	}
	return total
}

// TestAgentShardsRejectsPartialSurveyCache pins the concurrent gate-run
// hazard from #1192: the survey cache is shared across runs, and a reader
// that accepts a nonempty but partial cache packs shards over only the
// tests it measured, then reports green while the rest run in no shard.
// A cache that does not cover the current test set must be rejected and
// the survey re-run.
func TestAgentShardsRejectsPartialSurveyCache(t *testing.T) {
	cfg, _, _, _ := e2eConfig(t)

	// First run populates the cache with a complete survey.
	var firstOut, firstErr bytes.Buffer
	cfg.stdout, cfg.stderr = &firstOut, &firstErr
	if rc := runShards(cfg); rc != 0 {
		t.Fatalf("priming run rc = %d\nstdout:\n%s\nstderr:\n%s", rc, &firstOut, &firstErr)
	}
	cached, err := filepath.Glob(filepath.Join(cfg.cacheDir, "survey-*.log"))
	if err != nil || len(cached) != 1 {
		t.Fatalf("survey cache not written: %v %v", cached, err)
	}
	total := shardedTestCount(t, firstOut.String())
	if total == 0 {
		t.Fatalf("priming run sharded no tests:\n%s", &firstOut)
	}

	// Simulate a second run observing the cache mid-write: nonempty, but
	// carrying costs for only two of the fixture's tests.
	partial := "=== RUN   TestFixtureAlpha\n--- PASS: TestFixtureAlpha (0.03s)\n" +
		"=== RUN   TestFixtureDelta\n--- PASS: TestFixtureDelta (0.00s)\n"
	if err := os.WriteFile(cached[0], []byte(partial), 0o644); err != nil {
		t.Fatalf("seeding partial cache: %v", err)
	}

	cfg2 := cfg
	var stdout2, stderr2 bytes.Buffer
	cfg2.stdout, cfg2.stderr = &stdout2, &stderr2
	if rc := runShards(cfg2); rc != 0 {
		t.Fatalf("partial-cache rerun rc = %d\nstdout:\n%s\nstderr:\n%s", rc, &stdout2, &stderr2)
	}
	if !strings.Contains(stdout2.String(), "surveying test costs") {
		t.Fatalf("a cache covering only a subset of the test set was accepted; the run did not re-survey:\n%s", &stdout2)
	}
	if got := shardedTestCount(t, stdout2.String()); got != total {
		t.Fatalf("partial-cache rerun sharded %d tests, want %d; the remaining tests ran in no shard:\n%s", got, total, &stdout2)
	}
}

// TestWriteFileAtomicReplacesTheDestinationInode pins the atomic-replace
// mechanism: a replacement must land on a new inode via rename, never by
// rewriting the shared destination in place, and it must not leave its
// temporary file behind.
func TestWriteFileAtomicReplacesTheDestinationInode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "survey.log")

	if err := writeFileAtomic(path, []byte("first\n")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}

	if err := writeFileAtomic(path, []byte("second\n")); err != nil {
		t.Fatalf("second write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "second\n" {
		t.Fatalf("content = %q, want %q", data, "second\n")
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if os.SameFile(before, after) {
		t.Fatalf("replacement reused the destination inode; the write was in place, not an atomic rename")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading dir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("directory holds %v, want only the destination; a temp file leaked", names)
	}
}
