//go:build browserguard

package hub

// The background browser driver runs as a real child process that the owning
// test cancels and joins when its budget runs out. This file drives that
// lifetime contract against a real child. The command cancellation setup
// (configureBackgroundDriverCommand) and the owning budget/await composition
// (backgroundDriverBudget, awaitBackgroundDriver) are the same seams the real
// TestBackgroundTranscriptsEntrySliceBrowser uses, so the test and the driver share one
// lifetime definition. The child is this test binary re-executed, so a process
// that installs its own SIGTERM disposition proves the join for real.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/rendezvous"
)

const (
	// backgroundDriverFakeModeEnv switches the re-executed test binary into the
	// fake driver child. backgroundDriverFakeReadyEnv names the marker the
	// unresponsive child writes once its SIGTERM disposition is installed, so
	// the parent cancels a process genuinely willing to ignore the first signal.
	backgroundDriverFakeModeEnv  = "EVENER_BACKGROUND_DRIVER_FAKE_MODE"
	backgroundDriverFakeReadyEnv = "EVENER_BACKGROUND_DRIVER_FAKE_READY"
)

// TestBackgroundDriverLifetimeFakeDriver is the re-executed child, not a real
// test. The lifetime cases launch this binary with the mode env set; without it
// the case skips.
func TestBackgroundDriverLifetimeFakeDriver(t *testing.T) {
	mode := os.Getenv(backgroundDriverFakeModeEnv)
	if mode == "" {
		t.Skip("not the fake driver child")
	}
	switch mode {
	case "ignore-term":
		// Ignore the first termination signal exactly as an unresponsive driver
		// would, report readiness, then wait for the forced stop.
		signal.Ignore(syscall.SIGTERM)
		if ready := os.Getenv(backgroundDriverFakeReadyEnv); ready != "" {
			if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
				t.Fatalf("write fake driver readiness marker: %v", err)
			}
		}
		time.Sleep(10 * time.Minute)
	case "fail-now":
		// An immediate command failure: a non-zero exit before any work.
		os.Exit(7)
	default:
		// A clean completion: the child returns and exits zero.
	}
}

// TestBackgroundDriverLifetime pins the owning driver-lifetime contract: the
// budget keeps the join and cleanup reserves, a driver that finishes on its own
// is not cancelled, an immediate command failure is reported rather than
// mistaken for a timeout, and a driver that ignores SIGTERM is force-killed and
// actually joined, with its exit error preserved.
func TestBackgroundDriverLifetime(t *testing.T) {
	// Not parallel: real child-process cancellation and wall-clock join bounds;
	// two runs would contend for signal delivery.
	t.Run("budget keeps the join and cleanup reserves", func(t *testing.T) {
		lt := backgroundDriverLifetime{
			Configured: 10 * time.Minute,
			Join:       backgroundDriverForceKillDelay,
			Cleanup:    backgroundHelperCleanupReserve,
		}
		now := time.Now()
		if got, ok := backgroundDriverBudget(now.Add(time.Hour), true, lt); !ok || got != lt.Configured {
			t.Fatalf("budget = %s, %v; want the configured cap %s when the deadline is far", got, ok, lt.Configured)
		}
		if got, ok := backgroundDriverBudget(time.Time{}, false, lt); !ok || got != lt.Configured {
			t.Fatalf("budget = %s, %v; want the configured cap %s with no deadline", got, ok, lt.Configured)
		}
		deadline := now.Add(100 * time.Second)
		want := 100*time.Second - lt.Join - lt.Cleanup
		if got, ok := backgroundDriverBudget(deadline, true, lt); !ok || got < want-time.Second || got > want+time.Second {
			t.Fatalf("budget = %s, %v; want about %s (100s before the deadline minus the %s join and %s cleanup reserves)", got, ok, want, lt.Join, lt.Cleanup)
		}
		if got, ok := backgroundDriverBudget(now.Add(lt.Join+lt.Cleanup-time.Second), true, lt); ok {
			t.Fatalf("budget = %s, ok; want no budget when the deadline leaves less than the %s reserves", got, lt.Join+lt.Cleanup)
		}
	})

	t.Run("a driver that completes on its own is not cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done, finished := startBackgroundDriverChild(ctx, t, "clean", "")
		timedOut, joined, exitErr := awaitBackgroundDriver(t, backgroundDriverForceKillDelay, backgroundDriverJoinReserve, cancel, done, finished)
		if timedOut || !joined || exitErr != nil {
			t.Fatalf("clean driver: exitErr=%v timedOut=%v joined=%v; want nil, false, true", exitErr, timedOut, joined)
		}
	})

	t.Run("an immediate command failure is reported, not a timeout", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done, finished := startBackgroundDriverChild(ctx, t, "fail-now", "")
		timedOut, joined, exitErr := awaitBackgroundDriver(t, backgroundDriverForceKillDelay, backgroundDriverJoinReserve, cancel, done, finished)
		if timedOut || !joined || exitErr == nil {
			t.Fatalf("failing driver: exitErr=%v timedOut=%v joined=%v; want a non-nil error, false, true", exitErr, timedOut, joined)
		}
	})

	t.Run("a driver that ignores SIGTERM is force-killed and joined", func(t *testing.T) {
		ready := filepath.Join(t.TempDir(), "ready")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done, finished := startBackgroundDriverChild(ctx, t, "ignore-term", ready)
		waitBackgroundDriverReady(t, ready)
		start := time.Now()
		timedOut, joined, exitErr := awaitBackgroundDriver(t, 100*time.Millisecond, backgroundDriverJoinReserve, cancel, done, finished)
		if !timedOut {
			t.Fatalf("a driver that never exits must report a timeout")
		}
		if !joined {
			t.Fatalf("driver was not joined after cancel within the %s join reserve", backgroundDriverJoinReserve)
		}
		if exitErr == nil {
			t.Fatalf("forced-stop exit error = nil, want the termination error preserved")
		}
		if elapsed := time.Since(start); elapsed > backgroundDriverJoinReserve+5*time.Second {
			t.Fatalf("join took %s, beyond the %s reserve plus margin", elapsed, backgroundDriverJoinReserve)
		}
	})
}

