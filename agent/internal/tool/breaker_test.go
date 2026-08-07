package tool

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestFailureLedger_IdenticalFailureTwice_StreakTwo(t *testing.T) {
	l := newFailureLedger()
	if failStreak, _ := l.record("write_file", []byte(`{"path":"a"}`), true, "boom"); failStreak != 1 {
		t.Fatalf("first failure: failStreak = %d, want 1", failStreak)
	}
	if failStreak, _ := l.record("write_file", []byte(`{"path":"a"}`), true, "boom"); failStreak != 2 {
		t.Fatalf("second identical failure: failStreak = %d, want 2", failStreak)
	}
	failStreak, _, snippets := l.check("write_file", []byte(`{"path":"a"}`))
	if failStreak != 2 {
		t.Fatalf("check: failStreak = %d, want 2", failStreak)
	}
	if len(snippets) != 2 {
		t.Fatalf("check: snippets = %v, want 2 entries", snippets)
	}
}

func TestFailureLedger_SuccessInBetween_StreakResetsToOne(t *testing.T) {
	l := newFailureLedger()
	l.record("write_file", []byte(`{"path":"a"}`), true, "boom")
	if failStreak, _ := l.record("write_file", []byte(`{"path":"a"}`), false, "ok"); failStreak != 0 {
		t.Fatalf("success: failStreak = %d, want 0", failStreak)
	}
	failStreak, _, snippets := l.check("write_file", []byte(`{"path":"a"}`))
	if failStreak != 0 {
		t.Fatalf("check after success: failStreak = %d, want 0", failStreak)
	}
	if len(snippets) != 0 {
		t.Fatalf("check after success: snippets = %v, want none", snippets)
	}
	if failStreak, _ := l.record("write_file", []byte(`{"path":"a"}`), true, "boom"); failStreak != 1 {
		t.Fatalf("failure after success: failStreak = %d, want 1", failStreak)
	}
}

func TestFailureLedger_DifferentErrorClass_StreakResetsAndSnippetsReplaced(t *testing.T) {
	l := newFailureLedger()
	l.record("write_file", []byte(`{"path":"a"}`), true, "permission denied")
	l.record("write_file", []byte(`{"path":"a"}`), true, "permission denied")
	failStreak, _ := l.record("write_file", []byte(`{"path":"a"}`), true, "file not found")
	if failStreak != 1 {
		t.Fatalf("new error class: failStreak = %d, want 1", failStreak)
	}
	_, _, snippets := l.check("write_file", []byte(`{"path":"a"}`))
	if len(snippets) != 1 || snippets[0] != "file not found" {
		t.Fatalf("snippets not replaced: %v", snippets)
	}
}

func TestFailureLedger_DifferentArgsHash_IndependentStreak(t *testing.T) {
	l := newFailureLedger()
	l.record("write_file", []byte(`{"path":"a"}`), true, "boom")
	l.record("write_file", []byte(`{"path":"a"}`), true, "boom")
	failStreak, _, _ := l.check("write_file", []byte(`{"path":"b"}`))
	if failStreak != 0 {
		t.Fatalf("different args: failStreak = %d, want 0 (independent signature)", failStreak)
	}
	if failStreak, _ := l.record("write_file", []byte(`{"path":"b"}`), true, "boom"); failStreak != 1 {
		t.Fatalf("different args first failure: failStreak = %d, want 1", failStreak)
	}
}

func TestFailureLedger_InterleavedOtherToolCalls_StreakPreserved(t *testing.T) {
	l := newFailureLedger()
	l.record("write_file", []byte(`{"path":"a"}`), true, "boom")
	l.record("read_file", []byte(`{"path":"z"}`), false, "ok")
	failStreak, _ := l.record("write_file", []byte(`{"path":"a"}`), true, "boom")
	if failStreak != 2 {
		t.Fatalf("interleaved other-tool call reset the streak: failStreak = %d, want 2", failStreak)
	}
}

