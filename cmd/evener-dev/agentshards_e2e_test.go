package dev

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/devtool/shardrun"
	"primeradiant.com/evener/internal/testfs"
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
	tmp := testfs.CanonicalTempDir(t)
	t.Setenv("TMPDIR", tmp)
	// The fixture module lives under testdata, outside the repo's go.work
	// workspace; the child toolchain must resolve its own go.mod instead.
	t.Setenv("GOWORK", "off")
	isolateToolchainEnv(t)
	var stdout, stderr bytes.Buffer
	return shardsConfig{
		label: "agent", envPrefix: "AGENT", pkgDir: fixtureModule(t),
		count:    2,
		parallel: 1,
		cacheDir: filepath.Join(t.TempDir(), "cache"),
		stdout:   &stdout,
		stderr:   &stderr,
	}, &stdout, &stderr, tmp
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
	if got := verdictShards(t, out, "PASS"); !slices.Equal(got, []int{0, 1}) {
		t.Fatalf("green run PASS verdicts = %v, want shards 0 and 1:\n%s", got, out)
	}
	if shardedTestCount(t, out) == 0 {
		t.Fatalf("green run reported no tests placed into its shards:\n%s", out)
	}
	// Nothing is retained, so no output may point into the scratch root.
	if strings.Contains(out+stderr.String(), tmp) {
		t.Fatalf("green run pointed at retained logs:\n%s\n%s", out, stderr)
	}
	// A green survey prints no failure excerpt: the block exists to explain a
	// failure, and the summary must not grow an empty one.
	if stderr.Len() != 0 {
		t.Fatalf("green run wrote a failure excerpt to stderr:\n%s", stderr)
	}
	if left := scratchLeftovers(t, tmp); len(left) != 0 {
		t.Fatalf("green run left scratch behind: %v", left)
	}
	surveyed := surveyCacheFile(t, cfg.cacheDir)

	// Second run, same cache: the survey must not re-run.
	cfg2 := cfg
	var stdout2, stderr2 bytes.Buffer
	cfg2.stdout, cfg2.stderr = &stdout2, &stderr2
	if rc := runShards(cfg2); rc != 0 {
		t.Fatalf("cached rerun rc = %d\nstdout:\n%s\nstderr:\n%s", rc, &stdout2, &stderr2)
	}
	if !os.SameFile(surveyed, surveyCacheFile(t, cfg.cacheDir)) {
		t.Fatalf("cached rerun surveyed again and replaced the cache:\n%s", &stdout2)
	}
	if got := verdictShards(t, stdout2.String(), "PASS"); !slices.Equal(got, []int{0, 1}) {
		t.Fatalf("cached rerun PASS verdicts = %v, want shards 0 and 1:\n%s", got, &stdout2)
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

// TestAgentShardsRunsEveryTestBinaryUnderATestTimeout pins the tripwire every
// invocation that runs tests carries. A compiled test binary run directly has
// no -test.timeout at all, so a hung test held its shard until something
// outside killed it, and nothing named the test. `go test` gives a package
// binary 10m by default; the survey and every shard get the same, and a
// caller's -timeout replaces it. The fixture records the timeout each
// invocation actually parsed.
func TestAgentShardsRunsEveryTestBinaryUnderATestTimeout(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		want  string
	}{
		{name: "default", want: "10m0s"},
		{name: "caller", flags: []string{"-timeout", "7m"}, want: "7m0s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, stdout, stderr, _ := e2eConfig(t)
			cfg.flags = tc.flags
			dir := t.TempDir()
			t.Setenv("SHARD_FIXTURE_TIMEOUT_DIR", dir)
			if rc := runShards(cfg); rc != 0 {
				t.Fatalf("runShards = %d, want 0\nstdout:\n%s\nstderr:\n%s", rc, stdout, stderr)
			}
			seen := map[string][]string{}
			records, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("reading recorded timeouts: %v", err)
			}
			for _, record := range records {
				data, err := os.ReadFile(filepath.Join(dir, record.Name()))
				if err != nil {
					t.Fatalf("reading %s: %v", record.Name(), err)
				}
				role, _, _ := strings.Cut(record.Name(), ".")
				seen[role] = append(seen[role], string(data))
			}
			if got := seen["survey"]; !slices.Equal(got, []string{tc.want}) {
				t.Fatalf("survey ran under -test.timeout %q, want [%s]", got, tc.want)
			}
			if got := seen["shard"]; !slices.Equal(got, []string{tc.want, tc.want}) {
				t.Fatalf("shards ran under -test.timeout %q, want %s for each of 2", got, tc.want)
			}
		})
	}
}

