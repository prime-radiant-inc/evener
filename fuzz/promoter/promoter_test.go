package promoter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// replayFn decides, per replay call index, whether the failure reproduced and
// whether it reproduced as the same bug.
type replayFn func(call int) (failed bool, same bool)

// testAdapter is a configurable Adapter wired to the real bucket store and Go
// emitter, so the promote/dedup paths are exercised end-to-end. Its Signature
// folds Detail into the key for non-panic oracles (and stack frames for panics),
// which is what lets distinct semantic failures dedup independently.
type testAdapter struct {
	pkg    string
	dir    string
	replay replayFn

	emits int
	calls int
}

func (a *testAdapter) Minimize(f Failure) Failure { return f }

func (a *testAdapter) Signature(f Failure) Signature {
	key := f.Detail
	if f.Oracle == Panic && len(f.Stack) > 0 {
		key = strings.Join(topN(f.Stack, 4), "|")
	}
	if key == "" {
		key = ShortHash(f)
	}
	return Signature{Oracle: f.Oracle, Key: key}
}

func (a *testAdapter) Replay(_ context.Context, _ Failure) (bool, bool) {
	c := a.calls
	a.calls++
	return a.replay(c)
}

func (a *testAdapter) Emit(f Failure) (string, error) {
	a.emits++
	return WriteGoTest(a.dir, GoTest{
		Package:    a.pkg,
		Surface:    f.Surface,
		Oracle:     f.Oracle,
		Signature:  a.Signature(f).String(),
		Hash:       ShortHash(f),
		ReplayBody: "\t_ = t // replay of artifact: " + string(f.Artifact),
	})
}

func topN(frames []string, n int) []string {
	if len(frames) < n {
		return frames
	}
	return frames[:n]
}

type fakeQuarantiner struct {
	count        int
	survivedRuns int
	last         Failure
}

func (q *fakeQuarantiner) Quarantine(f Failure, survivedRuns int) error {
	q.count++
	q.survivedRuns = survivedRuns
	q.last = f
	return nil
}

func newHarness(t *testing.T, replay replayFn, k int) (*Promoter, *testAdapter, *fakeQuarantiner, *BucketStore) {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenBucketStore(filepath.Join(dir, "buckets.json"))
	if err != nil {
		t.Fatalf("OpenBucketStore: %v", err)
	}
	adapter := &testAdapter{pkg: "regressions", dir: filepath.Join(dir, "tests"), replay: replay}
	q := &fakeQuarantiner{}
	return New(adapter, store, q, k), adapter, q, store
}

func deterministicFailure() Failure {
	return Failure{
		Surface:  "appwire-seq",
		Oracle:   Invariant,
		Detail:   "status-monotonicity",
		Artifact: json.RawMessage(`{"ops":["init","turn/start"],"seed":42}`),
	}
}

// TestPromote_DeterministicFailure_PromotesOnce is the core acceptance: a
// failure that reproduces all K times with the same signature is promoted, the
// regression test is written, and the bucket is recorded (and survives reopen).
func TestPromote_DeterministicFailure_PromotesOnce(t *testing.T) {
	p, adapter, q, store := newHarness(t, func(int) (bool, bool) { return true, true }, 5)
	f := deterministicFailure()

	out, err := p.Promote(context.Background(), f)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if out != Promoted {
		t.Fatalf("outcome = %v, want Promoted", out)
	}
	if adapter.emits != 1 {
		t.Fatalf("emits = %d, want 1", adapter.emits)
	}
	if adapter.calls != 5 {
		t.Fatalf("replay calls = %d, want 5 (K)", adapter.calls)
	}
	if q.count != 0 {
		t.Fatalf("quarantine count = %d, want 0", q.count)
	}

	sig := adapter.Signature(f)
	path, ok := store.Get(sig)
	if !ok {
		t.Fatalf("bucket for %s not recorded", sig)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("regression test not written: %v", err)
	}

	// The bucket must survive a reopen (atomic persistence).
	reopened, err := OpenBucketStore(filepath.Join(filepath.Dir(filepath.Dir(path)), "buckets.json"))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if !reopened.Has(sig) {
		t.Fatalf("bucket not persisted across reopen")
	}
}

// TestPromote_SameFailureAgain_Dedups proves the second sighting of an
// already-promoted failure emits NO second test.
func TestPromote_SameFailureAgain_Dedups(t *testing.T) {
	p, adapter, _, _ := newHarness(t, func(int) (bool, bool) { return true, true }, 5)
	f := deterministicFailure()

	if out, err := p.Promote(context.Background(), f); err != nil || out != Promoted {
		t.Fatalf("first Promote = %v, %v; want Promoted, nil", out, err)
	}
	callsAfterFirst := adapter.calls

	out, err := p.Promote(context.Background(), f)
	if err != nil {
		t.Fatalf("second Promote: %v", err)
	}
	if out != AlreadyKnown {
		t.Fatalf("second outcome = %v, want AlreadyKnown", out)
	}
	if adapter.emits != 1 {
		t.Fatalf("emits = %d after dedup, want 1 (no second test)", adapter.emits)
	}
	if adapter.calls != callsAfterFirst {
		t.Fatalf("dedup must short-circuit before replay: calls went %d -> %d", callsAfterFirst, adapter.calls)
	}
}

