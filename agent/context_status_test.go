package agent

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// assertContextStatus checks one fixture-authored expected state plus the
// invariants every sample must satisfy. The expected metrics are authored in
// the test, not derived by calling the estimator.
func assertContextStatus(t *testing.T, s *Session, wantUsed, wantWindow int, wantAssumed bool) {
	t.Helper()
	metrics, pressure := s.ContextStatus()
	if metrics.Used != wantUsed || metrics.Window != wantWindow || metrics.Assumed != wantAssumed {
		t.Fatalf("ContextStatus metrics = %+v pressure %v, want Used %d Window %d Assumed %v",
			metrics, pressure, wantUsed, wantWindow, wantAssumed)
	}
	checkContextStatusInvariants(t, metrics, pressure)
	// Preservation check only: each wrapper takes a fresh sample of the
	// unchanged session state, so it must report the same metrics and pressure
	// the combined call just returned.
	if got := s.ContextMetrics(); got != metrics {
		t.Fatalf("ContextMetrics = %+v, want %+v", got, metrics)
	}
	if got := s.ContextPressure(); got != pressure {
		t.Fatalf("ContextPressure = %v, want %v", got, pressure)
	}
}

// TestContextStatusParityAcrossMutations drives the production append, fold,
// measurement and profile-switch mutators and asserts the very next sample
// reflects each one. The expected Used/Window/Assumed values are computed by
// hand from the fixture's 1024-char messages (tokens = chars/4), except the
// window, which is the fixture profile's own known window.
func TestContextStatusParityAcrossMutations(t *testing.T) {
	t.Parallel()
	s := newSession(t)
	text := contextStatusText()
	userTurn := schema.NewTurn(schema.TurnUserInput, llm.User(text))
	assistantTurn := schema.NewTurn(schema.TurnAssistant, llm.Assistant(text))
	for i := range 4 {
		if i%2 == 0 {
			s.recordTurn(userTurn, userTurn)
		} else {
			s.recordTurn(assistantTurn, assistantTurn)
		}
	}
	wantWindow := s.profile.ContextWindowSize()

	// initial: four 1024-char turns -> 4096/4 = 1024 tokens.
	assertContextStatus(t, s, 1024, wantWindow, false)

	// append one 1024-char turn: 5120/4 = 1280 tokens.
	s.recordTurn(userTurn, userTurn)
	assertContextStatus(t, s, 1280, wantWindow, false)

	// fold to one 1024-char summary: 1024/4 = 256 tokens.
	folded := []schema.Turn{schema.NewTurn(schema.TurnSummary, llm.Assistant(text))}
	s.mu.Lock()
	published, ok := s.publishFoldedHistory(len(s.history), s.historyRevision, folded)
	s.mu.Unlock()
	if !ok || len(published) != 1 {
		t.Fatalf("publishFoldedHistory published %d turns, ok=%v; want 1, true", len(published), ok)
	}
	assertContextStatus(t, s, 256, wantWindow, false)

	// A recorded measurement for the current length replaces the char/4
	// estimate.
	if accepted := s.contextMgr.RecordInputTokensFor(s.profile, 300, 1); !accepted {
		t.Fatal("RecordInputTokensFor refused the fixture's own profile")
	}
	assertContextStatus(t, s, 300, wantWindow, false)

	// An unknown-window profile is assumed at 128000 tokens and clears the
	// measurement, so the single summary turn is estimated at 256.
	s.contextMgr.SetProfile(&provider.Profile{})
	assertContextStatus(t, s, 256, 128000, true)

	// nil context manager reports zero metrics and pressure.
	savedMgr := s.contextMgr
	s.contextMgr = nil
	assertContextStatus(t, s, 0, 0, false)
	s.contextMgr = savedMgr

	// A manager with no profile has no window to report.
	s.contextMgr.SetProfile(nil)
	assertContextStatus(t, s, 0, 0, false)
}

// TestContextStatusAllocationBudget pins the fixture at 10,000 turns with no
// recorded measurement. The budget is the component count of one sample: one
// history-slice copy and one []llm.Message estimate. This test must stay
// serial (no t.Parallel): AllocsPerRun reads the process-wide allocator
// counters, so a concurrent test in this package would perturb the count.
func TestContextStatusAllocationBudget(t *testing.T) {
	const turns = 10_000
	s, _ := newContextStatusLargeFixture(turns)
	if len(s.history) != turns {
		t.Fatalf("fixture history = %d, want %d", len(s.history), turns)
	}
	if got := s.contextMgr.LastInputTokens(); got != 0 {
		t.Fatalf("fixture LastInputTokens = %d, want 0 (no recorded measurement)", got)
	}
	allocs := testing.AllocsPerRun(20, func() { _, _ = s.ContextStatus() })
	t.Logf("ContextStatus at %d turns allocates %v allocations per sample", turns, allocs)
	if allocs > 2 {
		t.Fatalf("ContextStatus allocates %v, budget 2", allocs)
	}
}