// TestAgentShardsCaptureKeepsPerTestVerdictsUnderQuietForwardedFlags pins issue
// #144 review finding 3. The capture appends -test.v so `go tool test2json` can
// recover each test's duration from the shard logs, but the caller's own flags
// used to arrive after it: a forwarded -v=false switched verbosity back off, the
// shard logs carried no `--- PASS:` verdict at all, and the timing check still
// passed on the package terminal events while enforcing no sharded test's
// per-test ceiling. -test.v must win over the forwarded flag, and per-test rows
// must survive the conversion the gate runs (capture_shard_rows).
func TestAgentShardsCaptureKeepsPerTestVerdictsUnderQuietForwardedFlags(t *testing.T) {
	cfg, stdout, stderr, _ := e2eConfig(t)
	cfg.noSurvey = true
	cfg.flags = []string{"-count=1", "-v=false"}
	cfg.jsonDir = t.TempDir()
	cfg.jsonPackage = "shardfixture"
	if rc := runShards(cfg); rc != 0 {
		t.Fatalf("runShards = %d, want 0\nstdout:\n%s\nstderr:\n%s", rc, stdout, stderr)
	}
	logs, err := filepath.Glob(filepath.Join(cfg.jsonDir, "agent-shard*.v"))
	if err != nil {
		t.Fatalf("globbing shard logs: %v", err)
	}
	if len(logs) == 0 {
		t.Fatalf("no shard logs under %s", cfg.jsonDir)
	}
	// Convert exactly as run-module-tests.sh's capture_shard_rows does, then
	// parse: a per-test row exists only if the log carried the verdict line that
	// test2json reads it from.
	var stream bytes.Buffer
	for _, log := range logs {
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatalf("reading %s: %v", log, err)
		}
		cmd := exec.Command("go", "tool", "test2json", "-t", "-p", cfg.jsonPackage)
		cmd.Stdin = bytes.NewReader(data)
		cmd.Stdout = &stream
		cmd.Env = append(os.Environ(), "GOENV=off", "GOFLAGS=")
		if err := cmd.Run(); err != nil {
			t.Fatalf("test2json over %s: %v", log, err)
		}
	}
	rows, err := parseGoTestJSON(strings.NewReader(stream.String()), []string{cfg.jsonPackage})
	if err != nil {
		t.Fatalf("parseGoTestJSON over the quiet run's shard logs: %v", err)
	}
	perTest := false
	for _, row := range rows {
		if strings.HasPrefix(row, "TEST\t"+cfg.jsonPackage+"\t") {
			perTest = true
			break
		}
	}
	if !perTest {
		t.Fatalf("the quiet run produced no per-test rows, so the capture's -test.v did not take precedence over the forwarded -v=false:\nrows:\n%s", strings.Join(rows, "\n"))
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

// shardPhases parses the runner's TIME lines: each phase's wall seconds, and
// the shard phase's per-shard seconds by shard number.
func shardPhases(t *testing.T, out string) (phases map[string]float64, perShard map[int]float64) {
	t.Helper()
	phases, perShard = map[string]float64{}, map[int]float64{}
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "TIME" {
			continue
		}
		phase, ok := strings.CutPrefix(fields[1], "agent:")
		if !ok {
			t.Fatalf("TIME line names another label: %q", line)
		}
		var s float64
		if _, err := fmt.Sscanf(fields[2], "%fs", &s); err != nil {
			t.Fatalf("TIME line has no seconds: %q", line)
		}
		phases[phase] = s
		for _, shard := range fields[3:] {
			var i int
			var secs float64
			if _, err := fmt.Sscanf(shard, "%d:%fs", &i, &secs); err != nil {
				t.Fatalf("TIME line has a malformed shard entry %q: %q", shard, line)
			}
			perShard[i] = secs
		}
	}
	return phases, perShard
}

