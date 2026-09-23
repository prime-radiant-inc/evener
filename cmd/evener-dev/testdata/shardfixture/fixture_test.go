// Package shardfixture is the real test module evener-dev's agent-shards tests
// run: a handful of tests with distinct costs, one that fails on command, one
// that dies without a `go test` failure marker, and one that holds until
// signaled — real work for a real toolchain, no fakes.
package shardfixture

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain picks up the shard's -test.run regex from EVENER_SHARD_RUN_FILE
// when set, so agent-shards never puts the regex on the command line.
func TestMain(m *testing.M) {
	flag.Parse()
	if err := configureShardRunFile(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "shardfixture TestMain: %v\n", err)
		os.Exit(2)
	}
	done := announceLiveShard()
	code := m.Run()
	if done != nil {
		done()
	}
	os.Exit(code)
}

// liveShardTimeout bounds how long a shard binary waits for the peers the run
// should be running beside, or for word that the runner is holding them back.
// It only has to cover process startup, and a passing run never spends it: it
// is the tripwire for a probe that could not measure.
const liveShardTimeout = 5 * time.Second

// announceLiveShard, when SHARD_FIXTURE_LIVE_DIR is set, records this test
// binary as a live process for as long as it runs. A run executes one binary
// per shard, so no single binary can see how many of its peers are alive.
//
// Each process drops a live.<pid> marker and then waits for
// SHARD_FIXTURE_LIVE_EXPECT peers to appear, up to liveShardTimeout, recording
// the largest population it actually saw. Waiting on the peers' own markers,
// rather than sleeping a fixed time, is what makes the observation
// deterministic: a run that starts every shard at once reaches the expected
// count, and a capped run's observation is bounded by the cap rather than by
// when the scheduler happened to run each process. Processes that reach the
// expected count rendezvous on ready.<pid> before any of them exits, so a peer
// cannot remove its live marker before a slower peer has sampled it. A process
// that sees SHARD_FIXTURE_RUNNER_WAITING first writes capped.<pid> instead:
// the runner's cap engaged, so the full count will not arrive. Only a process
// that sees neither its peers nor that signal writes a timeout.<pid> marker, so
// the caller can tell a real observation from a probe that could not measure.
//
// The observed population goes to seen.<pid> and the marker is removed on exit,
// so a finished process does not count as live. Inert unless the test asks for
// it.
func announceLiveShard() func() {
	dir := os.Getenv("SHARD_FIXTURE_LIVE_DIR")
	if dir == "" {
		return nil
	}
	// Only a shard invocation carries the run file. The runner also runs this
	// binary to list tests and (unless skipped) to survey; those are not shards
	// and must not join the population the caller is measuring.
	if os.Getenv("EVENER_SHARD_RUN_FILE") == "" {
		return nil
	}
	expect, err := strconv.Atoi(os.Getenv("SHARD_FIXTURE_LIVE_EXPECT"))
	if err != nil || expect < 1 {
		return nil
	}
	pid := os.Getpid()
	live := filepath.Join(dir, fmt.Sprintf("live.%d", pid))
	if err := os.WriteFile(live, []byte("live\n"), 0o644); err != nil {
		return nil
	}
	// SHARD_FIXTURE_RUNNER_WAITING names the file the caller writes when the
	// runner first blocks for a free slot, and it stays for the rest of the
	// run. It says the cap engaged in this run, not that this particular shard
	// is the one being waited on: once the runner has held a shard back, no
	// shard of the run can count on every peer being live at once, so each one
	// alive then stops waiting and records a capped marker instead of timing
	// out. The caller's peak count is what shows how many actually overlapped.
	runnerWaiting := os.Getenv("SHARD_FIXTURE_RUNNER_WAITING")
	observed := 1 // this process is live
	deadline := time.Now().Add(liveShardTimeout)
	reached := false
	capped := false
	for time.Now().Before(deadline) {
		if n := countMarkers(dir, "live.*"); n > observed {
			observed = n
		}
		if observed >= expect {
			reached = true
			break
		}
		if runnerWaiting != "" {
			if _, err := os.Stat(runnerWaiting); err == nil {
				capped = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if capped {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("capped.%d", pid)), []byte("capped\n"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("seen.%d", pid)),
			[]byte(strconv.Itoa(observed)+"\n"), 0o644)
		return func() { _ = os.Remove(live) }
	}
	if reached {
		// Rendezvous before anyone leaves: a peer that reached the barrier and
		// exited immediately would remove its live marker before a slower peer
		// sampled it. Holding every process until all have arrived is what makes
		// the observation deterministic rather than a race against exit.
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("ready.%d", pid)), []byte("ready\n"), 0o644)
		for time.Now().Before(deadline) && countMarkers(dir, "ready.*") < expect {
			time.Sleep(20 * time.Millisecond)
		}
		reached = countMarkers(dir, "ready.*") >= expect
	}
	if !reached {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("timeout.%d", pid)), []byte("timeout\n"), 0o644)
	}
	_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("seen.%d", pid)),
		[]byte(strconv.Itoa(observed)+"\n"), 0o644)
	return func() { _ = os.Remove(live) }
}

