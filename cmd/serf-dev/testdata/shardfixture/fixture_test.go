// Package shardfixture is the real test module serf-dev's agent-shards tests
// run: a handful of tests with distinct costs, one that fails on command, and
// one that holds until signaled — real work for a real toolchain, no fakes.
package shardfixture

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFixtureAlpha(t *testing.T) { time.Sleep(30 * time.Millisecond) }

func TestFixtureBeta(t *testing.T) {
	if os.Getenv("SHARD_FIXTURE_FAIL") == "beta" {
		t.Fatal("failing as instructed by SHARD_FIXTURE_FAIL")
	}
}

func TestFixtureGamma(t *testing.T) { time.Sleep(10 * time.Millisecond) }

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