func TestErrorClass_EqualAcrossDigitVariation(t *testing.T) {
	a := errorClass("timed out after 12.4s")
	b := errorClass("timed out after 130.9s")
	if a != b {
		t.Fatalf("errorClass should ignore digit runs: %q != %q", a, b)
	}
}

func TestErrorClass_DifferentMessages_Inequal(t *testing.T) {
	a := errorClass("file not found")
	b := errorClass("permission denied")
	if a == b {
		t.Fatalf("errorClass conflated distinct messages: %q == %q", a, b)
	}
}

func TestFailureLedger_Eviction_KeepsNewest(t *testing.T) {
	l := newFailureLedger()
	for i := range 513 {
		args := fmt.Appendf(nil, `{"i":%d}`, i)
		l.record("write_file", args, true, "boom")
	}
	if len(l.entries) != 512 {
		t.Fatalf("entries = %d, want 512 after eviction", len(l.entries))
	}
	// The oldest signature (i=0) must have been evicted.
	failStreak, _, _ := l.check("write_file", []byte(`{"i":0}`))
	if failStreak != 0 {
		t.Fatalf("oldest signature should have been evicted, failStreak = %d", failStreak)
	}
	// The newest signature (i=512) must still be present.
	failStreak, _, _ = l.check("write_file", []byte(`{"i":512}`))
	if failStreak != 1 {
		t.Fatalf("newest signature should survive eviction, failStreak = %d, want 1", failStreak)
	}
}

func TestFailureLedger_SuccessThenRefailUnderEvictionPressure_SurvivesEviction(t *testing.T) {
	l := newFailureLedger()
	argsA := []byte(`{"path":"a"}`)

	// A fails, then succeeds. Since success no longer deletes the entry, A's
	// slot in the insertion order must still be exactly one entry — a stale
	// duplicate slot would push the order slice past capacity and trigger a
	// bogus eviction that deletes a live entry via key collision.
	l.record("write_file", argsA, true, "boom")
	l.record("write_file", argsA, false, "ok")

	// Fill the ledger to capacity with 511 other distinct failing signatures.
	for i := range 511 {
		args := fmt.Appendf(nil, `{"i":%d}`, i)
		l.record("write_file", args, true, "boom")
	}

	// A fails again: still the same entry (never deleted), so this must not
	// trigger any eviction of a live signature.
	failStreak, _ := l.record("write_file", argsA, true, "boom")
	if failStreak != 1 {
		t.Fatalf("refail after success: record failStreak = %d, want 1", failStreak)
	}
	failStreak, _, _ = l.check("write_file", argsA)
	if failStreak != 1 {
		t.Fatalf("refail after success: check failStreak = %d, want 1 (entry silently destroyed)", failStreak)
	}
	// None of the 511 fillers should have been evicted either — the ledger
	// holds exactly 512 live signatures (A plus 511 fillers), at capacity.
	if len(l.entries) != 512 {
		t.Fatalf("entries = %d, want 512 (no bogus eviction)", len(l.entries))
	}
}

func TestFailureLedger_SnippetTruncation_IsUTF8Safe(t *testing.T) {
	l := newFailureLedger()
	// 499 ASCII bytes followed by a 3-byte rune (€): a naive byte-index
	// truncation at 500 bytes splits the multi-byte rune and produces
	// invalid UTF-8.
	output := strings.Repeat("x", 499) + "€€€"
	l.record("write_file", []byte(`{"path":"a"}`), true, output)
	_, _, snippets := l.check("write_file", []byte(`{"path":"a"}`))
	if len(snippets) != 1 {
		t.Fatalf("snippets = %v, want 1 entry", snippets)
	}
	if !utf8.ValidString(snippets[0]) {
		t.Fatalf("snippet is not valid UTF-8: %q", snippets[0])
	}
}

