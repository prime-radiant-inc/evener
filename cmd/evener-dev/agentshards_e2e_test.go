package dev

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
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

// e2eConfig is a runShards config over the fixture module with isolated
// TMPDIR and survey cache, capture buffers attached.
func e2eConfig(t *testing.T) (shardsConfig, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	// The fixture module lives under testdata, outside the repo's go.work
	// workspace; the child toolchain must resolve its own go.mod instead.
	t.Setenv("GOWORK", "off")
	resolved, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatalf("resolving TMPDIR fixture: %v", err)
	}
	var stdout, stderr bytes.Buffer
	return shardsConfig{
		agentDir: fixtureModule(t),
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
		"--- FAIL: TestFixtureBeta",
		"full log: ",
	} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("red-survey stderr missing %q:\n%s", want, errOut)
		}
	}
	if len(scratchLeftovers(t, tmp)) != 1 {
		t.Fatalf("red survey should retain its scratch for diagnosis")
	}
}

// TestAgentShardsSkipOnlyReachesTheSurvey pins how far AGENT_SHARD_SKIP
// actually reaches. The script only ever appended -test.skip to the survey
// pass, and this port keeps that: skipping works by leaving a test out of the
// cost table, so a run that does not survey does not skip. Fixing the wart —
// threading the regex into the shard invocations and into the cache key —
// means changing this pin with it.
func TestAgentShardsSkipOnlyReachesTheSurvey(t *testing.T) {
	t.Setenv("SHARD_FIXTURE_FAIL", "beta")

	surveyed, stdout, stderr, _ := e2eConfig(t)
	surveyed.skip = "^TestFixtureBeta$"
	if rc := runShards(surveyed); rc != 0 {
		t.Fatalf("surveyed run with the red test skipped: rc = %d, want 0\nstdout:\n%s\nstderr:\n%s", rc, stdout, stderr)
	}

	unsurveyed, stdout2, stderr2, _ := e2eConfig(t)
	unsurveyed.skip = "^TestFixtureBeta$"
	unsurveyed.noSurvey = true
	if rc := runShards(unsurveyed); rc != 1 {
		t.Fatalf("unsurveyed run: rc = %d, want 1 — AGENT_SHARD_SKIP reaches the shards now, so the interface comment and this pin are both stale\nstdout:\n%s\nstderr:\n%s", rc, stdout2, stderr2)
	}
	if !strings.Contains(stdout2.String(), "--- FAIL: TestFixtureBeta") {
		t.Fatalf("the unsurveyed run failed for some reason other than the unskipped test:\n%s", stdout2)
	}
}

func TestAgentShardsMissingAgentDirRefuses(t *testing.T) {
	cfg, _, stderr, _ := e2eConfig(t)
	cfg.agentDir = filepath.Join(t.TempDir(), "no-such-module")
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
	bin := filepath.Join(t.TempDir(), "evener-dev")
	cmd := exec.Command("go", "build", "-o", bin, "../evener-dev/bin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building evener-dev: %v\n%s", err, out)
	}
	return bin
}

