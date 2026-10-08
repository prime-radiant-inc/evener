package agent

import (
	"context"
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

// blockedNamerSession is a session whose retirement controller runs on a fake
// clock and whose initial-prompt namer is blocked in its provider call until
// release is closed.
func blockedNamerSession(t *testing.T) (*Session, *RetirementController, *agenttest.FakeClock, chan struct{}) {
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
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		root.sendersWG.Wait()
	})
	namer := llm.NewClient()
	namer.Register(&agenttest.ScriptedAdapter{Provider: root.currentProfile().CheapProvider(), Responder: func(llm.Request) llm.Response {
		close(entered)
		<-release
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
	close(release)
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
	root := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir()}))
	clk := agenttest.NewFakeClockAt(time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC))
	c, err := NewRetirementController(0, clk)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	release, err := root.beginRetirementMutation("autonomous") // as SetGoal holds it
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	done := startManualClaim(c)
	select {
	case got := <-done:
		if got.err != nil || got.claim != nil {
			t.Fatalf("manual retire during a goal's lease = claim %v, %v; want a refusal", got.claim, got.err)
		}
	// TRIPWIRE: a refusal returns at once; 30s only fires if the retire
	// waited on the fake clock for work it must not wait for.
	case <-time.After(30 * time.Second):
		t.Fatal("manual retire waited for a goal's lease")
	}
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
	done := startManualClaim(c)
	select {
	case got := <-done:
		if got.err != nil || got.claim != nil {
			t.Fatalf("manual retire during a turn beside the namer = claim %v, %v; want a refusal", got.claim, got.err)
		}
	// TRIPWIRE: a refusal returns at once; 30s only fires if the retire
	// waited out the namer on the fake clock for a refusal.
	case <-time.After(30 * time.Second):
		t.Fatal("manual retire waited for the namer while a turn held the process")
	}
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
	select {
	case got := <-done:
		if got.err != nil || got.claim != nil {
			t.Fatalf("manual retire after a turn began = claim %v, %v; want a refusal", got.claim, got.err)
		}
	// TRIPWIRE: the new lease wakes the wait at once; 30s only fires if the
	// retire kept waiting on the fake clock for the namer.
	case <-time.After(30 * time.Second):
		t.Fatal("manual retire kept waiting for the namer after a turn began")
	}
}