// TestPromote_FlakyFailure_Quarantined proves a failure that does NOT reproduce
// deterministically is quarantined and never emitted — the rule that keeps the
// gate trustworthy.
func TestPromote_FlakyFailure_Quarantined(t *testing.T) {
	// Reproduces on the first replay, then stops: a classic flake.
	flaky := func(call int) (bool, bool) { return call == 0, true }
	p, adapter, q, store := newHarness(t, flaky, 5)

	out, err := p.Promote(context.Background(), deterministicFailure())
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if out != Quarantined {
		t.Fatalf("outcome = %v, want Quarantined", out)
	}
	if adapter.emits != 0 {
		t.Fatalf("emits = %d, want 0 (flake must never be emitted)", adapter.emits)
	}
	if q.count != 1 {
		t.Fatalf("quarantine count = %d, want 1", q.count)
	}
	if q.survivedRuns != 1 {
		t.Fatalf("survivedRuns = %d, want 1 (failed on the 2nd replay)", q.survivedRuns)
	}
	if store.Len() != 0 {
		t.Fatalf("store recorded a flake: len = %d, want 0", store.Len())
	}
}

// TestPromote_SignatureShift_Quarantined proves a failure that reproduces but as
// a DIFFERENT bug each time is also quarantined.
func TestPromote_SignatureShift_Quarantined(t *testing.T) {
	shifting := func(call int) (bool, bool) { return true, call < 2 }
	p, adapter, q, _ := newHarness(t, shifting, 5)

	out, err := p.Promote(context.Background(), deterministicFailure())
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if out != Quarantined {
		t.Fatalf("outcome = %v, want Quarantined", out)
	}
	if adapter.emits != 0 {
		t.Fatalf("emits = %d, want 0", adapter.emits)
	}
	if q.count != 1 {
		t.Fatalf("quarantine count = %d, want 1", q.count)
	}
}

// TestPromote_DistinctSemanticFailures_DedupIndependently guards against the
// dedup over-collapse the design review flagged: two different invariant
// violations (no distinguishing stack) must get different signatures and both
// promote, not be dropped as already-known.
func TestPromote_DistinctSemanticFailures_DedupIndependently(t *testing.T) {
	p, adapter, _, store := newHarness(t, func(int) (bool, bool) { return true, true }, 3)

	f1 := Failure{Surface: "appwire-seq", Oracle: Invariant, Detail: "status-monotonicity",
		Artifact: json.RawMessage(`{"a":1}`)}
	f2 := Failure{Surface: "appwire-seq", Oracle: Invariant, Detail: "no-wedge",
		Artifact: json.RawMessage(`{"b":2}`)}

	if out, _ := p.Promote(context.Background(), f1); out != Promoted {
		t.Fatalf("f1 outcome = %v, want Promoted", out)
	}
	if out, _ := p.Promote(context.Background(), f2); out != Promoted {
		t.Fatalf("f2 outcome = %v, want Promoted (distinct invariant must not dedup against f1)", out)
	}
	if adapter.emits != 2 {
		t.Fatalf("emits = %d, want 2", adapter.emits)
	}
	if store.Len() != 2 {
		t.Fatalf("store len = %d, want 2 distinct buckets", store.Len())
	}
}

// TestRenderGoTest_ProducesCompilableSource confirms the emitter output is valid,
// gofmt-clean Go with the deterministic regression name and provenance trailer.
func TestRenderGoTest_ProducesCompilableSource(t *testing.T) {
	src, err := RenderGoTest(GoTest{
		Package:    "regressions",
		Surface:    "appwire-seq",
		Oracle:     Invariant,
		Signature:  "invariant:status-monotonicity",
		Seam:       "Router.Dispatch",
		Hash:       "abc123def456",
		ReplayBody: "\t_ = t",
	})
	if err != nil {
		t.Fatalf("RenderGoTest: %v", err)
	}
	got := string(src)
	for _, want := range []string{
		"package regressions",
		"func TestRegression_appwire_seq_invariant_abc123def456(t *testing.T)",
		"DO NOT EDIT by hand",
		"Seam: Router.Dispatch",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated source missing %q:\n%s", want, got)
		}
	}
}

// TestRenderGoTest_RejectsMalformedBody ensures a bad ReplayBody fails loudly at
// gofmt time rather than emitting an uncompilable regression test.
func TestRenderGoTest_RejectsMalformedBody(t *testing.T) {
	_, err := RenderGoTest(GoTest{
		Package:    "regressions",
		Surface:    "x",
		Oracle:     Panic,
		Hash:       "deadbeef0000",
		ReplayBody: "this is ( not valid go",
	})
	if err == nil {
		t.Fatal("expected error for malformed replay body, got nil")
	}
}
