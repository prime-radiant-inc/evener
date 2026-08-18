package agent

import (
	"fmt"
	"testing"
)

// toolSig is a plain (name, args) pair -- the same shape observeToolCall
// takes -- used to build the synthetic fixtures shared by the loop-guard
// unit tests and its consumeModelStream integration tests.
type toolSig struct{ name, args string }

// buildEightyThreeCallNoCycleFixture reconstructs kata d74b's second measured
// shape honestly: "83 calls / 48 distinct signatures / max repeat 12". The
// captured stream itself is not in the repo (rebuilding the capture harness
// was explicitly out of bounds), so this is a synthetic stand-in built to the
// documented statistics, NOT a byte-for-byte replay.
//
// Construction: one "heavy" signature H repeats 12 times at absolute
// positions 0,7,14,...,77 (gap 7, so it can never appear more than once in
// any window the cycle detector examines -- max window is k*R=25). The other
// 71 positions cycle through 47 distinct filler signatures in round-robin
// order (period 47, far above the max cycle length of 5). Total: 12 + 71 =
// 83 calls, 1 + 47 = 48 distinct signatures, max repeat = 12.
//
// Note this is an approximation of the captured incident's exact texture
// (which interleaving pattern the real 48-signature/83-call stream actually
// used is unknown -- only the three summary statistics were recorded), built
// specifically to avoid the one property that would make the fixture beg the
// question: it must not contain a short cycle, or the cycle detector would
// trip it "by accident" and the test would prove nothing about the ceiling.
func buildEightyThreeCallNoCycleFixture(t *testing.T) []toolSig {
	t.Helper()
	const total = 83
	const heavyCount = 12
	const heavyGap = 7 // 0,7,...,77 -> 12 positions, all < 83

	heavy := toolSig{name: "manage_worktree", args: `{"op":"status"}`}
	fillers := make([]toolSig, 47)
	for i := range fillers {
		fillers[i] = toolSig{name: fmt.Sprintf("tool_%02d", i), args: fmt.Sprintf(`{"call":%d}`, i)}
	}

	heavyPositions := make(map[int]bool, heavyCount)
	for i := range heavyCount {
		heavyPositions[i*heavyGap] = true
	}

	sigs := make([]toolSig, 0, total)
	fillerIdx := 0
	for pos := range total {
		if heavyPositions[pos] {
			sigs = append(sigs, heavy)
			continue
		}
		sigs = append(sigs, fillers[fillerIdx%len(fillers)])
		fillerIdx++
	}

	if got := countDistinctSigs(sigs); got != 48 {
		t.Fatalf("fixture generator bug: %d distinct signatures, want 48", got)
	}
	if got := maxRepeatCount(sigs); got != heavyCount {
		t.Fatalf("fixture generator bug: max repeat = %d, want %d", got, heavyCount)
	}
	if len(sigs) != total {
		t.Fatalf("fixture generator bug: %d calls, want %d", len(sigs), total)
	}
	return sigs
}

func countDistinctSigs(sigs []toolSig) int {
	seen := map[string]bool{}
	for _, s := range sigs {
		seen[s.name+":"+s.args] = true
	}
	return len(seen)
}

func maxRepeatCount(sigs []toolSig) int {
	counts := map[string]int{}
	maxCount := 0
	for _, s := range sigs {
		key := s.name + ":" + s.args
		counts[key]++
		if counts[key] > maxCount {
			maxCount = counts[key]
		}
	}
	return maxCount
}

// assertNoShortCycleAnywhere offline-checks every suffix of sigs for a
// k=1..5 pattern repeated 5 times consecutively, using the same window
// arithmetic checkCycle uses. It exists so the ceiling-fixture test's claim
// ("this shape has no short cycle by construction") is verified mechanically
// rather than trusted from the construction's math alone.
func assertNoShortCycleAnywhere(t *testing.T, sigs []toolSig) {
	t.Helper()
	g := newStreamLoopGuard()
	for i, s := range sigs {
		g.sigs = append(g.sigs, s.name+":"+s.args)
		if trip := g.checkCycle(); trip != nil {
			t.Fatalf("fixture contains a short cycle at call %d: %+v (fixture is invalid for the ceiling-only test)", i+1, trip)
		}
	}
}
