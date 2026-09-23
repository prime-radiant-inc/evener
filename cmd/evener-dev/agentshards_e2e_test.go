package dev

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// fixtureModule returns the absolute path of the real test module the
// runner is aimed at.
func fixtureModule(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "shardfixture"))
	if err != nil {
		t.Fatalf("resolving fixture module: %v", err)
	}
	return abs
}

// isolateToolchainEnv keeps a run from inheriting the developer's toolchain
// settings. The runner refuses a test-side flag in GOFLAGS, so
// `go env -w GOFLAGS=-short` on someone's machine would fail every test that
// calls runShards -- and a build flag there would quietly change what is
// built and measured. GOENV=off ignores the written file; the empty GOFLAGS
// overrides whatever is already exported.
//
// Every test that builds a shardsConfig and calls runShards, or that runs the
// built binary, needs this.
func isolateToolchainEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GOENV", "off")
	t.Setenv("GOFLAGS", "")
}

// e2eConfig is a runShards config over the fixture module with isolated
// TMPDIR and survey cache, capture buffers attached.
func e2eConfig(t *testing.T) (shardsConfig, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	// The fixture module lives under testdata, outside the repo's go.work
	// workspace; the child toolchain must resolve its own go.mod instead.
	t.Setenv("GOWORK", "off")
	isolateToolchainEnv(t)
	resolved, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatalf("resolving TMPDIR fixture: %v", err)
	}
	var stdout, stderr bytes.Buffer
	return shardsConfig{
		label: "agent", pkgDir: fixtureModule(t),
		count:    2,
		parallel: 1,
		cacheDir: filepath.Join(t.TempDir(), "cache"),
		stdout:   &stdout,
		stderr:   &stderr,
	}, &stdout, &stderr, resolved
}

func scratchLeftovers(t *testing.T, tmp string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(tmp, "agent-test-shards.*"))
	if err != nil {
		t.Fatalf("globbing scratch leftovers: %v", err)
	}
	return matches
}

func TestAgentShardsGreenRunSurveysPassesAndCleansUp(t *testing.T) {
	cfg, stdout, stderr, tmp := e2eConfig(t)

	rc := runShards(cfg)
	if rc != 0 {
		t.Fatalf("green run rc = %d\nstdout:\n%s\nstderr:\n%s", rc, stdout, stderr)
	}
	out := stdout.String()
	for _, want := range []string{
		"agent-shards: surveying test costs (one-time for this test set)",
		"agent-shards: 2 shards, -parallel 1 each",
		"PASS  agent:0",
		"PASS  agent:1",
		" tests)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("green run stdout missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out+stderr.String(), "full logs:") {
		t.Fatalf("green run reported retained logs:\n%s\n%s", out, stderr)
	}
	// A green survey prints no failure excerpt: the block exists to explain a
	// failure, and the summary must not grow an empty one.
	if stderr.Len() != 0 {
		t.Fatalf("green run wrote a failure excerpt to stderr:\n%s", stderr)
	}
	if left := scratchLeftovers(t, tmp); len(left) != 0 {
		t.Fatalf("green run left scratch behind: %v", left)
	}
	cached, err := filepath.Glob(filepath.Join(cfg.cacheDir, "survey-*.log"))
	if err != nil || len(cached) != 1 {
		t.Fatalf("survey cache not written: %v %v", cached, err)
	}

	// Second run, same cache: the survey must not re-run.
	cfg2 := cfg
	var stdout2, stderr2 bytes.Buffer
	cfg2.stdout, cfg2.stderr = &stdout2, &stderr2
	if rc := runShards(cfg2); rc != 0 {
		t.Fatalf("cached rerun rc = %d\nstdout:\n%s\nstderr:\n%s", rc, &stdout2, &stderr2)
	}
	if strings.Contains(stdout2.String(), "surveying test costs") {
		t.Fatalf("cached rerun surveyed again:\n%s", &stdout2)
	}
	if !strings.Contains(stdout2.String(), "PASS  agent:1") {
		t.Fatalf("cached rerun did not pass:\n%s", &stdout2)
	}
}