func TestFailureLedger_ConcurrentRecord_ConsistentTotal(t *testing.T) {
	l := newFailureLedger()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := fmt.Appendf(nil, `{"i":%d}`, i%5)
			l.record("write_file", args, true, "boom")
		}(i)
	}
	wg.Wait()
	// 50 goroutines across 5 distinct signatures: total recorded entries must
	// be exactly 5, each with a streak between 1 and 50 (races would corrupt
	// counts or lose entries, not just get numbers wrong).
	total := 0
	l.mu.Lock()
	for _, e := range l.entries {
		total += e.count
	}
	numEntries := len(l.entries)
	l.mu.Unlock()
	if numEntries != 5 {
		t.Fatalf("entries = %d, want 5 distinct signatures", numEntries)
	}
	if total != 50 {
		t.Fatalf("summed counts = %d, want 50", total)
	}
}

// --- Task 2: repetition trigger ---

func TestFailureLedger_IdenticalSuccessBodies_RepeatStreakAdvances_FailStreakZero(t *testing.T) {
	l := newFailureLedger()
	for i, want := range []int{1, 2, 3} {
		_, repeatStreak := l.record("read_file", []byte(`{"path":"a"}`), false, "same body")
		if repeatStreak != want {
			t.Fatalf("call %d: repeatStreak = %d, want %d", i+1, repeatStreak, want)
		}
	}
	failStreak, repeatStreak, _ := l.check("read_file", []byte(`{"path":"a"}`))
	if failStreak != 0 {
		t.Fatalf("failStreak = %d, want 0", failStreak)
	}
	if repeatStreak != 3 {
		t.Fatalf("repeatStreak = %d, want 3", repeatStreak)
	}
}

func TestFailureLedger_ChangedBody_RepeatStreakResetsToOne(t *testing.T) {
	l := newFailureLedger()
	l.record("read_file", []byte(`{"path":"a"}`), false, "body one")
	l.record("read_file", []byte(`{"path":"a"}`), false, "body one")
	_, repeatStreak := l.record("read_file", []byte(`{"path":"a"}`), false, "body two")
	if repeatStreak != 1 {
		t.Fatalf("changed body: repeatStreak = %d, want 1", repeatStreak)
	}
}

func TestFailureLedger_IdenticalFailureBodies_BothCountersAdvanceTogether(t *testing.T) {
	l := newFailureLedger()
	for i, want := range []int{1, 2, 3} {
		failStreak, repeatStreak := l.record("write_file", []byte(`{"path":"a"}`), true, "boom")
		if failStreak != want {
			t.Fatalf("call %d: failStreak = %d, want %d", i+1, failStreak, want)
		}
		if repeatStreak != want {
			t.Fatalf("call %d: repeatStreak = %d, want %d", i+1, repeatStreak, want)
		}
	}
}

func TestFailureLedger_SuccessAfterFailures_ZeroesFailStreak_KeepsEntryAndBodyCount(t *testing.T) {
	l := newFailureLedger()
	l.record("write_file", []byte(`{"path":"a"}`), true, "boom")
	l.record("write_file", []byte(`{"path":"a"}`), true, "boom")
	failStreak, repeatStreak := l.record("write_file", []byte(`{"path":"a"}`), false, "done")
	if failStreak != 0 {
		t.Fatalf("success after failures: failStreak = %d, want 0", failStreak)
	}
	if repeatStreak != 1 {
		t.Fatalf("success after failures: repeatStreak = %d, want 1 (new body)", repeatStreak)
	}
	// The entry must still be live so the body hash persists across the next call.
	failStreak2, repeatStreak2 := l.record("write_file", []byte(`{"path":"a"}`), false, "done")
	if failStreak2 != 0 {
		t.Fatalf("second success: failStreak = %d, want 0", failStreak2)
	}
	if repeatStreak2 != 2 {
		t.Fatalf("second success: repeatStreak = %d, want 2 (entry survived to track body)", repeatStreak2)
	}
}

