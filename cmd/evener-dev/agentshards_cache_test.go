package dev

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/internal/testfs"
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
	cfg, total, cached := primedShards(t)

	// Simulate a second run observing the cache mid-write: nonempty, but
	// carrying costs for only two of the fixture's tests.
	partial := "=== RUN   TestFixtureAlpha\n--- PASS: TestFixtureAlpha (0.03s)\n" +
		"=== RUN   TestFixtureDelta\n--- PASS: TestFixtureDelta (0.00s)\n"
	if err := os.WriteFile(cached, []byte(partial), 0o644); err != nil {
		t.Fatalf("seeding partial cache: %v", err)
	}

	rc, out, errOut := rerunShards(cfg)
	if rc != 0 {
		t.Fatalf("partial-cache rerun rc = %d\nstdout:\n%s\nstderr:\n%s", rc, out, errOut)
	}
	if data, err := os.ReadFile(cached); err != nil || string(data) == partial {
		t.Fatalf("a cache covering only a subset of the test set was accepted; the run did not re-survey (err %v)", err)
	}
	if got := shardedTestCount(t, out); got != total {
		t.Fatalf("partial-cache rerun sharded %d tests, want %d; the remaining tests ran in no shard:\n%s", got, total, out)
	}
}

// primedShards runs the fixture once on an empty cache. It returns the config,
// the number of tests that run placed into shards, and the survey it cached.
func primedShards(t *testing.T) (shardsConfig, int, string) {
	t.Helper()
	cfg, stdout, stderr, _ := e2eConfig(t)
	if rc := runShards(cfg); rc != 0 {
		t.Fatalf("priming run rc = %d\nstdout:\n%s\nstderr:\n%s", rc, stdout, stderr)
	}
	cached, err := filepath.Glob(filepath.Join(cfg.cacheDir, "survey-*.log"))
	if err != nil || len(cached) != 1 {
		t.Fatalf("survey cache not written: %v %v", cached, err)
	}
	total := shardedTestCount(t, stdout.String())
	if total == 0 {
		t.Fatalf("priming run sharded no tests:\n%s", stdout)
	}
	return cfg, total, cached[0]
}

// rerunShards runs cfg again with fresh output buffers.
func rerunShards(cfg shardsConfig) (rc int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	cfg.stdout, cfg.stderr = &out, &errOut
	rc = runShards(cfg)
	return rc, out.String(), errOut.String()
}

// olderTestSetSurvey gives cfg a cache holding one survey, named for lane and
// measured before the fixture's TestFixtureAlpha existed: a different test
// set from the current one, with no cost for that test. It returns the
// survey's path.
func olderTestSetSurvey(t *testing.T, cfg *shardsConfig, current, lane string) string {
	t.Helper()
	data, err := os.ReadFile(current)
	if err != nil {
		t.Fatalf("reading survey cache: %v", err)
	}
	var kept []string
	for line := range strings.SplitSeq(string(data), "\n") {
		if !strings.Contains(line, "TestFixtureAlpha") {
			kept = append(kept, line)
		}
	}
	cfg.cacheDir = t.TempDir()
	older := filepath.Join(cfg.cacheDir, lane+"-0000000000000000.log")
	if err := os.WriteFile(older, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		t.Fatalf("seeding older survey: %v", err)
	}
	return older
}

// surveyLane is the lane prefix of a survey cache file's name.
func surveyLane(cached string) string {
	base := filepath.Base(cached)
	return base[:strings.LastIndex(base, "-")]
}

// TestAgentShardsBorrowsTheLanesSurveyForAChangedTestSet: adding a test
// changes the test set's identity. The lane's last survey still prices every
// other test, so the run packs from it and places the unmeasured test in a
// shard anyway. Its shards measure every test, and it records those costs for
// the current test set, so the borrowed survey does not go stale.
func TestAgentShardsBorrowsTheLanesSurveyForAChangedTestSet(t *testing.T) {
	cfg, total, current := primedShards(t)
	older := olderTestSetSurvey(t, &cfg, current, surveyLane(current))
	cfg.jsonDir = filepath.Join(t.TempDir(), "json")

	rc, out, errOut := rerunShards(cfg)
	if rc != 0 {
		t.Fatalf("changed-test-set run rc = %d\nstdout:\n%s\nstderr:\n%s", rc, out, errOut)
	}
	if got := shardedTestCount(t, out); got != total {
		t.Fatalf("changed-test-set run sharded %d tests, want %d; the unmeasured test ran in no shard:\n%s", got, total, out)
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

// TestAgentShardsSurveysOnlyWithoutABorrowableSurvey: a survey is one more run
// of the whole suite before the shards run it, and a red survey stops the run
// before any shard starts. With a failing test in the suite, a shard reports
// the failure exactly when no survey ran first.
func TestAgentShardsSurveysOnlyWithoutABorrowableSurvey(t *testing.T) {
	primed, _, current := primedShards(t)
	t.Setenv("SHARD_FIXTURE_FAIL", "beta")

	for _, tc := range []struct {
		name string
		// lane names the older survey in the cache.
		lane string
		// recordable is whether the run's shard logs carry per-test costs.
		recordable  bool
		wantBorrows bool
	}{
		{name: "the lane's own survey", lane: surveyLane(current), recordable: true, wantBorrows: true},
		// The build decides what a test costs, so another build's survey
		// prices nothing here.
		{name: "another lane's survey", lane: "survey-ffffffffffffffff", recordable: true, wantBorrows: false},
		// A run that cannot record what its shards measured would leave the
		// lane on the borrowed survey for good.
		{name: "shard costs not recordable", lane: surveyLane(current), recordable: false, wantBorrows: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A red run keeps its scratch directory, which is named for this
			// process, so each case needs a scratch root of its own.
			t.Setenv("TMPDIR", testfs.CanonicalTempDir(t))
			cfg := primed
			olderTestSetSurvey(t, &cfg, current, tc.lane)
			if tc.recordable {
				cfg.jsonDir = filepath.Join(t.TempDir(), "json")
			}
			rc, out, errOut := rerunShards(cfg)
			if rc != 1 {
				t.Fatalf("run with a failing test rc = %d, want 1 (the suite is red)\nstdout:\n%s\nstderr:\n%s", rc, out, errOut)
			}
			if borrowed := len(verdictShards(t, out, "FAIL")) > 0; borrowed != tc.wantBorrows {
				t.Fatalf("a shard reported the failure = %v, want %v\nstdout:\n%s\nstderr:\n%s", borrowed, tc.wantBorrows, out, errOut)
			}
		})
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