// TestAgentShardsBuildsAndRunsWithTheCallersFlags is the wiring: parseFlags is
// unit-tested, but nothing proved that runShards hands the build half to
// `go test -c` and the test half to the shard invocations. The fixture's gate
// test answers both questions from inside the binary -- a build tag only the
// caller's -tags can set for the compile, testing.Verbose() for the invocation
// -- so dropping either half turns this run red. Neither flag needs cgo, so
// this runs wherever the suite does.
func TestAgentShardsBuildsAndRunsWithTheCallersFlags(t *testing.T) {
	cfg, stdout, stderr, _ := e2eConfig(t)
	cfg.flags = []string{"-tags", "shardfixturetag", "-count=1", "-v"}
	t.Setenv("SHARDFIXTURE_GATE", "1")
	if code := runShards(cfg); code != 0 {
		t.Fatalf("runShards = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
}

// TestAgentShardsRunFileHoldsRegex verifies that the -test.run regex for
// each shard is written to a file (shardN.run) and handed via
// EVENER_SHARD_RUN_FILE, not passed on the execve argument list. A
// failing run retains the scratch dir, so we can inspect the files.
func TestAgentShardsRunFileHoldsRegex(t *testing.T) {
	cfg, stdout, _, tmp := e2eConfig(t)
	cfg.noSurvey = true
	t.Setenv("SHARD_FIXTURE_FAIL", "beta")

	_ = runShards(cfg) // fails, retaining scratch

	out := stdout.String()
	if !strings.Contains(out, "FAIL  agent:") {
		t.Fatalf("expected a failing shard:\n%s", out)
	}
	runFiles, err := filepath.Glob(filepath.Join(tmp, "agent-test-shards.*", "shard*.run"))
	if err != nil || len(runFiles) != 2 {
		t.Fatalf("expected 2 shard .run files, got %v: %v", runFiles, err)
	}
	for _, rf := range runFiles {
		data, err := os.ReadFile(rf)
		if err != nil {
			t.Fatalf("reading %s: %v", rf, err)
		}
		pattern := strings.TrimSpace(string(data))
		if !strings.HasPrefix(pattern, "^(") || !strings.HasSuffix(pattern, ")$") {
			t.Fatalf("run file %s does not look like an anchored regex: %q", rf, pattern)
		}
		// The fixture has few tests; each pattern must match at least one.
		re, err := regexp.Compile(pattern)
		if err != nil {
			t.Fatalf("run file %s regex does not compile: %v", rf, err)
		}
		if !re.MatchString("TestFixtureAlpha") && !re.MatchString("TestFixtureBeta") {
			t.Fatalf("run file %s matches neither fixture test: %q", rf, pattern)
		}
	}
}

func TestShardRunFileExplicitFailuresAndValidSelection(t *testing.T) {
	fixture := buildShardFixture(t)
	fixtureDir := fixtureModule(t)
	type testCase struct {
		name       string
		content    *string
		path       string
		wantExit   int
		wantOutput string
	}
	valid := "^TestFixtureAlpha$\n"
	empty := " \n\t"
	invalid := "["
	cases := []testCase{
		{name: "unset", wantExit: 0, wantOutput: "--- PASS: TestFixtureAlpha"},
		{name: "missing", path: "missing.run", wantExit: 2, wantOutput: "read failed"},
		{name: "empty", content: &empty, path: "empty.run", wantExit: 2, wantOutput: "run regex is empty"},
		{name: "invalid", content: &invalid, path: "invalid.run", wantExit: 2, wantOutput: "invalid run regex"},
		{name: "valid", content: &valid, path: "valid.run", wantExit: 0, wantOutput: "--- PASS: TestFixtureAlpha"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var runFile string
			if tc.content != nil {
				runFile = filepath.Join(t.TempDir(), tc.path)
				if err := os.WriteFile(runFile, []byte(*tc.content), 0o644); err != nil {
					t.Fatalf("writing run file: %v", err)
				}
			} else if tc.path != "" {
				runFile = filepath.Join(t.TempDir(), tc.path)
			}
			cmd := exec.Command(fixture, "-test.count=1", "-test.v")
			cmd.Dir = fixtureDir
			cmd.Env = slices.DeleteFunc(os.Environ(), func(value string) bool {
				return strings.HasPrefix(value, "EVENER_SHARD_RUN_FILE=")
			})
			cmd.Env = append(cmd.Env, "GOWORK=off")
			if runFile != "" {
				cmd.Env = append(cmd.Env, "EVENER_SHARD_RUN_FILE="+runFile)
			}
			output, err := cmd.CombinedOutput()
			if err == nil {
				if tc.wantExit != 0 {
					t.Fatalf("exit = 0, want %d; output:\n%s", tc.wantExit, output)
				}
			} else {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != tc.wantExit {
					t.Fatalf("exit = %v, want %d; output:\n%s", err, tc.wantExit, output)
				}
			}
			if !strings.Contains(string(output), tc.wantOutput) {
				t.Fatalf("output missing %q:\n%s", tc.wantOutput, output)
			}
			if tc.name == "valid" && strings.Contains(string(output), "--- PASS: TestFixtureBeta") {
				t.Fatalf("valid handoff ran an unselected test:\n%s", output)
			}
		})
	}
}

func buildShardFixture(t *testing.T) string {
	t.Helper()
	isolateToolchainEnv(t)
	bin := filepath.Join(t.TempDir(), "shardfixture.test")
	cmd := exec.Command("go", "test", "-c", "-o", bin, ".")
	cmd.Dir = fixtureModule(t)
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(value string) bool {
		return strings.HasPrefix(value, "EVENER_SHARD_RUN_FILE=")
	}), "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building shard fixture: %v\n%s", err, output)
	}
	return bin
}

