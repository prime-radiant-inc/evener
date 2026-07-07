//go:build serffuzz

package agent

import (
	"testing"
	"time"
)

// FuzzLifecycleSeq is the COVERAGE-GUIDED counterpart to TestLifecycleSeqFuzz
// (Phase 10 W3). TestLifecycleSeqFuzz draws op sequences with rapid (uniform
// random); this target decodes the fuzzer's []byte into the SAME lifecycleArtifact
// and runs the SAME oracle, so Go's coverage-guided engine steers the byte stream
// into deep op interleavings (delegate-during-compaction-during-background-shell,
// the fault op mid-sequence) that uniform sampling rarely reaches. It adds no new
// oracle — its value is REACH. The byte->op decode is intentionally stable so the
// persisted corpus keeps its meaning across edits (append new ops, never reorder).
func FuzzLifecycleSeq(f *testing.F) {
	// Seeds: short, valid op programs spanning the families. Bytes are op-stream
	// instructions, not raw transcript — see seqReader.
	f.Add([]byte{0, 1, 0, 0})                            // one process_input
	f.Add([]byte{3, 8, 0, 0, 9, 0, 1, 0, 13})            // delegate, bg-shell-ish, close-ish
	f.Add([]byte{6, 12, 0, 0, 12, 0, 0, 1, 0, 0, 13})    // llm_error fault then input
	f.Add([]byte{20, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}) // many ops

	f.Fuzz(func(t *testing.T, data []byte) {
		art := decodeLifecycleArtifact(data)
		if fail := lifecycleOracleRun(art); fail != nil {
			t.Fatalf("lifecycle oracle (coverage-guided): %s", fail.Detail)
		}
	})
}

// seqReader is a stable cursor over the fuzzer's bytes. Out of bytes -> 0, so a
// short input decodes deterministically (and a longer one is a strict superset).
type seqReader struct {
	data []byte
	pos  int
}

func (r *seqReader) next() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

// intn returns next() reduced into [0,n). n<=0 yields 0.
func (r *seqReader) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next()) % n
}

// decodeLifecycleArtifact maps bytes to a lifecycleArtifact, mirroring
// drawLifecycleArtifact's per-op field shapes (so a decoded program is always a
// legal artifact the oracle can run) but driven by the coverage-guided bytes.
func decodeLifecycleArtifact(data []byte) lifecycleArtifact {
	r := &seqReader{data: data}
	nOps := r.intn(24) + 1
	ops := make([]opRecord, 0, nOps)
	for i := 0; i < nOps; i++ {
		code := allLifecycleOps[r.intn(len(allLifecycleOps))]
		rec := opRecord{Code: int(code)}
		switch code {
		case opProcessInput, opProcessInterrupted, opLLMError:
			scriptLen := r.intn(5) + 1
			rec.Script = make([]int, scriptLen)
			for j := range rec.Script {
				rec.Script[j] = r.intn(int(numResponseKinds))
			}
			rec.Text = inputTexts[r.intn(len(inputTexts))]
			if code == opProcessInterrupted {
				rec.IntAt = r.intn(scriptLen + 1)
			}
		case opSteer, opEnqueue, opFollowUp, opSetGoal:
			rec.Text = inputTexts[r.intn(len(inputTexts))]
		case opAdvanceClock:
			rec.Dur = int64(r.next()) * int64(time.Second) // 0..255s, within the rapid range
		case opDelegate, opBackgroundDelegate:
			rec.Script = []int{int(kindDelegate)}
			rec.Text = inputTexts[r.intn(len(inputTexts))]
			csLen := r.intn(4) + 1
			rec.ChildScript = make([]int, csLen)
			for j := range rec.ChildScript {
				rec.ChildScript[j] = r.intn(int(numResponseKinds))
			}
		case opBackgroundShell:
			rec.Script = []int{int(kindShellBackground)}
			rec.Text = inputTexts[r.intn(len(inputTexts))]
		}
		ops = append(ops, rec)
	}
	return lifecycleArtifact{Ops: ops, EnvSeed: uint64(r.next())}
}