// countMarkers is how many files matching pattern exist in dir.
func countMarkers(dir, pattern string) int {
	markers, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return 0
	}
	return len(markers)
}

func configureShardRunFile() error {
	runFile, supplied := os.LookupEnv("EVENER_SHARD_RUN_FILE")
	if !supplied {
		return nil
	}
	data, err := os.ReadFile(runFile)
	if err != nil {
		return fmt.Errorf("EVENER_SHARD_RUN_FILE %q: read failed: %w", runFile, err)
	}
	pattern := strings.TrimSpace(string(data))
	if pattern == "" {
		return fmt.Errorf("EVENER_SHARD_RUN_FILE %q: run regex is empty", runFile)
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return fmt.Errorf("EVENER_SHARD_RUN_FILE %q: invalid run regex: %w", runFile, err)
	}
	if err := flag.Set("test.run", pattern); err != nil {
		return fmt.Errorf("EVENER_SHARD_RUN_FILE %q: setting test.run failed: %w", runFile, err)
	}
	return nil
}

// TestFixtureFlagsGate is how agent-shards' e2e test sees whether the caller's
// flags reached the two places they have to reach: -tags the compiler, and the
// test-side flags the invocation of this binary. It is inert unless the test
// asks for it, so every other fixture run is unaffected.
func TestFixtureFlagsGate(t *testing.T) {
	if os.Getenv("SHARDFIXTURE_GATE") != "1" {
		t.Skip("not asked for: this gate belongs to one e2e test")
	}
	if !fixtureBuiltWithTag {
		t.Error("this binary was built without the shardfixturetag tag, so the build did not get the caller's build flags")
	}
	if !testing.Verbose() {
		t.Error("this invocation did not get -test.v, so the shards did not get the caller's test flags")
	}
}

func TestFixtureAlpha(t *testing.T) { time.Sleep(30 * time.Millisecond) }

func TestFixtureBeta(t *testing.T) {
	if os.Getenv("SHARD_FIXTURE_EXIT") == "beta" {
		// A shard that dies with no "--- FAIL"/"FAIL"/"panic:" line anywhere
		// in its log: the build error, os.Exit, and OOM class of failure that
		// marker-matching replay used to drop on the floor.
		fmt.Println("fixture-beta exiting hard as instructed by SHARD_FIXTURE_EXIT")
		os.Exit(3)
	}
	if os.Getenv("SHARD_FIXTURE_FAIL") == "beta" {
		t.Fatal("failing as instructed by SHARD_FIXTURE_FAIL")
	}
}

func TestFixtureGamma(t *testing.T) {
	if os.Getenv("SHARD_FIXTURE_NOISE") != "" {
		// A green test whose output merely looks like a verdict: what
		// marker-matching replay mistook for a failing shard.
		fmt.Println("FAIL: fixture-gamma is green and only looks red")
	}
	time.Sleep(10 * time.Millisecond)
}

func TestFixtureDelta(t *testing.T) {}

// TestFixtureSlow dominates its shard's wall time when armed, so a survey
// isolates it and per-shard timing becomes observable from outside.
func TestFixtureSlow(t *testing.T) {
	if os.Getenv("SHARD_FIXTURE_SLOW") != "" {
		time.Sleep(400 * time.Millisecond)
	}
}

// TestFixtureHold announces itself, then blocks on the hold FIFO until a
// writer arrives or the process is signaled: real held work for the
// interruption and SIGKILL scenarios, with no timers to flake on.
func TestFixtureHold(t *testing.T) {
	dir := os.Getenv("SHARD_FIXTURE_HOLD")
	if dir == "" {
		return
	}
	if os.Getenv("SHARD_FIXTURE_IGNORE_TERM") != "" {
		// A shard that swallows the runner's TERM and keeps holding: the
		// wedged case only a second signal can end. Each TERM is announced
		// before it is dropped, so the test can tell "the runner forwarded
		// it" from "the runner never got there".
		terms := make(chan os.Signal, 1)
		signal.Notify(terms, syscall.SIGTERM)
		go func() {
			for range terms {
				termed := filepath.Join(dir, fmt.Sprintf("termed.%d", os.Getpid()))
				_ = os.WriteFile(termed, []byte("termed\n"), 0o644)
			}
		}()
	}
	ready := filepath.Join(dir, fmt.Sprintf("held.%d", os.Getpid()))
	if err := os.WriteFile(ready, []byte("held\n"), 0o644); err != nil {
		t.Fatalf("announcing hold: %v", err)
	}
	fifo, err := os.Open(filepath.Join(dir, "hold.fifo"))
	if err != nil {
		t.Fatalf("opening hold fifo: %v", err)
	}
	defer func() { _ = fifo.Close() }()
	_, _ = fifo.Read(make([]byte, 1))
}