// passLineSeconds extracts each PASS line's reported wall seconds by shard.
func passLineSeconds(t *testing.T, out string) map[int]float64 {
	t.Helper()
	seconds := map[int]float64{}
	for line := range strings.SplitSeq(out, "\n") {
		var shard int
		var s float64
		if _, err := fmt.Sscanf(line, "PASS  agent:%d %fs", &shard, &s); err == nil {
			seconds[shard] = s
		}
	}
	return seconds
}

func TestAgentShardsReportsPerShardWallTime(t *testing.T) {
	cfg, stdout, stderr, _ := e2eConfig(t)
	t.Setenv("SHARD_FIXTURE_SLOW", "1")

	// The survey sees one ~0.4s test and five ~0ms tests, so LPT isolates
	// the slow one in its own shard; the other shard must report its OWN
	// short wall time, not the slow shard's.
	if rc := runShards(cfg); rc != 0 {
		t.Fatalf("run rc = %d\nstdout:\n%s\nstderr:\n%s", rc, stdout, stderr)
	}
	seconds := passLineSeconds(t, stdout.String())
	if len(seconds) != 2 {
		t.Fatalf("expected 2 PASS lines with times, got %v in:\n%s", seconds, stdout)
	}
	slow, fast := seconds[0], seconds[1]
	if fast > slow {
		slow, fast = fast, slow
	}
	if slow < 0.4 {
		t.Fatalf("no shard reports the slow test's wall time: %v", seconds)
	}
	if fast >= 0.4 {
		t.Fatalf("the fast shard reports the slow shard's clock (%v): per-shard wall time is not per-shard", seconds)
	}
}