// TestAgentShardsReportsPhaseTimes pins the phase summary a green run prints:
// the binary build, the test listing, the shard plan (survey or packing) and
// the shard phase, whose line also carries each shard's wall time. A CI log
// keeps only the gate's summary of a green run, so these lines are the record
// of where a lane's time went.
func TestAgentShardsReportsPhaseTimes(t *testing.T) {
	cfg, stdout, stderr, _ := e2eConfig(t)
	t.Setenv("SHARD_FIXTURE_SLOW", "1")

	if rc := runShards(cfg); rc != 0 {
		t.Fatalf("run rc = %d\nstdout:\n%s\nstderr:\n%s", rc, stdout, stderr)
	}
	phases, perShard := shardPhases(t, stdout.String())
	for _, phase := range []string{"build", "list", "plan", "shards"} {
		if _, ok := phases[phase]; !ok {
			t.Fatalf("no TIME line for the %s phase in:\n%s", phase, stdout)
		}
	}
	// The survey runs the slow test once, so the plan phase holds it.
	if phases["plan"] < 0.4 {
		t.Fatalf("plan phase %.2fs does not hold the survey's slow test:\n%s", phases["plan"], stdout)
	}
	if want := passLineSeconds(t, stdout.String()); !maps.Equal(roundSeconds(perShard), roundSeconds(want)) {
		t.Fatalf("shard phase per-shard times %v, want the PASS lines' %v:\n%s", perShard, want, stdout)
	}
	for i, s := range perShard {
		if s > phases["shards"] {
			t.Fatalf("shard %d took %.2fs, longer than the whole shard phase %.2fs:\n%s", i, s, phases["shards"], stdout)
		}
	}
}