// startBackgroundDriverChild launches the re-executed test binary as a fake
// driver under the real cancellation setup. It kills and reaps the child before
// the test returns, reporting any cleanup failure rather than swallowing it.
func startBackgroundDriverChild(ctx context.Context, t *testing.T, mode, ready string) (<-chan error, <-chan struct{}) {
	t.Helper()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBackgroundDriverLifetimeFakeDriver$")
	configureBackgroundDriverCommand(cmd)
	cmd.Env = append(os.Environ(),
		backgroundDriverFakeModeEnv+"="+mode,
		backgroundDriverFakeReadyEnv+"="+ready,
	)
	done := make(chan error, 1)
	finished := make(chan struct{})
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake driver: %v", err)
	}
	go func() {
		defer close(finished)
		done <- cmd.Wait()
	}()
	t.Cleanup(func() {
		if cmd.Process != nil {
			if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Errorf("kill fake driver: %v", err)
			}
		}
		select {
		case <-finished:
		case <-time.After(backgroundDriverForceKillDelay):
			t.Errorf("fake driver process did not exit before the test returned")
		}
	})
	return done, finished
}

// waitBackgroundDriverReady waits, bounded by the forced-stop delay, for the
// unresponsive child to install its SIGTERM disposition.
func waitBackgroundDriverReady(t *testing.T, ready string) {
	t.Helper()
	deadline := time.Now().Add(backgroundDriverForceKillDelay)
	for {
		if _, err := os.Stat(ready); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake driver did not report readiness at %s", ready)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestBackgroundDriverAdmissionRefusesBeforeLaunch drives the ACTUAL owning
// composition (runOwnedBackgroundDriver) with no budget left. Admission runs
// before the launch, so the launch probe must not run.
func TestBackgroundDriverAdmissionRefusesBeforeLaunch(t *testing.T) {
	lt := backgroundDriverLifetime{
		Configured: backgroundDriverTimeout,
		Join:       backgroundDriverJoinReserve,
		Cleanup:    backgroundHelperCleanupReserve,
	}
	deadline := time.Now().Add(lt.Join + lt.Cleanup - time.Second)
	launched := false
	err := runOwnedBackgroundDriver(t, deadline, true, lt, func(ctx context.Context) (<-chan error, <-chan struct{}) {
		launched = true
		return nil, nil
	})
	if launched {
		t.Fatalf("driver was started with no budget left; admission must precede the launch")
	}
	if err == nil {
		t.Fatalf("an insufficient budget must be refused")
	}
}

// TestBackgroundDriverLaunchRegistersFinishedInsideClosure pins that the real
// launch registers the fixture's finished channel only when the closure runs, so
// a run refused before launch leaves the fixture with no channel for cleanup to
// wait on.
func TestBackgroundDriverLaunchRegistersFinishedInsideClosure(t *testing.T) {
	fixture := &skillGuardFixture{}
	_ = backgroundDriverLaunchFor(t, fixture, "http://127.0.0.1:1", "", "", [2]rendezvous.Entry{})
	if fixture.driverFinished != nil {
		t.Fatalf("building the launch registered a finished channel; registration belongs inside the closure")
	}
}

// TestBackgroundDriverOwnedControls drives the ACTUAL owning composition on its
// admitted paths with real child processes: a clean exit returns nil after the
// driver is reaped, and a driver that ignores SIGTERM is cancelled, force-killed
// and joined before the owning function returns.
func TestBackgroundDriverOwnedControls(t *testing.T) {
	lt := backgroundDriverLifetime{
		Configured: backgroundDriverTimeout,
		Join:       backgroundDriverJoinReserve,
		Cleanup:    backgroundHelperCleanupReserve,
	}

	t.Run("an admitted clean driver completes and is reaped", func(t *testing.T) {
		var finishedCh <-chan struct{}
		err := runOwnedBackgroundDriver(t, time.Time{}, false, lt, func(ctx context.Context) (<-chan error, <-chan struct{}) {
			done, finished := startBackgroundDriverChild(ctx, t, "clean", "")
			finishedCh = finished
			return done, finished
		})
		if err != nil {
			t.Fatalf("clean driver through the owning composition = %v, want nil", err)
		}
		select {
		case <-finishedCh:
		default:
			t.Fatalf("owning composition returned before the driver was reaped")
		}
	})

	t.Run("an admitted driver that ignores SIGTERM is joined", func(t *testing.T) {
		ready := filepath.Join(t.TempDir(), "ready")
		var finishedCh <-chan struct{}
		deadline := time.Now().Add(lt.Join + lt.Cleanup + 200*time.Millisecond)
		err := runOwnedBackgroundDriver(t, deadline, true, lt, func(ctx context.Context) (<-chan error, <-chan struct{}) {
			done, finished := startBackgroundDriverChild(ctx, t, "ignore-term", ready)
			finishedCh = finished
			waitBackgroundDriverReady(t, ready)
			return done, finished
		})
		if err == nil {
			t.Fatalf("a driver that never exits must make the owning composition return an error")
		}
		select {
		case <-finishedCh:
		default:
			t.Fatalf("owning composition returned before joining the cancelled driver")
		}
	})
}
