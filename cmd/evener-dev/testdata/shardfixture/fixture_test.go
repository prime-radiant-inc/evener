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
	os.Exit(m.Run())
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