func TestAgentShardsFailingShardRetainsEvidence(t *testing.T) {
	cfg, stdout, stderr, tmp := e2eConfig(t)
	cfg.noSurvey = true
	t.Setenv("SHARD_FIXTURE_FAIL", "beta")

	rc := runShards(cfg)
	if rc != 1 {
		t.Fatalf("failing run rc = %d, want 1\nstdout:\n%s\nstderr:\n%s", rc, stdout, stderr)
	}
	out := stdout.String()
	for _, want := range []string{
		"FAIL  agent:",
		"=== failing shard output ===",
		"----- agent:",
		"--- FAIL: TestFixtureBeta",
		"full logs: ",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("failing run stdout missing %q:\n%s", want, out)
		}
	}
	// The script printed the pointer twice — stdout in the replay block,
	// stderr from cleanup — and consumers read either. Preserved wart.
	if !strings.Contains(stderr.String(), "full logs: ") {
		t.Fatalf("failing run stderr missing retained-logs pointer:\n%s", stderr)
	}
	left := scratchLeftovers(t, tmp)
	if len(left) != 1 {
		t.Fatalf("failing run retained %v, want exactly one scratch dir", left)
	}
	logs, err := filepath.Glob(filepath.Join(left[0], "shard*.log"))
	if err != nil || len(logs) != 2 {
		t.Fatalf("retained dir holds %v, want two shard logs", logs)
	}
	if got, want := replayedShards(t, out), verdictShards(t, out, "FAIL"); !slices.Equal(got, want) {
		t.Fatalf("replay block covered shards %v, want exactly the FAIL verdicts %v:\n%s", got, want, out)
	}
}

// verdictShards returns the shard indices reported with a verdict, in the
// order the run reported them.
func verdictShards(t *testing.T, out, verdict string) []int {
	t.Helper()
	var shards []int
	for line := range strings.SplitSeq(out, "\n") {
		var shard int
		if _, err := fmt.Sscanf(line, verdict+"  agent:%d", &shard); err == nil {
			shards = append(shards, shard)
		}
	}
	return shards
}

// replayedShards returns the shard indices whose logs appear under the
// failing-shard banner, in order.
func replayedShards(t *testing.T, out string) []int {
	t.Helper()
	_, block, found := strings.Cut(out, "=== failing shard output ===")
	if !found {
		return nil
	}
	var shards []int
	for line := range strings.SplitSeq(block, "\n") {
		var shard int
		if _, err := fmt.Sscanf(line, "----- agent:%d -----", &shard); err == nil {
			shards = append(shards, shard)
		}
	}
	return shards
}

// shardHolding reports which shard was assigned a test, read from the names
// files the run left in its scratch directory.
func shardHolding(t *testing.T, logdir, test string) int {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(logdir, "shard*.names"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no shard names files in %s: %v", logdir, err)
	}
	for _, file := range names {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		if !slices.Contains(strings.Fields(string(data)), test) {
			continue
		}
		var shard int
		if _, err := fmt.Sscanf(filepath.Base(file), "shard%d.names", &shard); err != nil {
			t.Fatalf("parsing shard index from %s: %v", file, err)
		}
		return shard
	}
	t.Fatalf("no shard was assigned %s (files: %v)", test, names)
	return -1
}

// TestAgentShardsReplayIsByVerdictNotMarker pins both directions of the
// failing-shard replay. A shard can fail with no `go test` marker anywhere in
// its log — a build error, an os.Exit, an OOM kill — and selecting logs by
// marker dropped exactly the verdicts with the most to explain (kata mjzx,
// fixed in run-module-tests.sh for the same reason). The other direction
// matters just as much: a green shard whose output happens to start a line
// with FAIL must stay out of the block.
func TestAgentShardsReplayIsByVerdictNotMarker(t *testing.T) {
	cfg, stdout, stderr, tmp := e2eConfig(t)
	cfg.noSurvey = true
	t.Setenv("SHARD_FIXTURE_EXIT", "beta")
	t.Setenv("SHARD_FIXTURE_NOISE", "1")

	rc := runShards(cfg)
	if rc != 1 {
		t.Fatalf("markerless failure rc = %d, want 1\nstdout:\n%s\nstderr:\n%s", rc, stdout, stderr)
	}
	out := stdout.String()
	failed := verdictShards(t, out, "FAIL")
	if len(failed) != 1 {
		t.Fatalf("expected exactly one FAIL verdict, got %v:\n%s", failed, out)
	}
	if got := replayedShards(t, out); !slices.Equal(got, failed) {
		t.Fatalf("replay block covered shards %v, want exactly the FAIL verdicts %v:\n%s", got, failed, out)
	}
	if !strings.Contains(out, "fixture-beta exiting hard") {
		t.Fatalf("the markerless failure's own output was not replayed:\n%s", out)
	}

	left := scratchLeftovers(t, tmp)
	if len(left) != 1 {
		t.Fatalf("markerless failure retained %v, want exactly one scratch dir", left)
	}
	noisy := shardHolding(t, left[0], "TestFixtureGamma")
	if noisy == failed[0] {
		t.Fatalf("fixture partition put the noisy test in the failing shard (%d); this run cannot pin green exclusion", noisy)
	}
	if strings.Contains(out, "fixture-gamma is green and only looks red") {
		t.Fatalf("green shard %d was replayed because its output looks like a verdict:\n%s", noisy, out)
	}
}

