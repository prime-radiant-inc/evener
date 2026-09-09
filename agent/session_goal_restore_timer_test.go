package agent

import (
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// restoreGoalTestSession rebuilds a session from meta on a FakeClock with the
// minimal deterministic restore config (mirrors restoreIntegritySession).
func restoreGoalTestSession(t *testing.T, clk *agenttest.FakeClock, meta schema.SessionMeta) *Session {
	t.Helper()
	stateDir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := RestoreSessionFromMetaWithConfig(
		c,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		meta,
		RestoreSessionConfig{StateDir: stateDir, clock: clk, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	return sess
}

// TestGoalRestore_WaitingRearmsFourWayTimer pins restart-during-wait (spec
// §§2, 7): a restored waiting goal re-arms one coalesced sclock timer per
// the four-way min — never 8 independent timers, never deadline- or
// cap-blind. Here the goal deadline (4h) precedes the wait deadline (24h
// cap), so the timer must fire at the goal deadline, not the wait's.
func TestGoalRestore_WaitingRearmsFourWayTimer(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	now := clk.Now()

	waitDeadline := now.Add(23 * time.Hour)
	meta := schema.SessionMeta{
		ID:        "restore-wait-timer",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    (SessionConfig{}).toSnapshot(),
		Goal: &schema.GoalSnapshot{
			Objective: "wait out the restart",
			Status:    string(goal.StatusWaiting),
			CreatedAt: now,
			UpdatedAt: now,
			Waits: []schema.GoalWaitSnapshot{{
				WaitID:         "wait_1",
				Kind:           string(goal.WaitUntilTime),
				Label:          "long-timer",
				Deadline:       waitDeadline,
				RegisteredAt:   now,
				IdempotencyKey: "k",
			}},
			Budgets: &schema.GoalBudgetsSnapshot{
				MaxContinuations:    goal.DefaultMaxContinuations,
				Deadline:            now.Add(goal.DefaultGoalDeadline),
				MaxParkedTotalNanos: int64(goal.DefaultMaxParkedTotal),
			},
		},
	}
	sess := restoreGoalTestSession(t, clk, meta)
	defer sess.Close()

	full, ok := sess.getOrCreateGoalStore().GoalSnapshot()
	if !ok || full.Status != goal.StatusWaiting || len(full.Waits) != 1 {
		t.Fatalf("restored goal = %+v ok=%v, want waiting with 1 wait", full, ok)
	}
	// The four-way min: min(23h wait, 4h goal deadline, 24h parked-total
	// crossing, 60s poll due) = 60s poll due. The poll leg is the nearest
	// obligation: the timer must be armed and must fire at now+60s.
	sess.mu.Lock()
	armed := sess.goalWaitTimer != nil
	sess.mu.Unlock()
	if !armed {
		t.Fatal("restored waiting goal must re-arm the coalesced timer (four-way min)")
	}
	fire, ok := goalWaitNextFire(full, now)
	if !ok {
		t.Fatal("goalWaitNextFire must arm on a restored waiting goal")
	}
	if want := now.Add(goal.GoalWaitPollInterval); !fire.Equal(want) {
		t.Fatalf("four-way fire = %v, want the poll-due leg %v (min of 23h/4h/24h/60s)", fire, want)
	}
}

// TestGoalRestore_FourWayMinPicksGoalDeadline pins the deadline leg: with no
// poll pressure in range (poll leg removed by construction here — the wait
// deadline falls before the next poll due), the goal deadline wins over a
// later wait deadline.
func TestGoalRestore_FourWayMinPicksGoalDeadline(t *testing.T) {
	t.Parallel()
	now := time.Unix(2_000_000_000, 0).UTC()
	full := goal.GoalSnapshot{
		Status: goal.StatusWaiting,
		Waits: []goal.Wait{{
			Lease: goal.Lease{WaitID: "wait_1", Deadline: now.Add(30 * time.Second)},
		}},
		Budgets: goal.Budgets{
			MaxContinuations: goal.DefaultMaxContinuations,
			Deadline:         now.Add(10 * time.Second),
			MaxParkedTotal:   goal.DefaultMaxParkedTotal,
		},
	}
	// min(30s wait, 10s goal deadline, 24h parked crossing, 60s poll) = 10s.
	fire, ok := goalWaitNextFire(full, now)
	if !ok {
		t.Fatal("four-way min must arm")
	}
	if want := now.Add(10 * time.Second); !fire.Equal(want) {
		t.Fatalf("four-way fire = %v, want goal-deadline leg %v", fire, want)
	}
}

// TestGoalRestore_FourWayMinPicksParkedCrossing pins the parked-total leg: a
// tightened maxParkedTotal whose projected crossing precedes every deadline
// arms the timer to the crossing.
func TestGoalRestore_FourWayMinPicksParkedCrossing(t *testing.T) {
	t.Parallel()
	now := time.Unix(2_000_000_000, 0).UTC()
	full := goal.GoalSnapshot{
		Status: goal.StatusWaiting,
		Waits: []goal.Wait{{
			Lease: goal.Lease{WaitID: "wait_1", Deadline: now.Add(time.Hour)},
		}},
		Budgets: goal.Budgets{
			MaxContinuations: goal.DefaultMaxContinuations,
			Deadline:         now.Add(4 * time.Hour),
			ParkedTotal:      50 * time.Minute,
			MaxParkedTotal:   time.Hour,
		},
	}
	// min(1h wait, 4h deadline, 10m parked crossing, 60s poll) = 60s poll.
	fire, ok := goalWaitNextFire(full, now)
	if !ok {
		t.Fatal("four-way min must arm")
	}
	if want := now.Add(goal.GoalWaitPollInterval); !fire.Equal(want) {
		t.Fatalf("four-way fire = %v, want poll-due leg %v", fire, want)
	}
	// Tighten further: a 30s parked remainder beats the 60s poll.
	full.Budgets.ParkedTotal = 59*time.Minute + 30*time.Second
	fire, ok = goalWaitNextFire(full, now)
	if !ok {
		t.Fatal("four-way min must arm")
	}
	if want := now.Add(30 * time.Second); !fire.Equal(want) {
		t.Fatalf("four-way fire = %v, want parked-crossing leg %v", fire, want)
	}
}

// TestGoalRestore_PendingWakeCrashRecoveryRedrives pins pendingWake crash
// recovery (spec §7): persist with a consumed-but-undelivered wake, restore,
// and the gate must re-drive the wake turn — never lose it, never duplicate
// the terminal report.
func TestGoalRestore_PendingWakeCrashRecoveryRedrives(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	src := newWaitGateSession(t, clk)
	defer src.Close()
	wireKickAndNotify(src)

	now := clk.Now()
	store := src.getOrCreateGoalStore()
	store.Set("crash mid-wake", now)
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Target: "crash-timer", Timeout: time.Minute, Label: "crash-timer"}, now)
	if !ok {
		t.Fatal("precondition: registration should succeed")
	}
	clk.Advance(2 * time.Minute)
	if _, ok := store.ClaimFire(w.Lease.WaitID, "wait expired: crash-timer", clk.Now()); !ok {
		t.Fatal("precondition: claim should consume the expired lease")
	}
	meta := src.Meta()
	if meta.Goal == nil || len(meta.Goal.PendingWake) != 1 {
		t.Fatalf("persisted backlog = %+v, want the 1 claimed fire (crash between consume and kick)", meta.Goal)
	}
	// Crash: restore the persisted meta on a fresh clock at the same instant.
	clk2 := agenttest.NewFakeClockAt(clk.Now())
	meta.ID = "restore-crash-wake"
	restored := restoreGoalTestSession(t, clk2, meta)
	defer restored.Close()

	full, ok := restored.getOrCreateGoalStore().GoalSnapshot()
	if !ok || len(full.PendingWake) != 1 {
		t.Fatalf("restored backlog = %+v ok=%v, want the crash-surviving wake", full, ok)
	}
	// The gate re-drives the wake turn carrying the trigger (rule 1).
	prompt, cont := restored.armGoalContinuation(false, true)
	if !cont || !strings.Contains(prompt, w.Lease.WaitID) {
		t.Fatalf("restored gate = (%q, %v), want the crash-recovered wake drive carrying %q", prompt, cont, w.Lease.WaitID)
	}
	if !strings.Contains(prompt, goalWaitWakeTrailerPrefix) {
		t.Fatalf("recovered prompt must carry the wake trailer:\n%s", prompt)
	}
}

// TestGoalRestore_ExpiredWaitAttachScanClaimsAtRestore pins attach-scan at
// restore (spec §7): a wait whose deadline passed while down claims into
// pendingWake during restore itself, so the first gate already drives.
func TestGoalRestore_ExpiredWaitAttachScanClaimsAtRestore(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	now := clk.Now()
	meta := schema.SessionMeta{
		ID:        "restore-expired-scan",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    (SessionConfig{}).toSnapshot(),
		Goal: &schema.GoalSnapshot{
			Objective: "scan me",
			Status:    string(goal.StatusWaiting),
			CreatedAt: now.Add(-2 * time.Hour),
			UpdatedAt: now.Add(-2 * time.Hour),
			Waits: []schema.GoalWaitSnapshot{{
				WaitID:         "wait_1",
				Kind:           string(goal.WaitUntilTime),
				Label:          "short-timer",
				Deadline:       now.Add(-time.Hour),
				RegisteredAt:   now.Add(-2 * time.Hour),
				IdempotencyKey: "k",
			}},
			Budgets: &schema.GoalBudgetsSnapshot{
				MaxContinuations:    goal.DefaultMaxContinuations,
				Deadline:            now.Add(2 * time.Hour),
				MaxParkedTotalNanos: int64(goal.DefaultMaxParkedTotal),
			},
		},
	}
	sess := restoreGoalTestSession(t, clk, meta)
	defer sess.Close()

	full, ok := sess.getOrCreateGoalStore().GoalSnapshot()
	if !ok {
		t.Fatal("restored goal must load")
	}
	if len(full.PendingWake) != 1 || full.PendingWake[0].WaitID != "wait_1" {
		t.Fatalf("attach-scan must claim the already-expired lease at restore, got %+v", full.PendingWake)
	}
}

// TestGoalRestore_SettleKicksRestoredBacklog pins the restore kick-immediately
// path (spec §7): a restored pendingWake backlog with no kick wired at
// restore has no scheduled wake turn yet — the first settle after wiring
// kicks it exactly once (the delivered set distinguishes it from an
// already-kicked backlog, which suppresses).
func TestGoalRestore_SettleKicksRestoredBacklog(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	src := newWaitGateSession(t, clk)
	defer src.Close()
	wireKickAndNotify(src)

	store := src.getOrCreateGoalStore()
	store.Set("crash mid-wake", clk.Now())
	w, ok := store.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Target: "crash-timer", Timeout: time.Minute, Label: "crash-timer"}, clk.Now())
	if !ok {
		t.Fatal("precondition: registration should succeed")
	}
	clk.Advance(2 * time.Minute)
	if _, ok := store.ClaimFire(w.Lease.WaitID, "wait expired: crash-timer", clk.Now()); !ok {
		t.Fatal("precondition: claim should consume the expired lease")
	}
	meta := src.Meta()
	clk2 := agenttest.NewFakeClockAt(clk.Now())
	meta.ID = "restore-settle-kick"
	restored := restoreGoalTestSession(t, clk2, meta)
	defer restored.Close()
	kicks := wireKickAndNotify(restored)

	if !restored.settleGoalOnIdle() {
		t.Fatal("first settle over a restored-but-never-kicked backlog must kick")
	}
	if *kicks != 1 {
		t.Fatalf("kicks = %d, want 1", *kicks)
	}
	if restored.settleGoalOnIdle() {
		t.Fatal("second settle must not re-kick the same restored fire (exactly-once)")
	}
	if *kicks != 1 {
		t.Fatalf("kicks = %d, want still 1 after the second settle", *kicks)
	}
}
