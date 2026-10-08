package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

type manualClaimResult struct {
	claim    *RetirementClaim
	snapshot RetirementSnapshot
	err      error
}

// fakeClockRetirementRoot is a session attached to a retirement controller
// that runs on a fake clock.
func fakeClockRetirementRoot(t *testing.T) (*Session, *RetirementController, *agenttest.FakeClock) {
	t.Helper()
	root := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir()}))
	clk := agenttest.NewFakeClockAt(time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC))
	c, err := NewRetirementController(0, clk)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	return root, c, clk
}

// blockedNamerSession is fakeClockRetirementRoot with the session's
// initial-prompt namer blocked in its provider call until release is called.
func blockedNamerSession(t *testing.T) (*Session, *RetirementController, *agenttest.FakeClock, func()) {
	t.Helper()
	root, c, clk := fakeClockRetirementRoot(t)
	entered, unblock := make(chan struct{}), make(chan struct{})
	enter := sync.OnceFunc(func() { close(entered) })
	release := sync.OnceFunc(func() { close(unblock) })
	t.Cleanup(func() {
		release()
		root.sendersWG.Wait()
	})
	namer := llm.NewClient()
	namer.Register(&agenttest.ScriptedAdapter{Provider: root.currentProfile().CheapProvider(), Responder: func(llm.Request) llm.Response {
		enter()
		<-unblock
		return llm.Response{Message: llm.Assistant(`{"name":"Named Session"}`)}
	}})
	updateSessionTestConfig(root, func(cfg *testConfig) { cfg.namerClient = namer })
	root.launchInitialPromptNamer(context.Background(), "the opening prompt")
	<-entered
	return root, c, clk, release
}

// awaitManualClaimWaiting returns once the retire in done is waiting on the
// namer, its timer parked on clk, and fails if it answered without waiting.
func awaitManualClaimWaiting(t *testing.T, clk *agenttest.FakeClock, done <-chan manualClaimResult) {
	t.Helper()
	// TRIPWIRE: the retire parks its timer at once; 30s only fires on a hang.
	waitForCondition(t, 30*time.Second, "the manual retire to wait on the namer", func() bool {
		select {
		case got := <-done:
			t.Fatalf("manual retire answered without waiting for the namer: claim %v, %+v, %v", got.claim, got.snapshot, got.err)
		default:
		}
		return clk.BlockedCount() == 1
	})
}

// assertRefusedAtOnce fails unless the retire in done is refused without
// waiting; during names the work held when it was asked.
func assertRefusedAtOnce(t *testing.T, done <-chan manualClaimResult, during string) {
	t.Helper()
	select {
	case got := <-done:
		if got.err != nil || got.claim != nil {
			t.Fatalf("manual retire during %s = claim %v, %v; want a refusal", during, got.claim, got.err)
		}
	// TRIPWIRE: a refusal returns at once; 30s only fires if the retire
	// waited on the fake clock for the namer.
	case <-time.After(30 * time.Second):
		t.Fatalf("manual retire waited for the namer during %s", during)
	}
}

func startManualClaim(c *RetirementController) <-chan manualClaimResult {
	done := make(chan manualClaimResult, 1)
	go func() {
		claim, snapshot, err := c.TryManualClaim(context.Background())
		done <- manualClaimResult{claim, snapshot, err}
	}()
	return done
}

// A user's retire that arrives while the session namer is still naming the
// session waits for it to settle and then retires, rather than being refused
// over decoration (#3921).
func TestManualRetireWaitsForTheSessionNamer(t *testing.T) {
	t.Parallel()
	_, c, clk, release := blockedNamerSession(t)
	done := startManualClaim(c)
	awaitManualClaimWaiting(t, clk, done)
	release()
	got := <-done
	if got.err != nil || got.claim == nil {
		t.Fatalf("manual retire after the namer settled = %+v, %v; want a claim", got.snapshot, got.err)
	}
	if err := c.Abort(got.claim, ""); err != nil {
		t.Fatal(err)
	}
}

// The wait is bounded by the namer's own timeout: a namer still running then
// refuses the retire as before.
func TestManualRetireStopsWaitingForTheNamerAtItsTimeout(t *testing.T) {
	t.Parallel()
	_, c, clk, _ := blockedNamerSession(t)
	done := startManualClaim(c)
	awaitManualClaimWaiting(t, clk, done)
	clk.Advance(sessionNameTimeout)
	got := <-done
	if got.err != nil || got.claim != nil {
		t.Fatalf("manual retire past the namer's timeout = claim %v, %v; want a refusal", got.claim, got.err)
	}
}

// Only the namer is waited for: other autonomous work, such as a goal's
// lease, refuses a manual retire at once, as before.
func TestManualRetireDoesNotWaitForOtherAutonomousWork(t *testing.T) {
	t.Parallel()
	root, c, _ := fakeClockRetirementRoot(t)
	release, err := root.beginRetirementMutation("autonomous") // as SetGoal holds it
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	assertRefusedAtOnce(t, startManualClaim(c), "a goal's lease")
}

// A namer runs inside the turn that launched it, and goal turns can run
// beside one: when anything but a namer holds the process, the retire is
// refused at once rather than after waiting out the namer for a refusal.
func TestManualRetireDoesNotWaitForTheNamerBesideOtherWork(t *testing.T) {
	t.Parallel()
	root, c, _, _ := blockedNamerSession(t)
	release, err := root.beginRetirementMutation("turn")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	assertRefusedAtOnce(t, startManualClaim(c), "a turn beside the namer")
}

// Other work that starts while the retire waits on the namer ends the wait:
// the retire is refused then, not after the namer settles.
func TestManualRetireStopsWaitingWhenOtherWorkStarts(t *testing.T) {
	t.Parallel()
	root, c, clk, _ := blockedNamerSession(t)
	done := startManualClaim(c)
	awaitManualClaimWaiting(t, clk, done)
	release, err := root.beginRetirementMutation("turn")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	assertRefusedAtOnce(t, done, "a turn begun while it waited")
}

// A retire whose caller gave up while it waited on the namer answers with
// the context's error and never goes on to claim.
func TestManualRetireCancelledWhileWaitingDoesNotClaim(t *testing.T) {
	t.Parallel()
	_, c, clk, _ := blockedNamerSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan manualClaimResult, 1)
	go func() {
		claim, snapshot, err := c.TryManualClaim(ctx)
		done <- manualClaimResult{claim, snapshot, err}
	}()
	awaitManualClaimWaiting(t, clk, done)
	cancel()
	got := <-done
	if got.claim != nil {
		_ = c.Abort(got.claim, "")
	}
	if got.claim != nil || !errors.Is(got.err, context.Canceled) {
		t.Fatalf("cancelled manual retire = claim %v, %v; want no claim and context.Canceled", got.claim, got.err)
	}
}