// TestAgentShardsRedSurveyFailsLoudly pins what a red survey prints. The
// survey has no per-shard verdict to sort by, so its excerpt is the whole
// diagnosis -- and a CI job summary sees only that excerpt, because the log it
// points at is runner-local and dies with the runner. The failing test's own
// assertion text therefore has to be in it (issue #2121): a marker line names
// the test and never says why it failed.
func TestAgentShardsRedSurveyFailsLoudly(t *testing.T) {
	cfg, stdout, stderr, tmp := e2eConfig(t)
	t.Setenv("SHARD_FIXTURE_FAIL", "beta")

	rc := runShards(cfg)
	if rc != 1 {
		t.Fatalf("red-survey run rc = %d, want 1\nstdout:\n%s\nstderr:\n%s", rc, stdout, stderr)
	}
	errOut := stderr.String()
	for _, want := range []string{
		"agent-shards: the survey pass failed — the suite is red",
		"failing as instructed by SHARD_FIXTURE_FAIL",
		"--- FAIL: TestFixtureBeta",
		"full log: ",
	} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("red-survey stderr missing %q:\n%s", want, errOut)
		}
	}
	// The assertion is read as the block it explains, so it must print with
	// its verdict rather than apart from it.
	assertion := strings.Index(errOut, "failing as instructed by SHARD_FIXTURE_FAIL")
	verdict := strings.Index(errOut, "--- FAIL: TestFixtureBeta")
	if assertion > verdict {
		t.Fatalf("the assertion printed after the verdict it explains:\n%s", errOut)
	}
	if len(scratchLeftovers(t, tmp)) != 1 {
		t.Fatalf("red survey should retain its scratch for diagnosis")
	}
}

// TestAgentShardsSkipReachesTheShardsToo pins AGENT_SHARD_SKIP's contract:
// the regex reaches the survey and every shard, so the test it names does not
// run on either path. The survey alone was not enough -- a cached survey means
// no survey runs, and the shards were then given the very test the operator
// had asked to skip.
func TestAgentShardsSkipReachesTheShardsToo(t *testing.T) {
	t.Setenv("SHARD_FIXTURE_FAIL", "beta")

	surveyed, stdout, stderr, _ := e2eConfig(t)
	surveyed.skip = "^TestFixtureBeta$"
	if rc := runShards(surveyed); rc != 0 {
		t.Fatalf("surveyed run with the red test skipped: rc = %d, want 0\nstdout:\n%s\nstderr:\n%s", rc, stdout, stderr)
	}

	// The path that used to run the skipped test: no survey, so nothing had
	// filtered it out before the shards were packed. The shards are given the
	// skip themselves now, which is what makes the knob mean the same thing on
	// a cache hit as on a cold run.
	unsurveyed, stdout2, stderr2, _ := e2eConfig(t)
	unsurveyed.skip = "^TestFixtureBeta$"
	unsurveyed.noSurvey = true
	if rc := runShards(unsurveyed); rc != 0 {
		t.Fatalf("unsurveyed run with the red test skipped: rc = %d, want 0\nstdout:\n%s\nstderr:\n%s", rc, stdout2, stderr2)
	}
	if strings.Contains(stdout2.String(), "TestFixtureBeta") {
		t.Fatalf("the skipped test ran anyway:\n%s", stdout2)
	}
}