// startHeldRun starts the evener-dev binary against the fixture with one shard
// held open, and returns the running command, the held test-binary pid, the
// scratch TMPDIR, and the hold directory. extraEnv arms fixture behaviour in
// the shard processes.
func startHeldRun(t *testing.T, extraEnv ...string) (*exec.Cmd, int, string, string) {
	t.Helper()
	bin := buildEvenerDev(t)
	tmp := t.TempDir()
	resolvedTmp, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatalf("resolving TMPDIR: %v", err)
	}
	holdDir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(holdDir, "hold.fifo"), 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	workRoot := t.TempDir()
	if err := os.Symlink(fixtureModule(t), filepath.Join(workRoot, "agent")); err != nil {
		t.Fatalf("linking fixture as ./agent: %v", err)
	}

	cmd := exec.Command(bin, "dev", "agent-shards")
	cmd.Dir = workRoot
	cmd.Env = append(os.Environ(),
		"TMPDIR="+tmp,
		"GOWORK=off", // the fixture module lives outside the repo workspace
		"SHARD_FIXTURE_HOLD="+holdDir,
		"AGENT_SHARD_COUNT=2",
		"AGENT_SHARD_PARALLEL=1",
		"AGENT_SHARD_NO_SURVEY=1",
		"AGENT_SHARD_CACHE_DIR="+filepath.Join(t.TempDir(), "cache"),
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting held run: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	// Await the hold announcement; the ceiling is a tripwire sized for a
	// cold `go test -c` under load, not a mechanism.
	deadline := time.Now().Add(120 * time.Second)
	for {
		matches, _ := filepath.Glob(filepath.Join(holdDir, "held.*"))
		if len(matches) == 1 {
			var heldPid int
			if _, err := fmt.Sscanf(filepath.Base(matches[0]), "held.%d", &heldPid); err != nil {
				t.Fatalf("parsing held pid from %s: %v", matches[0], err)
			}
			return cmd, heldPid, resolvedTmp, holdDir
		}
		if time.Now().After(deadline) {
			t.Fatalf("held shard never announced; stderr:\n%s", &stderr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// awaitFile waits for a path to appear, failing with label if it never does.
func awaitFile(t *testing.T, path, label string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %s never appeared", label, path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// releaseHold unblocks a held shard by opening the write end of its FIFO.
// Only safe while the shard is still reading: an open with no reader blocks.
func releaseHold(t *testing.T, holdDir string) {
	t.Helper()
	fifo, err := os.OpenFile(filepath.Join(holdDir, "hold.fifo"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("opening hold fifo for release: %v", err)
	}
	_, _ = fifo.WriteString("x")
	_ = fifo.Close()
}

func awaitPidGone(t *testing.T, pid int, label string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: pid %d still alive", label, pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAgentShardsSIGTERMExits143RetainsLogsReapsChildren(t *testing.T) {
	cmd, heldPid, tmp, _ := startHeldRun(t)

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signaling runner: %v", err)
	}
	err := cmd.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 143 {
		t.Fatalf("interrupted runner exit = %v, want exit status 143", err)
	}
	stderr := cmd.Stderr.(*bytes.Buffer).String()
	if !strings.Contains(stderr, "interrupted by SIGTERM") {
		t.Fatalf("interruption not explained on stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "full logs: ") {
		t.Fatalf("interrupted run did not point at retained logs:\n%s", stderr)
	}
	want := filepath.Join(tmp, fmt.Sprintf("agent-test-shards.%d", cmd.Process.Pid))
	if _, statErr := os.Stat(want); statErr != nil {
		t.Fatalf("interrupted run's scratch %s not retained: %v", want, statErr)
	}
	awaitPidGone(t, heldPid, "held shard child after SIGTERM")
}

// TestAgentShardsSecondSignalEndsAWedgedRun pins the second Ctrl-C. A shard
// that ignores TERM leaves the runner waiting on it forever; the script's
// first handler cleared its traps, so the next signal took its default action
// and ended the run. Relaying only one signal makes the runner deaf to every
// signal after the first, and SIGKILL becomes the only way out.
func TestAgentShardsSecondSignalEndsAWedgedRun(t *testing.T) {
	cmd, heldPid, tmp, holdDir := startHeldRun(t, "SHARD_FIXTURE_IGNORE_TERM=1")

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("first SIGTERM: %v", err)
	}
	// The shard announcing the TERM it swallowed proves the runner handled
	// the first signal and forwarded it, so the second is never mistaken for
	// the first.
	awaitFile(t, filepath.Join(holdDir, fmt.Sprintf("termed.%d", heldPid)),
		"held shard never saw the runner's forwarded TERM")

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("second SIGTERM: %v", err)
	}
	// A deaf runner hangs here; the killer bounds the wait and its firing IS
	// the failure.
	killed := make(chan struct{})
	killer := time.AfterFunc(20*time.Second, func() {
		_ = cmd.Process.Signal(syscall.SIGKILL)
		close(killed)
	})
	err := cmd.Wait()
	timely := killer.Stop()
	if !timely {
		<-killed
	}
	// Whatever happened to the runner, the held shard is orphaned now: it
	// ignores TERM, so KILL is what ends it.
	_ = syscall.Kill(heldPid, syscall.SIGKILL)
	awaitPidGone(t, heldPid, "held shard child after the run ended")
	if !timely {
		t.Fatalf("second SIGTERM did not end the runner; it had to be SIGKILLed after 20s (wait: %v)", err)
	}

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 143 {
		t.Fatalf("second SIGTERM: runner exit = %v, want exit status 143", err)
	}
	stderr := cmd.Stderr.(*bytes.Buffer).String()
	if !strings.Contains(stderr, "interrupted by SIGTERM") {
		t.Fatalf("first interruption not explained on stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "SIGTERM again") {
		t.Fatalf("second signal not explained on stderr:\n%s", stderr)
	}
	// The logs the first signal retained are still on disk for diagnosis.
	want := filepath.Join(tmp, fmt.Sprintf("agent-test-shards.%d", cmd.Process.Pid))
	if _, statErr := os.Stat(want); statErr != nil {
		t.Fatalf("second-signal run's scratch %s not retained: %v", want, statErr)
	}
}

func TestAgentShardsSIGKILLLeftoverIsReclaimedByNextRun(t *testing.T) {
	cmd, heldPid, tmp, holdDir := startHeldRun(t)

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("SIGKILLing runner: %v", err)
	}
	_ = cmd.Wait()

	leftover := filepath.Join(tmp, fmt.Sprintf("agent-test-shards.%d", cmd.Process.Pid))
	if _, err := os.Stat(leftover); err != nil {
		t.Fatalf("SIGKILL should leave scratch behind: %v", err)
	}

	// Release the orphaned held child (nothing reparents it once the runner
	// is KILLed), and wait for it to be truly gone before reclaiming.
	releaseHold(t, holdDir)
	awaitPidGone(t, heldPid, "held shard child after release")

	// The next run of the same tool — same TMPDIR, hold disarmed — reclaims
	// the dead runner's scratch and completes green.
	t.Setenv("TMPDIR", tmp)
	t.Setenv("GOWORK", "off")
	var stdout, stderr bytes.Buffer
	cfg := shardsConfig{
		agentDir: fixtureModule(t),
		count:    2,
		parallel: 1,
		noSurvey: true,
		cacheDir: filepath.Join(t.TempDir(), "cache"),
		stdout:   &stdout,
		stderr:   &stderr,
	}
	if rc := runShards(cfg); rc != 0 {
		t.Fatalf("next run rc = %d\nstdout:\n%s\nstderr:\n%s", rc, &stdout, &stderr)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("next run did not reclaim the SIGKILL leftover %s: %v", leftover, err)
	}
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