// TestContextStatusAllocationCeiling10000Turns is the byte guard the allocation
// count cannot provide: two allocations can also be two history copies and no
// estimate. Fixture construction, warmup, runtime.GC and the read of "before"
// are outside the measured window, and the iteration count is fixed. This test
// must stay serial (no t.Parallel) because runtime.MemStats describes the
// whole process.
func TestContextStatusAllocationCeiling10000Turns(t *testing.T) {
	const turns = 10_000
	s, profile := newContextStatusLargeFixture(turns)
	if got := s.contextMgr.LastInputTokens(); got != 0 {
		t.Fatalf("fixture LastInputTokens = %d, want 0 (no recorded measurement)", got)
	}

	_, _ = s.ContextStatus() // warm the call once
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	const iters = 20
	var metrics ContextMetrics
	var pressure float64
	for range iters {
		metrics, pressure = s.ContextStatus()
	}
	runtime.ReadMemStats(&after)
	bytesPerOp := float64(after.TotalAlloc-before.TotalAlloc) / float64(iters)
	t.Logf("ContextStatus at %d turns allocates %.0f bytes per sample", turns, bytesPerOp)
	if bytesPerOp > 10*1024*1024 {
		t.Fatalf("ContextStatus allocates %.0f bytes per sample, ceiling %d", bytesPerOp, 10*1024*1024)
	}

	// Fixture-authored output for the same sample.
	const wantUsed = turns * contextStatusMessageChars / 4
	wantWindow := profile.ContextWindowSize()
	if metrics.Used != wantUsed {
		t.Fatalf("Used = %d, want %d", metrics.Used, wantUsed)
	}
	if metrics.Window != wantWindow || metrics.Assumed {
		t.Fatalf("Window/Assumed = %d/%v, want %d/false", metrics.Window, metrics.Assumed, wantWindow)
	}
	checkContextStatusInvariants(t, metrics, pressure)
}

// checkContextStatusInvariants applies the identities every sample must hold,
// independent of which mutation has landed.
func checkContextStatusInvariants(t *testing.T, metrics ContextMetrics, pressure float64) {
	t.Helper()
	wantRemaining := max(metrics.Window-metrics.Used, 0)
	if metrics.Remaining != wantRemaining {
		t.Errorf("Remaining = %d, want %d for %+v", metrics.Remaining, wantRemaining, metrics)
	}
	wantPressure := 0.0
	if metrics.Window > 0 {
		wantPressure = float64(metrics.Used) / float64(metrics.Window)
	}
	if pressure != wantPressure {
		t.Errorf("pressure = %v, want %v for %+v", pressure, wantPressure, metrics)
	}
}

// TestContextStatusConcurrentMutations runs the production append and fold
// writers alongside a measurement writer and a reader. Each result is one
// shared history snapshot plus one profile/measurement snapshot; it is not a
// globally atomic session instant, and this test asserts invariants, not that
// a particular interleaving happened.
//
// The fold writer holds attentionMu and s.mu across its snapshot and publish,
// so its revision check always succeeds at the moment it publishes. That
// exercises replacement under concurrent readers, not the production fold's
// snapshot/fold-unlocked/reacquire conflict path; producer parity is not
// claimed for it.
func TestContextStatusConcurrentMutations(t *testing.T) {
	t.Parallel()
	s := newSession(t)
	wantWindow := s.profile.ContextWindowSize()
	text := contextStatusText()
	turn := schema.NewTurn(schema.TurnUserInput, llm.User(text))

	const iterations = 100
	var wg sync.WaitGroup
	var refused atomic.Int64

	wg.Add(4)

	// writer 1: the production append path takes attentionMu then s.mu.
	go func() {
		defer wg.Done()
		for range iterations {
			s.recordTurn(turn, turn)
		}
	}()

	// writer 2: replacement under concurrent readers; both locks held across
	// the snapshot and publish so the revision check succeeds.
	go func() {
		defer wg.Done()
		for range iterations {
			folded := []schema.Turn{schema.NewTurn(schema.TurnSummary, llm.Assistant(text))}
			s.attentionMu.Lock()
			s.mu.Lock()
			snapLen := len(s.history)
			snapRev := s.historyRevision
			_, ok := s.publishFoldedHistory(snapLen, snapRev, folded)
			s.mu.Unlock()
			s.attentionMu.Unlock()
			if !ok {
				t.Errorf("publishFoldedHistory refused a snapshot taken under s.mu")
			}
		}
	}()

	// writer 3: record a measurement at a history length read under s.mu.
	// Every call must be accepted; no SetProfile runs during this test.
	go func() {
		defer wg.Done()
		for range iterations {
			s.mu.Lock()
			historyLen := len(s.history)
			s.mu.Unlock()
			if accepted := s.contextMgr.RecordInputTokensFor(s.profile, 1000, historyLen); !accepted {
				refused.Add(1)
			}
		}
	}()

	// reader: every sample must hold the ratio and remaining identities and
	// report the fixture's only known window state.
	go func() {
		defer wg.Done()
		for range iterations {
			metrics, pressure := s.ContextStatus()
			if metrics.Window != wantWindow || metrics.Assumed {
				t.Errorf("Window/Assumed = %d/%v, want %d/false", metrics.Window, metrics.Assumed, wantWindow)
			}
			checkContextStatusInvariants(t, metrics, pressure)
		}
	}()

	wg.Wait()
	if n := refused.Load(); n != 0 {
		t.Fatalf("RecordInputTokensFor refused %d of %d calls", n, iterations)
	}

	// Freshness after the writers quiesce. This measurement is fixture-authored
	// by length, not estimator-derived.
	s.mu.Lock()
	finalLen := len(s.history)
	s.mu.Unlock()
	if accepted := s.contextMgr.RecordInputTokensFor(s.profile, 300, finalLen); !accepted {
		t.Fatal("RecordInputTokensFor refused the fixture's own profile after the writers stopped")
	}
	metrics, pressure := s.ContextStatus()
	if metrics.Used != 300 {
		t.Fatalf("Used after final measurement = %d, want 300", metrics.Used)
	}
	if metrics.Window != wantWindow || metrics.Assumed {
		t.Fatalf("Window/Assumed after final measurement = %d/%v, want %d/false", metrics.Window, metrics.Assumed, wantWindow)
	}
	checkContextStatusInvariants(t, metrics, pressure)
}