// TestAgentShardsBoundsTotalShardConcurrency is the issue #1191 regression:
// each shard is one OS process, and the runner used to start all of them at
// once. AGENT_SHARD_PARALLEL therefore bounded only the tests within a shard,
// and a one-CPU cgroup still got one live process per shard.
//
// The fixture reports the peak population of live shard binaries and waits for
// its peers' markers rather than sleeping a fixed time, so the uncapped case
// reaches both shards' markers and the capped case cannot. A capped run is
// expected to write timeout markers -- that is the second shard being held back
// -- while an uncapped run must not, which is what keeps a probe that simply
// failed to measure from passing as a low-concurrency observation.
func TestAgentShardsBoundsTotalShardConcurrency(t *testing.T) {
	for _, tc := range []struct {
		name        string
		concurrency int
		wantPeak    int
		wantTimeout bool
	}{
		{"uncapped runs every shard at once", 0, 2, false},
		{"capped serializes the shards", 1, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, stdout, stderr, _ := e2eConfig(t)
			cfg.noSurvey = true
			cfg.concurrency = tc.concurrency
			liveDir := t.TempDir()
			t.Setenv("SHARD_FIXTURE_LIVE_DIR", liveDir)
			t.Setenv("SHARD_FIXTURE_LIVE_EXPECT", "2")
			if rc := runShards(cfg); rc != 0 {
				t.Fatalf("run rc = %d, want 0\nstdout:\n%s\nstderr:\n%s", rc, stdout, stderr)
			}
			if got := peakLiveShards(t, liveDir); got != tc.wantPeak {
				t.Fatalf("peak concurrent shard processes = %d, want %d\nstdout:\n%s", got, tc.wantPeak, stdout)
			}
			if timeouts := len(globMarkers(t, liveDir, "timeout.*")); (timeouts > 0) != tc.wantTimeout {
				t.Fatalf("timeout markers = %d, wantTimeout %v; the probe never saw its peers\nstdout:\n%s",
					timeouts, tc.wantTimeout, stdout)
			}
		})
	}
}

// globMarkers returns the files matching pattern in dir.
func globMarkers(t *testing.T, dir, pattern string) []string {
	t.Helper()
	markers, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatalf("globbing %s: %v", pattern, err)
	}
	return markers
}

// peakLiveShards is the largest population any shard binary observed while it
// was alive, read from the markers announceLiveShard left behind.
func peakLiveShards(t *testing.T, dir string) int {
	t.Helper()
	seen := globMarkers(t, dir, "seen.*")
	peak := 0
	for _, file := range seen {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			t.Fatalf("parsing %s: %v", file, err)
		}
		if n > peak {
			peak = n
		}
	}
	if peak == 0 {
		t.Fatalf("no shard binary reported its liveness; the probe never ran")
	}
	return peak
}

func TestAgentShardsMissingAgentDirRefuses(t *testing.T) {
	cfg, _, stderr, _ := e2eConfig(t)
	cfg.pkgDir = filepath.Join(t.TempDir(), "no-such-module")
	if rc := runShards(cfg); rc != 2 {
		t.Fatalf("missing agent dir rc = %d, want 2", rc)
	}
	if !strings.Contains(stderr.String(), "agent-shards: no agent dir") {
		t.Fatalf("missing agent dir not explained:\n%s", stderr)
	}
}

// buildEvenerDev compiles the real evener-dev binary (whose `dev` subcommand
// runs agent-shards) for signal-delivery scenarios.
func buildEvenerDev(t *testing.T) string {
	t.Helper()
	isolateToolchainEnv(t)
	bin := filepath.Join(t.TempDir(), "evener-dev")
	cmd := exec.Command("go", "build", "-o", bin, "../evener-dev/bin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building evener-dev: %v\n%s", err, out)
	}
	return bin
}