func TestFailureLedger_DifferentArgsHash_IndependentRepeatCounters(t *testing.T) {
	l := newFailureLedger()
	l.record("read_file", []byte(`{"path":"a"}`), false, "same body")
	l.record("read_file", []byte(`{"path":"a"}`), false, "same body")
	_, repeatStreak := l.record("read_file", []byte(`{"path":"b"}`), false, "same body")
	if repeatStreak != 1 {
		t.Fatalf("different args: repeatStreak = %d, want 1 (independent signature)", repeatStreak)
	}
}

func TestFailureLedger_InterleavedOtherToolCalls_RepeatStreakPreserved(t *testing.T) {
	l := newFailureLedger()
	l.record("read_file", []byte(`{"path":"a"}`), false, "same body")
	l.record("write_file", []byte(`{"path":"z"}`), false, "other body")
	_, repeatStreak := l.record("read_file", []byte(`{"path":"a"}`), false, "same body")
	if repeatStreak != 2 {
		t.Fatalf("interleaved other-tool call reset repeatStreak: %d, want 2", repeatStreak)
	}
}

func TestFailureLedger_SurvivingSuccessEntries_DoNotCorruptEviction(t *testing.T) {
	l := newFailureLedger()
	// 513 distinct signatures, all successes. Since success no longer
	// deletes, every one of these creates a live entry.
	for i := range 513 {
		args := fmt.Appendf(nil, `{"i":%d}`, i)
		l.record("read_file", args, false, "body")
	}
	if len(l.entries) != 512 {
		t.Fatalf("entries = %d, want 512 after eviction", len(l.entries))
	}
	if len(l.order) != 512 {
		t.Fatalf("order = %d, want 512 (no stale/duplicate keys)", len(l.order))
	}
	_, repeatStreak, _ := l.check("read_file", []byte(`{"i":0}`))
	if repeatStreak != 0 {
		t.Fatalf("oldest signature should have been evicted, repeatStreak = %d", repeatStreak)
	}
	_, repeatStreak, _ = l.check("read_file", []byte(`{"i":512}`))
	if repeatStreak != 1 {
		t.Fatalf("newest signature should survive eviction, repeatStreak = %d, want 1", repeatStreak)
	}
}

func TestFailureLedger_RecurringSignature_SurvivesUnrelatedChurn(t *testing.T) {
	l := newFailureLedger()
	churn := func(from, to int) {
		for i := from; i < to; i++ {
			l.record("read_file", fmt.Appendf(nil, `{"i":%d}`, i), false, "body")
		}
	}
	failing := []byte(`{"path":"broken"}`)

	// A signature that keeps failing, interleaved with a flood of distinct
	// one-off calls. Eviction must be driven by how recently a signature was
	// used, not by how long ago it was first seen — otherwise the very calls
	// the breaker exists to catch are the ones it forgets.
	l.record("write_file", failing, true, "boom")
	churn(0, 511)
	if streak, _ := l.record("write_file", failing, true, "boom"); streak != 2 {
		t.Fatalf("second failure after churn: streak = %d, want 2", streak)
	}
	// One short of the cap: an LRU keeps the recently-touched signature, a
	// FIFO evicts it. Churning a full cap's worth of new signatures would
	// evict everything under either policy and prove nothing.
	churn(511, 1022)

	streak, _, _ := l.check("write_file", failing)
	if streak != 2 {
		t.Fatalf("recurring signature evicted by unrelated churn: streak = %d, want 2", streak)
	}
}

func TestFailureLedger_ConcurrentRecord_BothCountersRaceClean(t *testing.T) {
	l := newFailureLedger()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := fmt.Appendf(nil, `{"i":%d}`, i%5)
			l.record("read_file", args, false, "same body")
		}(i)
	}
	wg.Wait()
	total := 0
	l.mu.Lock()
	for _, e := range l.entries {
		total += e.bodyCount
	}
	numEntries := len(l.entries)
	l.mu.Unlock()
	if numEntries != 5 {
		t.Fatalf("entries = %d, want 5 distinct signatures", numEntries)
	}
	if total != 50 {
		t.Fatalf("summed bodyCounts = %d, want 50", total)
	}
}
