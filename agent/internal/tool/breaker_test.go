package tool

import (
	"fmt"
	"sync"
	"testing"
)

func TestFailureLedger_IdenticalFailureTwice_StreakTwo(t *testing.T) {
	l := newFailureLedger()
	if streak := l.record("write_file", []byte(`{"path":"a"}`), true, "boom"); streak != 1 {
		t.Fatalf("first failure: streak = %d, want 1", streak)
	}
	if streak := l.record("write_file", []byte(`{"path":"a"}`), true, "boom"); streak != 2 {
		t.Fatalf("second identical failure: streak = %d, want 2", streak)
	}
	streak, snippets := l.check("write_file", []byte(`{"path":"a"}`))
	if streak != 2 {
		t.Fatalf("check: streak = %d, want 2", streak)
	}
	if len(snippets) != 2 {
		t.Fatalf("check: snippets = %v, want 2 entries", snippets)
	}
}

func TestFailureLedger_SuccessInBetween_StreakResetsToOne(t *testing.T) {
	l := newFailureLedger()
	l.record("write_file", []byte(`{"path":"a"}`), true, "boom")
	if streak := l.record("write_file", []byte(`{"path":"a"}`), false, ""); streak != 0 {
		t.Fatalf("success: streak = %d, want 0", streak)
	}
	streak, snippets := l.check("write_file", []byte(`{"path":"a"}`))
	if streak != 0 {
		t.Fatalf("check after success: streak = %d, want 0", streak)
	}
	if len(snippets) != 0 {
		t.Fatalf("check after success: snippets = %v, want none", snippets)
	}
	if streak := l.record("write_file", []byte(`{"path":"a"}`), true, "boom"); streak != 1 {
		t.Fatalf("failure after success: streak = %d, want 1", streak)
	}
}

func TestFailureLedger_DifferentErrorClass_StreakResetsAndSnippetsReplaced(t *testing.T) {
	l := newFailureLedger()
	l.record("write_file", []byte(`{"path":"a"}`), true, "permission denied")
	l.record("write_file", []byte(`{"path":"a"}`), true, "permission denied")
	streak := l.record("write_file", []byte(`{"path":"a"}`), true, "file not found")
	if streak != 1 {
		t.Fatalf("new error class: streak = %d, want 1", streak)
	}
	_, snippets := l.check("write_file", []byte(`{"path":"a"}`))
	if len(snippets) != 1 || snippets[0] != "file not found" {
		t.Fatalf("snippets not replaced: %v", snippets)
	}
}

func TestFailureLedger_DifferentArgsHash_IndependentStreak(t *testing.T) {
	l := newFailureLedger()
	l.record("write_file", []byte(`{"path":"a"}`), true, "boom")
	l.record("write_file", []byte(`{"path":"a"}`), true, "boom")
	streak, _ := l.check("write_file", []byte(`{"path":"b"}`))
	if streak != 0 {
		t.Fatalf("different args: streak = %d, want 0 (independent signature)", streak)
	}
	if streak := l.record("write_file", []byte(`{"path":"b"}`), true, "boom"); streak != 1 {
		t.Fatalf("different args first failure: streak = %d, want 1", streak)
	}
}

func TestFailureLedger_InterleavedOtherToolCalls_StreakPreserved(t *testing.T) {
	l := newFailureLedger()
	l.record("write_file", []byte(`{"path":"a"}`), true, "boom")
	l.record("read_file", []byte(`{"path":"z"}`), false, "")
	streak := l.record("write_file", []byte(`{"path":"a"}`), true, "boom")
	if streak != 2 {
		t.Fatalf("interleaved other-tool call reset the streak: streak = %d, want 2", streak)
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
	for i := 0; i < 257; i++ {
		args := []byte(fmt.Sprintf(`{"i":%d}`, i))
		l.record("write_file", args, true, "boom")
	}
	if len(l.entries) != 256 {
		t.Fatalf("entries = %d, want 256 after eviction", len(l.entries))
	}
	// The oldest signature (i=0) must have been evicted.
	streak, _ := l.check("write_file", []byte(`{"i":0}`))
	if streak != 0 {
		t.Fatalf("oldest signature should have been evicted, streak = %d", streak)
	}
	// The newest signature (i=256) must still be present.
	streak, _ = l.check("write_file", []byte(`{"i":256}`))
	if streak != 1 {
		t.Fatalf("newest signature should survive eviction, streak = %d, want 1", streak)
	}
}

func TestFailureLedger_ConcurrentRecord_ConsistentTotal(t *testing.T) {
	l := newFailureLedger()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := []byte(fmt.Sprintf(`{"i":%d}`, i%5))
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
