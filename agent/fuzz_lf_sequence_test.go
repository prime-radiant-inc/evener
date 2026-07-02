//go:build serffuzz

package agent

import (
	"encoding/json"
	"testing"
	"time"
)

// FuzzLifecycleSequence bridges the stateful lifecycle sequence fuzzer
// (TestLifecycleSeqFuzz, a rapid.Check property) into a native `go test -fuzz`
// target so the WHOLE agent turn-loop / handleModelError / drain / compaction /
// delegate / job orchestration it drives counts toward fuzz-reachable coverage
// (the `^Fuzz` metric skips rapid Test* functions) and go-fuzz can mutate whole
// op sequences. Each input is a JSON-encoded lifecycleArtifact; it is decoded and
// CLAMPED to a structurally valid op sequence, then replayed through the same
// offline harness (deny env, fake clock, scripted adapter) and checked by the
// same oracles via lifecycleOracleRun. The seed corpus drives the big paths
// end-to-end — every handleModelError fault arm, web-search/thinking content,
// foreground+background shell, delegate, background delegate, goal, interrupt,
// steer, compaction, close — so replaying the seeds alone reaches deep into the
// orchestration; mutation explores from there.
func FuzzLifecycleSequence(f *testing.F) {
	seeds := []lifecycleArtifact{
		// A plain tool-using turn to completion.
		{Ops: []opRecord{{Code: int(opProcessInput), Script: []int{int(kindReadFile), int(kindFinal)}, Text: "hi"}}},
		// Every handleModelError arm (one fault per op, one-shot).
		{Ops: []opRecord{{Code: int(opLLMError), Script: []int{int(kindFinal)}, Text: "x", FaultKind: int(faultContentFilter)}}},
		{Ops: []opRecord{{Code: int(opLLMError), Script: []int{int(kindFinal)}, Text: "x", FaultKind: int(faultAuth)}}},
		{Ops: []opRecord{{Code: int(opLLMError), Script: []int{int(kindFinal)}, Text: "x", FaultKind: int(faultContextLength)}}},
		{Ops: []opRecord{{Code: int(opLLMError), Script: []int{int(kindFinal)}, Text: "x", FaultKind: int(faultRateLimit)}}},
		{Ops: []opRecord{{Code: int(opLLMError), Script: []int{int(kindFinal)}, Text: "x", FaultKind: int(faultServer)}}},
		// Web-search + thinking content responses (recordResponseUsage + classify).
		{Ops: []opRecord{
			{Code: int(opProcessInput), Script: []int{int(kindWebSearch)}, Text: "x"},
			{Code: int(opProcessInput), Script: []int{int(kindThinking), int(kindFinal)}, Text: "y"},
		}},
		// Foreground shell + forced compaction in one turn.
		{Ops: []opRecord{{Code: int(opProcessInput), Script: []int{int(kindShell), int(kindCompact), int(kindFinal)}, Text: "run"}}},
		// Leaf delegate + background shell + background delegate.
		{Ops: []opRecord{{Code: int(opDelegate), Script: []int{int(kindDelegate)}, Text: "task", ChildScript: []int{int(kindFinal)}}}},
		{Ops: []opRecord{{Code: int(opBackgroundShell), Script: []int{int(kindShellBackground)}, Text: "bg"}}},
		{Ops: []opRecord{{Code: int(opBackgroundDelegate), Script: []int{int(kindDelegate)}, Text: "bg", ChildScript: []int{int(kindText), int(kindFinal)}}}},
		// Goal set/run/clear, and interrupt+steer+close.
		{Ops: []opRecord{{Code: int(opSetGoal), Text: "goal"}, {Code: int(opProcessInput), Script: []int{int(kindFinal)}, Text: "go"}, {Code: int(opClearGoal)}}},
		{Ops: []opRecord{
			{Code: int(opProcessInterrupted), Script: []int{int(kindReadFile), int(kindReadFile)}, Text: "x", IntAt: 1},
			{Code: int(opSteer), Text: "steer"},
			{Code: int(opClose)},
		}},
	}
	for _, s := range seeds {
		b, err := json.Marshal(s)
		if err != nil {
			f.Fatalf("marshal seed: %v", err)
		}
		f.Add(b)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		var art lifecycleArtifact
		if err := json.Unmarshal(data, &art); err != nil {
			return // not a valid artifact encoding — skip (not a corpus failure)
		}
		art = clampLifecycleArtifact(art)
		if len(art.Ops) == 0 {
			return
		}
		if fl := lifecycleOracleRun(art); fl != nil {
			t.Fatalf("lifecycle oracle: %s", fl.Detail)
		}
	})
}

// clampLifecycleArtifact maps an arbitrary decoded artifact onto the structurally
// valid space drawLifecycleArtifact produces, so a mutated/garbage input can never
// panic the harness itself (only a genuine defect in the code under test can): the
// op count and per-op script/child lengths are bounded, op codes and fault/response
// kinds are folded into range, and structural ops (delegate / background shell /
// background delegate) get their required scripts re-derived — mirroring the rapid
// draw — so those spawn paths stay reachable regardless of the raw bytes.
func clampLifecycleArtifact(art lifecycleArtifact) lifecycleArtifact {
	const maxOps, maxScript, maxChild = 24, 5, 4
	if len(art.Ops) > maxOps {
		art.Ops = art.Ops[:maxOps]
	}
	for i := range art.Ops {
		op := &art.Ops[i]
		code := allLifecycleOps[nonNegMod(op.Code, len(allLifecycleOps))]
		op.Code = int(code)
		op.FaultKind = nonNegMod(op.FaultKind, int(numFaultKinds))
		if op.Dur < 0 {
			op.Dur = 0
		} else if op.Dur > int64(5*time.Minute) {
			op.Dur = int64(5 * time.Minute)
		}
		if len(op.Script) > maxScript {
			op.Script = op.Script[:maxScript]
		}
		for j := range op.Script {
			op.Script[j] = nonNegMod(op.Script[j], int(numResponseKinds))
		}
		if len(op.ChildScript) > maxChild {
			op.ChildScript = op.ChildScript[:maxChild]
		}
		for j := range op.ChildScript {
			op.ChildScript[j] = nonNegMod(op.ChildScript[j], int(numResponseKinds))
		}
		switch code {
		case opProcessInput, opProcessInterrupted, opLLMError:
			if len(op.Script) == 0 {
				op.Script = []int{int(kindFinal)}
			}
			if op.IntAt < 0 {
				op.IntAt = 0
			} else if op.IntAt > len(op.Script) {
				op.IntAt = len(op.Script)
			}
		case opDelegate, opBackgroundDelegate:
			op.Script = []int{int(kindDelegate)}
			if len(op.ChildScript) == 0 {
				op.ChildScript = []int{int(kindFinal)}
			}
		case opBackgroundShell:
			op.Script = []int{int(kindShellBackground)}
		default:
			op.Script = nil
			op.ChildScript = nil
		}
	}
	return art
}

// nonNegMod returns x mod n folded into [0,n) (never negative); 0 when n<=0.
func nonNegMod(x, n int) int {
	if n <= 0 {
		return 0
	}
	x %= n
	if x < 0 {
		x += n
	}
	return x
}
