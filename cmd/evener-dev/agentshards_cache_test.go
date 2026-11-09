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

// surveyCacheFile stats the run's single survey cache file. The cache is
// replaced atomically by rename, so a fresh survey shows up as a new inode.
func surveyCacheFile(t *testing.T, cacheDir string) os.FileInfo {
	t.Helper()
	cached, err := filepath.Glob(filepath.Join(cacheDir, "survey-*.log"))
	if err != nil || len(cached) != 1 {
		t.Fatalf("survey cache not written: %v %v", cached, err)
	}
	info, err := os.Stat(cached[0])
	if err != nil {
		t.Fatalf("stat survey cache: %v", err)
	}
	return info
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
	if data, err := os.ReadFile(cached[0]); err != nil || string(data) == partial {
		t.Fatalf("a cache covering only a subset of the test set was accepted; the run did not re-survey (err %v)", err)
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

// olderTestSetSurvey replaces the run's survey cache with one measured before
// name existed: a different test set, so a different cache entry, carrying no
// cost for name. lane is the entry's lane prefix, or "" for the run's own. It
// returns the entry's path.
func olderTestSetSurvey(t *testing.T, cacheDir, lane, name string) string {
	t.Helper()
	cached, err := filepath.Glob(filepath.Join(cacheDir, "survey-*.log"))
	if err != nil || len(cached) != 1 {
		t.Fatalf("survey cache not written: %v %v", cached, err)
	}
	data, err := os.ReadFile(cached[0])
	if err != nil {
		t.Fatalf("reading survey cache: %v", err)
	}
	var kept []string
	for line := range strings.SplitSeq(string(data), "\n") {
		if !strings.Contains(line, name) {
			kept = append(kept, line)
		}
	}
	if lane == "" {
		base := filepath.Base(cached[0])
		lane = base[:strings.LastIndex(base, "-")]
	}
	older := filepath.Join(cacheDir, lane+"-0000000000000000.log")
	if err := os.WriteFile(older, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		t.Fatalf("seeding older survey: %v", err)
	}
	if err := os.Remove(cached[0]); err != nil {
		t.Fatalf("removing current survey: %v", err)
	}
	return older
}

// primedShardsConfig returns a config whose cache holds one complete survey,
// and the number of tests that priming run placed into shards.
func primedShardsConfig(t *testing.T) (shardsConfig, int) {
	t.Helper()
	cfg, stdout, stderr, _ := e2eConfig(t)
	if rc := runShards(cfg); rc != 0 {
		t.Fatalf("priming run rc = %d\nstdout:\n%s\nstderr:\n%s", rc, stdout, stderr)
	}
	total := shardedTestCount(t, stdout.String())
	if total == 0 {
		t.Fatalf("priming run sharded no tests:\n%s", stdout)
	}
	return cfg, total
}

// TestAgentShardsReusesTheLanesSurveyWhenTheTestSetChanges: adding a test
// changes the test set's identity, and surveying again runs the whole suite a
// second time before the shards run it. The lane's last survey still prices
// every other test, so the run packs from it, places the unmeasured test in a
// shard anyway, and runs the suite once.
func TestAgentShardsReusesTheLanesSurveyWhenTheTestSetChanges(t *testing.T) {
	cfg, total := primedShardsConfig(t)
	older := olderTestSetSurvey(t, cfg.cacheDir, "", "TestFixtureAlpha")

	var stdout, stderr bytes.Buffer
	cfg.stdout, cfg.stderr = &stdout, &stderr
	if rc := runShards(cfg); rc != 0 {
		t.Fatalf("changed-test-set run rc = %d\nstdout:\n%s\nstderr:\n%s", rc, &stdout, &stderr)
	}
	if got := shardedTestCount(t, stdout.String()); got != total {
		t.Fatalf("changed-test-set run sharded %d tests, want %d; the unmeasured test ran in no shard:\n%s", got, total, &stdout)
	}
	// A survey pass writes a cache entry for the new test set, so the older
	// entry standing alone means no survey ran.
	cached, err := filepath.Glob(filepath.Join(cfg.cacheDir, "survey-*.log"))
	if err != nil || len(cached) != 1 || cached[0] != older {
		t.Fatalf("cache holds %v (err %v), want only %s; the run surveyed instead of reusing the lane's survey", cached, err, older)
	}
}

// TestAgentShardsRecordsShardCostsForAChangedTestSet: a run that borrowed an
// older survey measured every test while running its shards. It records those
// costs for the current test set, so the unmeasured test is priced from then
// on and the borrowed survey does not go stale.
func TestAgentShardsRecordsShardCostsForAChangedTestSet(t *testing.T) {
	cfg, _ := primedShardsConfig(t)
	older := olderTestSetSurvey(t, cfg.cacheDir, "", "TestFixtureAlpha")

	cfg.jsonDir = filepath.Join(t.TempDir(), "json")
	var stdout, stderr bytes.Buffer
	cfg.stdout, cfg.stderr = &stdout, &stderr
	if rc := runShards(cfg); rc != 0 {
		t.Fatalf("changed-test-set run rc = %d\nstdout:\n%s\nstderr:\n%s", rc, &stdout, &stderr)
	}
	cached, err := filepath.Glob(filepath.Join(cfg.cacheDir, "survey-*.log"))
	if err != nil {
		t.Fatalf("globbing survey cache: %v", err)
	}
	priced := false
	for _, path := range cached {
		if path == older {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for _, tc := range parseSurvey(string(data)) {
			priced = priced || tc.name == "TestFixtureAlpha"
		}
	}
	if !priced {
		t.Fatalf("no cache entry prices TestFixtureAlpha after a green run measured it: %v", cached)
	}
}

// TestAgentShardsIgnoresAnotherLanesSurvey: the build decides what a test
// costs, so a survey measured under another build prices nothing here. With
// only that to borrow, the run surveys.
func TestAgentShardsIgnoresAnotherLanesSurvey(t *testing.T) {
	cfg, _ := primedShardsConfig(t)
	other := olderTestSetSurvey(t, cfg.cacheDir, "survey-ffffffffffffffff", "TestFixtureAlpha")

	var stdout, stderr bytes.Buffer
	cfg.stdout, cfg.stderr = &stdout, &stderr
	if rc := runShards(cfg); rc != 0 {
		t.Fatalf("other-lane run rc = %d\nstdout:\n%s\nstderr:\n%s", rc, &stdout, &stderr)
	}
	cached, err := filepath.Glob(filepath.Join(cfg.cacheDir, "survey-*.log"))
	if err != nil || len(cached) != 2 {
		t.Fatalf("cache holds %v (err %v), want %s and this lane's own survey", cached, err, other)
	}
}