// roundSeconds rounds to the hundredths both lines print.
func roundSeconds(in map[int]float64) map[int]float64 {
	out := make(map[int]float64, len(in))
	for k, v := range in {
		out[k] = math.Round(v*100) / 100
	}
	return out
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
		"--- FAIL: TestFixtureBeta",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("failing run stdout missing %q:\n%s", want, out)
		}
	}
	left := scratchLeftovers(t, tmp)
	if len(left) != 1 {
		t.Fatalf("failing run retained %v, want exactly one scratch dir", left)
	}
	// The script printed the pointer twice — stdout in the replay block,
	// stderr from cleanup — and consumers read either. Preserved wart.
	if !strings.Contains(out, left[0]) || !strings.Contains(stderr.String(), left[0]) {
		t.Fatalf("failing run did not point both streams at its retained logs %s:\nstdout:\n%s\nstderr:\n%s", left[0], out, stderr)
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
// its peers' markers rather than sleeping a fixed time. Uncapped, both shards
// reach each other. Capped, the runner itself says when it holds the second
// shard back: its slotWait seam writes a marker the moment a shard has to wait
// for a free slot, and a live shard that sees it records a capped observation
// instead of waiting for a peer that must not come. No time window decides
// either case: a runner that ignored the cap would never wait for a slot, so
// both shards would run and the peak would read 2. A timeout marker means the
// probe could not measure, and fails either case.
func TestAgentShardsBoundsTotalShardConcurrency(t *testing.T) {
	for _, tc := range []struct {
		name        string
		concurrency int
		wantPeak    int
		wantCapped  bool
	}{
		{"uncapped runs every shard at once", 0, 2, false},
		{"capped serializes the shards", 1, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, stdout, stderr, _ := e2eConfig(t)
			cfg.noSurvey = true
			cfg.concurrency = tc.concurrency
			liveDir := t.TempDir()
			runnerWaiting := filepath.Join(liveDir, "runner-waiting")
			cfg.slotWait = func() { _ = os.WriteFile(runnerWaiting, nil, 0o644) }
			t.Setenv("SHARD_FIXTURE_LIVE_DIR", liveDir)
			t.Setenv("SHARD_FIXTURE_LIVE_EXPECT", "2")
			t.Setenv("SHARD_FIXTURE_RUNNER_WAITING", runnerWaiting)
			if rc := runShards(cfg); rc != 0 {
				t.Fatalf("run rc = %d, want 0\nstdout:\n%s\nstderr:\n%s", rc, stdout, stderr)
			}
			if got := peakLiveShards(t, liveDir); got != tc.wantPeak {
				t.Fatalf("peak concurrent shard processes = %d, want %d\nstdout:\n%s", got, tc.wantPeak, stdout)
			}
			if timeouts := len(globMarkers(t, liveDir, "timeout.*")); timeouts > 0 {
				t.Fatalf("timeout markers = %d; the probe could not measure\nstdout:\n%s", timeouts, stdout)
			}
			if capped := len(globMarkers(t, liveDir, "capped.*")); (capped > 0) != tc.wantCapped {
				t.Fatalf("capped markers = %d, wantCapped %v\nstdout:\n%s", capped, tc.wantCapped, stdout)
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
	// The refusal names the directory it looked for, not the runner's label.
	if !strings.Contains(stderr.String(), "agent-shards: no "+cfg.pkgDir+" dir") {
		t.Fatalf("missing agent dir not explained:\n%s", stderr)
	}
}

// buildEvenerDev returns the evener-dev binary, compiled once per package run
// into TestMain's directory: the tests that exec it only need the same binary,
// and linking it again for each cost seconds apiece. The build gets the
// isolated toolchain env explicitly and always builds in the repo workspace:
// whichever test builds first must not pass on its own GOWORK (a fixture
// test's GOWORK=off cannot resolve the workspace's sibling modules). Each
// caller still isolates its own env for the commands it runs next.
func buildEvenerDev(t *testing.T) string {
	t.Helper()
	isolateToolchainEnv(t)
	evenerDevBuild.once.Do(func() {
		bin := filepath.Join(evenerDevBinDir, "evener-dev")
		cmd := exec.Command("go", "build", "-o", bin, "../evener-dev/bin")
		cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool {
			return strings.HasPrefix(kv, "GOWORK=")
		}), "GOENV=off", "GOFLAGS=")
		if out, err := cmd.CombinedOutput(); err != nil {
			evenerDevBuild.err = fmt.Errorf("building evener-dev: %w\n%s", err, out)
			return
		}
		evenerDevBuild.bin = bin
	})
	if evenerDevBuild.err != nil {
		t.Fatal(evenerDevBuild.err)
	}
	return evenerDevBuild.bin
}

var evenerDevBuild struct {
	once sync.Once
	bin  string
	err  error
}

func TestServeDevUsageAndUnknownSubcommand(t *testing.T) {
	code, stdout, stderr := execEvenerDev(t)
	if code != 2 || stdout != "" || stderr == "" {
		t.Fatalf("bare evener dev = exit %d, stdout %q, stderr %q; want 2 with usage on stderr only", code, stdout, stderr)
	}
	code, stdout, stderr = execEvenerDev(t, "no-such-subcommand")
	if code != 2 || stdout != "" {
		t.Fatalf("unknown subcommand = exit %d, stdout %q; want 2 with nothing on stdout", code, stdout)
	}
	if !strings.Contains(stderr, "no-such-subcommand") {
		t.Fatalf("unknown subcommand not named:\n%s", stderr)
	}
}

func TestAgentShardsEnvValidation(t *testing.T) {
	bin := buildEvenerDev(t)
	workRoot := t.TempDir()
	if err := os.Symlink(fixtureModule(t), filepath.Join(workRoot, "agent")); err != nil {
		t.Fatalf("linking fixture: %v", err)
	}
	noSurvey := []string{"AGENT_SHARD_NO_SURVEY=1"}
	for _, tc := range []struct {
		name, value string
		// extra is the rest of the environment the refusal needs.
		extra []string
	}{
		{"AGENT_SHARD_COUNT", "banana", nil},
		{"AGENT_SHARD_COUNT", "0", nil},
		{"AGENT_SHARD_PARALLEL", "-3", nil},
		{"AGENT_SHARD_CONCURRENCY", "banana", nil},
		{"AGENT_SHARD_CONCURRENCY", "-1", nil},
		{"AGENT_SHARD_PART", "banana", noSurvey},
		{"AGENT_SHARD_PART", "2", noSurvey},
		{"AGENT_SHARD_PART", "0/2", noSurvey},
		{"AGENT_SHARD_PART", "3/2", noSurvey},
		{"AGENT_SHARD_PART", "1/0", noSurvey},
		// More parts than shards leaves a part with nothing to run.
		{"AGENT_SHARD_PART", "1/3", append([]string{"AGENT_SHARD_COUNT=2"}, noSurvey...)},
		// Every part must pack the same shards, and a survey is per-runner
		// state: two runners with different caches would pack differently,
		// running some tests twice and others never.
		{"AGENT_SHARD_NO_SURVEY", "", []string{"AGENT_SHARD_PART=1/2"}},
	} {
		cmd := exec.Command(bin, "dev", "agent-shards")
		cmd.Dir = workRoot
		cmd.Env = append(append(os.Environ(), "TMPDIR="+t.TempDir(), "AGENT_SHARD_NO_SURVEY=", tc.name+"="+tc.value), tc.extra...)
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

// TestAgentShardsPartRunsItsShareOfTheShards pins AGENT_SHARD_PART, which
// splits one package's shards across CI runners: every part packs the same
// shards and runs every m-th of them, so the parts together run each test
// exactly once. Shard numbers stay those of the whole packing, so two parts'
// logs never share a name.
func TestAgentShardsPartRunsItsShareOfTheShards(t *testing.T) {
	bin := buildEvenerDev(t)
	workRoot := t.TempDir()
	if err := os.Symlink(fixtureModule(t), filepath.Join(workRoot, "agent")); err != nil {
		t.Fatalf("linking fixture: %v", err)
	}
	run := func(env ...string) string {
		t.Helper()
		cmd := exec.Command(bin, "dev", "agent-shards")
		cmd.Dir = workRoot
		cmd.Env = append(os.Environ(), append([]string{
			"TMPDIR=" + t.TempDir(), "AGENT_SHARD_CACHE_DIR=" + t.TempDir(),
			"GOWORK=off", "GOENV=off", "GOFLAGS=",
			"AGENT_SHARD_COUNT=4", "AGENT_SHARD_NO_SURVEY=1", "AGENT_SHARD_PART=",
		}, env...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("agent-shards %v: %v\n%s", env, err, out)
		}
		return string(out)
	}

	whole := shardedTestCount(t, run())
	if whole == 0 {
		t.Fatal("the whole run placed no tests")
	}
	parted := 0
	for _, tc := range []struct {
		part string
		want []int
	}{
		{"1/2", []int{0, 2}},
		{"2/2", []int{1, 3}},
	} {
		out := run("AGENT_SHARD_PART=" + tc.part)
		if got := verdictShards(t, out, "PASS"); !slices.Equal(got, tc.want) {
			t.Fatalf("part %s ran shards %v, want %v:\n%s", tc.part, got, tc.want, out)
		}
		parted += shardedTestCount(t, out)
	}
	if parted != whole {
		t.Fatalf("the parts ran %d tests between them, the whole run %d", parted, whole)
	}
}

// hubFixtureRoot lays the shard fixture out the way the repository holds
// cmd/evener-hub: a module root with the package in cmd/evener-hub, which is
// where hub-shards builds from and runs in.
func hubFixtureRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	pkg := filepath.Join(root, "cmd", "evener-hub")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := fixtureModule(t)
	for src, dst := range map[string]string{
		"go.mod":          filepath.Join(root, "go.mod"),
		"fixture_test.go": filepath.Join(pkg, "fixture_test.go"),
		"tagged_on.go":    filepath.Join(pkg, "tagged_on.go"),
		"tagged_off.go":   filepath.Join(pkg, "tagged_off.go"),
	} {
		data, err := os.ReadFile(filepath.Join(fixture, src))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// hubFixtureLiveStackRoot is hubFixtureRoot with a real cmd/evener package, the
// layout hub-shards needs to build the live-stack binaries the hub package's
// TestMain would otherwise warm per shard.
func hubFixtureLiveStackRoot(t *testing.T) string {
	t.Helper()
	root := hubFixtureRoot(t)
	dir := filepath.Join(root, "cmd", "evener")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	main := "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"fixture-live-stack\") }\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// hubFixtureToolchainEnv is the child environment a hub-shards run over
// hubFixtureRoot needs, appended after the ambient one so it wins: a private
// TMPDIR and survey cache, and a toolchain that resolves the fixture's own
// go.mod rather than an ambient GOWORK, GOENV or GOFLAGS.
func hubFixtureToolchainEnv(t *testing.T) []string {
	t.Helper()
	return []string{
		"TMPDIR=" + t.TempDir(),
		"HUB_SHARD_CACHE_DIR=" + t.TempDir(),
		"GOWORK=off", "GOENV=off", "GOFLAGS=",
		// A hub-shards run publishes these into its children. A fixture run
		// must decide its own prebuild, not inherit an outer runner's.
		shardrun.PrebuiltBinDirEnv + "=",
		shardrun.PrebuiltBinWarmEnv + "=",
	}
}

// goWrapper puts a `go` first on a child PATH that records every invocation and
// runs the real toolchain. It proves which builds a run attempted.
func goWrapper(t *testing.T) (dir, logPath string) {
	t.Helper()
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	dir = t.TempDir()
	logPath = filepath.Join(t.TempDir(), "go.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellquote.Literal(logPath) + "\nexec " + shellquote.Literal(realGo) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, logPath
}

// countBuilds counts the wrapper's invocations that built target.
func countBuilds(log []byte, target string) int {
	builds := 0
	for line := range strings.SplitSeq(string(log), "\n") {
		if strings.HasPrefix(line, "build ") && strings.HasSuffix(line, " "+target) {
			builds++
		}
	}
	return builds
}

// TestHubShardsReadsItsOwnVariablesAndPackage pins the hub-shards entry point:
// it shards cmd/evener-hub under the repository root, reads HUB_SHARD_* rather
// than the agent's variables, and names itself "hub" in its verdicts, so a gate
// running both shard sets side by side can tell their lines apart.
func TestHubShardsReadsItsOwnVariablesAndPackage(t *testing.T) {
	bin := buildEvenerDev(t)
	workRoot := hubFixtureRoot(t)
	runArgs := func(args []string, env ...string) (string, error) {
		cmd := exec.Command(bin, append([]string{"dev", "hub-shards"}, args...)...)
		cmd.Dir = workRoot
		cmd.Env = append(os.Environ(), append(hubFixtureToolchainEnv(t), env...)...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	run := func(env ...string) (string, error) { return runArgs(nil, env...) }

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

	// Run outside a checkout, the refusal names the package directory it
	// looked for.
	missing := exec.Command(bin, "dev", "hub-shards")
	missing.Dir = t.TempDir()
	missing.Env = append(os.Environ(), hubFixtureToolchainEnv(t)...)
	if out, err := missing.CombinedOutput(); err == nil || !strings.Contains(string(out), "hub-shards: no "+filepath.Join("cmd", "evener-hub")+" dir") {
		t.Fatalf("hub-shards outside a checkout = %v, want a refusal naming cmd/evener-hub:\n%s", err, out)
	}

	// A refused flag points at the hub's own variable, never the agent's.
	out, err := runArgs([]string{"-skip", "^TestFixtureBeta$"})
	if err == nil || !strings.Contains(out, "HUB_SHARD_SKIP") || strings.Contains(out, "AGENT_") {
		t.Fatalf("hub-shards -skip = %v, want a refusal naming HUB_SHARD_SKIP and no AGENT_ variable:\n%s", err, out)
	}

	// The fixture's beta test fails on demand; HUB_SHARD_SKIP must keep it out.
	out, err = run("HUB_SHARD_COUNT=2", "SHARD_FIXTURE_FAIL=beta", "HUB_SHARD_SKIP=^TestFixtureBeta$", "AGENT_SHARD_COUNT=banana")
	if err != nil {
		t.Fatalf("green hub-shards run failed: %v\n%s", err, out)
	}
	for _, want := range []string{"PASS  hub:0", "PASS  hub:1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("hub-shards output lacks %q:\n%s", want, out)
		}
	}
}

// TestHubShardsBuildsTheLiveStackBinariesOnce pins the follow-up to #5695:
// hub-shards builds ./cmd/evener for the whole run, and every shard inherits
// the directory through the environment instead of letting the hub TestMain
// warm its own copy.
func TestHubShardsBuildsTheLiveStackBinariesOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the go wrapper this test counts through is a POSIX shell script")
	}
	bin := buildEvenerDev(t)
	workRoot := hubFixtureLiveStackRoot(t)
	liveDir := t.TempDir()
	wrapperDir, goLog := goWrapper(t)

	cmd := exec.Command(bin, "dev", "hub-shards")
	cmd.Dir = workRoot
	cmd.Env = append(os.Environ(), append(hubFixtureToolchainEnv(t),
		"HUB_SHARD_COUNT=2",
		// The live-shard announcement below needs both shards alive at once;
		// do not inherit the gate's busy-host concurrency cap.
		"HUB_SHARD_CONCURRENCY=2",
		"SHARD_FIXTURE_LIVE_STACK=1",
		"SHARD_FIXTURE_LIVE_DIR="+liveDir,
		"SHARD_FIXTURE_LIVE_EXPECT=2",
		"PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hub-shards over the live-stack fixture failed: %v\n%s", err, out)
	}
	data, err := os.ReadFile(goLog)
	if err != nil {
		t.Fatalf("reading the go wrapper log: %v", err)
	}
	if builds := countBuilds(data, shardrun.PrebuiltBinTarget); builds != 1 {
		t.Fatalf("go build ./cmd/evener ran %d times, want once for the run:\n%s", builds, data)
	}
	seen, err := filepath.Glob(filepath.Join(liveDir, "seen.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 {
		t.Fatalf("expected both fixture shards to start, got %v:\n%s", seen, out)
	}
	timeouts, err := filepath.Glob(filepath.Join(liveDir, "timeout.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(timeouts) != 0 {
		t.Fatalf("fixture shards did not rendezvous: %v\n%s", timeouts, out)
	}
}

// TestHubShardsNamesALiveStackBuildFailure keeps a broken ./cmd/evener from
// surfacing as an opaque shard failure: the run exits nonzero at the build and
// no fixture test runs.
func TestHubShardsNamesALiveStackBuildFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the go wrapper this test counts through is a POSIX shell script")
	}
	bin := buildEvenerDev(t)
	workRoot := hubFixtureLiveStackRoot(t)
	broken := "package main\n\nfunc main() { thisSymbolDoesNotExist() }\n"
	if err := os.WriteFile(filepath.Join(workRoot, "cmd", "evener", "main.go"), []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	liveDir := t.TempDir()
	wrapperDir, goLog := goWrapper(t)
	cmd := exec.Command(bin, "dev", "hub-shards")
	cmd.Dir = workRoot
	cmd.Env = append(os.Environ(), append(hubFixtureToolchainEnv(t),
		"HUB_SHARD_COUNT=2",
		"SHARD_FIXTURE_LIVE_DIR="+liveDir,
		"SHARD_FIXTURE_LIVE_EXPECT=1",
		"PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("a broken cmd/evener did not fail the run:\n%s", out)
	}
	data, err := os.ReadFile(goLog)
	if err != nil {
		t.Fatal(err)
	}
	if builds := countBuilds(data, shardrun.PrebuiltBinTarget); builds != 1 {
		t.Fatalf("the live-stack build was not attempted (builds=%d):\n%s\n%s", builds, data, out)
	}
	seen, err := filepath.Glob(filepath.Join(liveDir, "seen.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatalf("a fixture shard started despite the live-stack build failing: %v\n%s", seen, out)
	}
}

// TestHubShardsResolvesBuildFlagPathsFromTheModuleRoot pins where hub-shards
// builds: cmd/evener-hub is a package of the root module, and the gate's root
// `go test` resolves path-valued build flags (-overlay, -modfile, -pgo) from
// the repository root, so hub-shards must too. The fixture is laid out like
// the repository, and a relative -overlay adds a test that fails with a marker
// only when the overlay reached the build.
func TestHubShardsResolvesBuildFlagPathsFromTheModuleRoot(t *testing.T) {
	bin := buildEvenerDev(t)
	workRoot := hubFixtureRoot(t)
	pkg := filepath.Join(workRoot, "cmd", "evener-hub")
	overlaid := filepath.Join(workRoot, "overlaid_test.go.src")
	if err := os.WriteFile(overlaid, []byte("package shardfixture\n\nimport \"testing\"\n\nfunc TestOverlayReachedTheBuild(t *testing.T) { t.Fatal(\"OVERLAY-REACHED-THE-BUILD\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	overlay := fmt.Sprintf(`{"Replace":{%q:%q}}`, filepath.Join(pkg, "overlaid_test.go"), overlaid)
	if err := os.WriteFile(filepath.Join(workRoot, "overlay.json"), []byte(overlay), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "dev", "hub-shards", "-overlay=overlay.json")
	cmd.Dir = workRoot
	// PWD as a shell would set it: exec leaves the inherited PWD alone once Env
	// is set, and hub-shards and the go command it starts then name the
	// directory by its canonical path, which on macOS (/var -> /private/var)
	// misses the overlay's keys.
	cmd.Env = append(os.Environ(), append(hubFixtureToolchainEnv(t), "HUB_SHARD_COUNT=2", "PWD="+workRoot)...)
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "OVERLAY-REACHED-THE-BUILD") {
		t.Fatalf("a repository-relative -overlay did not reach the hub build:\n%s", out)
	}
}

// TestCLIShardsReadsItsOwnVariables pins the cli-shards entry point: it shards
// cmd/evener and reads CLI_SHARD_* (the shared machinery is covered by the hub
// and agent tests), refusing a bad value by its own name.
func TestCLIShardsReadsItsOwnVariables(t *testing.T) {
	bin := buildEvenerDev(t)
	cmd := exec.Command(bin, "dev", "cli-shards")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), append(hubFixtureToolchainEnv(t), "CLI_SHARD_COUNT=banana")...)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), "cli-shards: CLI_SHARD_COUNT") {
		t.Fatalf("cli-shards CLI_SHARD_COUNT=banana = %v, want exit 1 naming CLI_SHARD_COUNT:\n%s", err, out)
	}
	missing := exec.Command(bin, "dev", "cli-shards")
	missing.Dir = t.TempDir()
	missing.Env = append(os.Environ(), hubFixtureToolchainEnv(t)...)
	if out, err := missing.CombinedOutput(); err == nil || !strings.Contains(string(out), "cli-shards: no "+filepath.Join("cmd", "evener")+" dir") {
		t.Fatalf("cli-shards outside a checkout = %v, want a refusal naming cmd/evener:\n%s", err, out)
	}
}