func TestServeDevUsageAndUnknownSubcommand(t *testing.T) {
	bin := buildEvenerDev(t)
	out, err := exec.Command(bin, "dev").CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("bare evener dev exit = %v, want 2", err)
	}
	if !strings.Contains(string(out), "usage: evener dev") || !strings.Contains(string(out), "agent-shards") {
		t.Fatalf("usage text missing:\n%s", out)
	}
	out, err = exec.Command(bin, "dev", "no-such-subcommand").CombinedOutput()
	exit = nil
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("unknown subcommand exit = %v, want 2", err)
	}
	if !strings.Contains(string(out), `unknown subcommand "no-such-subcommand"`) {
		t.Fatalf("unknown subcommand not named:\n%s", out)
	}
}

func TestAgentShardsEnvValidation(t *testing.T) {
	bin := buildEvenerDev(t)
	workRoot := t.TempDir()
	if err := os.Symlink(fixtureModule(t), filepath.Join(workRoot, "agent")); err != nil {
		t.Fatalf("linking fixture: %v", err)
	}
	for _, tc := range []struct{ name, value string }{
		{"AGENT_SHARD_COUNT", "banana"},
		{"AGENT_SHARD_COUNT", "0"},
		{"AGENT_SHARD_PARALLEL", "-3"},
		{"AGENT_SHARD_CONCURRENCY", "banana"},
		{"AGENT_SHARD_CONCURRENCY", "-1"},
	} {
		cmd := exec.Command(bin, "dev", "agent-shards")
		cmd.Dir = workRoot
		cmd.Env = append(os.Environ(), "TMPDIR="+t.TempDir(), tc.name+"="+tc.value)
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Fatalf("%s=%s exit = %v, want 1", tc.name, tc.value, err)
		}
		if !strings.Contains(string(out), tc.name) {
			t.Fatalf("%s=%s not named in error:\n%s", tc.name, tc.value, out)
		}
	}
}

// TestHubShardsReadsItsOwnVariablesAndPackage pins the hub-shards entry point:
// it shards cmd/evener-hub under the repository root, reads HUB_SHARD_* rather
// than the agent's variables, and names itself "hub" in its verdicts, so a gate
// running both shard sets side by side can tell their lines apart.
func TestHubShardsReadsItsOwnVariablesAndPackage(t *testing.T) {
	bin := buildEvenerDev(t)
	workRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workRoot, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fixtureModule(t), filepath.Join(workRoot, "cmd", "evener-hub")); err != nil {
		t.Fatalf("linking fixture: %v", err)
	}
	run := func(env ...string) (string, error) {
		cmd := exec.Command(bin, "dev", "hub-shards")
		cmd.Dir = workRoot
		cmd.Env = append(os.Environ(), append([]string{"TMPDIR=" + t.TempDir(), "HUB_SHARD_CACHE_DIR=" + t.TempDir()}, env...)...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	for _, tc := range []struct{ name, value string }{
		{"HUB_SHARD_COUNT", "banana"},
		{"HUB_SHARD_PARALLEL", "-3"},
		{"HUB_SHARD_CONCURRENCY", "-1"},
	} {
		out, err := run(tc.name + "=" + tc.value)
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Fatalf("%s=%s exit = %v, want 1", tc.name, tc.value, err)
		}
		if !strings.Contains(out, "hub-shards: ") || !strings.Contains(out, tc.name) {
			t.Fatalf("%s=%s not refused by hub-shards by name:\n%s", tc.name, tc.value, out)
		}
	}

	// The fixture's beta test fails on demand; HUB_SHARD_SKIP must keep it out.
	out, err := run("HUB_SHARD_COUNT=2", "SHARD_FIXTURE_FAIL=beta", "HUB_SHARD_SKIP=^TestFixtureBeta$", "AGENT_SHARD_COUNT=banana")
	if err != nil {
		t.Fatalf("green hub-shards run failed: %v\n%s", err, out)
	}
	for _, want := range []string{"PASS  hub:0", "PASS  hub:1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("hub-shards output lacks %q:\n%s", want, out)
		}
	}
}
