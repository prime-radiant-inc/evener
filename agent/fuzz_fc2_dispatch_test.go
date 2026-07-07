//go:build serffuzz

package agent

import (
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// FuzzFc2ClassifyJobReadWindow drives classifyJobReadWindow — the pure read-window
// dispatch core lifted out of jobReadOutputTool's switch — over the cross-product
// of the grep / from_line / head / tail presence flags. Oracles (beyond
// never-panic):
//   - determinism;
//   - totality: the result is always one of the six defined modes;
//   - precedence matches the request contract (grep dominates, then from_line,
//     then head+tail, then a one-sided head or tail, then the default digest);
//   - the default digest is chosen iff no flag is set.
func FuzzFc2ClassifyJobReadWindow(f *testing.F) {
	f.Add(true, false, false, false)
	f.Add(false, true, false, false)
	f.Add(false, false, true, true)
	f.Add(false, false, true, false)
	f.Add(false, false, false, true)
	f.Add(false, false, false, false)

	f.Fuzz(func(t *testing.T, hasGrep, hasFromLine, hasHead, hasTail bool) {
		mode := classifyJobReadWindow(hasGrep, hasFromLine, hasHead, hasTail)
		if mode2 := classifyJobReadWindow(hasGrep, hasFromLine, hasHead, hasTail); mode != mode2 {
			t.Fatalf("non-deterministic: %v vs %v", mode, mode2)
		}
		switch mode {
		case jobReadModeGrep, jobReadModeFromLine, jobReadModeHeadTail, jobReadModeHead, jobReadModeTail, jobReadModeDigest:
		default:
			t.Fatalf("invalid mode %v", mode)
		}

		var want jobReadWindowMode
		switch {
		case hasGrep:
			want = jobReadModeGrep
		case hasFromLine:
			want = jobReadModeFromLine
		case hasHead && hasTail:
			want = jobReadModeHeadTail
		case hasHead:
			want = jobReadModeHead
		case hasTail:
			want = jobReadModeTail
		default:
			want = jobReadModeDigest
		}
		if mode != want {
			t.Fatalf("precedence: (grep=%v,from=%v,head=%v,tail=%v) got %v, want %v",
				hasGrep, hasFromLine, hasHead, hasTail, mode, want)
		}
		if (mode == jobReadModeDigest) != (!hasGrep && !hasFromLine && !hasHead && !hasTail) {
			t.Fatalf("digest chosen iff no flag set, mismatch: mode=%v flags(%v,%v,%v,%v)",
				mode, hasGrep, hasFromLine, hasHead, hasTail)
		}
	})
}

// FuzzFc2ClassifyStopOutcome drives classifyStopOutcome — the pure decision that
// distinguishes a stop that cancelled a live job from one that raced with or
// arrived after its own completion. Oracles (beyond never-panic):
//   - determinism;
//   - totality: the result is one of the four defined outcomes;
//   - a previously-terminal job is always "already_terminal", whatever the record.
func FuzzFc2ClassifyStopOutcome(f *testing.F) {
	f.Add(uint8(0), uint8(0), true)  // previous running, rec nil
	f.Add(uint8(0), uint8(2), false) // previous running, rec cancelled
	f.Add(uint8(1), uint8(1), false) // previous terminal
	f.Add(uint8(0), uint8(1), false) // previous running, rec completed

	statuses := []jobstore.Status{
		jobstore.StatusRunning, jobstore.StatusCompleted, jobstore.StatusCancelled,
		jobstore.StatusStopped, jobstore.StatusFailed,
	}

	f.Fuzz(func(t *testing.T, prevSel, recSel uint8, recNil bool) {
		previous := statuses[int(prevSel)%len(statuses)]
		var rec *jobstore.JobRecord
		if !recNil {
			rec = &jobstore.JobRecord{Status: statuses[int(recSel)%len(statuses)]}
		}

		out := classifyStopOutcome(previous, rec)
		if out2 := classifyStopOutcome(previous, rec); out != out2 {
			t.Fatalf("non-deterministic: %q vs %q", out, out2)
		}
		switch out {
		case "already_terminal", "stop_requested", "cancelled_by_request", "completed_during_stop":
		default:
			t.Fatalf("invalid outcome %q", out)
		}
		if previous.IsTerminal() && out != "already_terminal" {
			t.Fatalf("previous terminal (%q) but outcome=%q", previous, out)
		}
	})
}

// FuzzFc2OutputWindowStatus drives outputWindowStatus — the pure retention/window
// classifier of a job-output read. Oracles (beyond never-panic):
//   - determinism;
//   - totality: the result is one of the three defined statuses;
//   - eviction dominates windowing (any dropped bytes ⇒ "evicted", regardless of
//     the truncation flag).
func FuzzFc2OutputWindowStatus(f *testing.F) {
	f.Add(int64(0), int64(0), false)
	f.Add(int64(100), int64(0), true)
	f.Add(int64(100), int64(50), false)
	f.Add(int64(100), int64(50), true)

	f.Fuzz(func(t *testing.T, total, dropped int64, truncated bool) {
		got := outputWindowStatus(total, dropped, truncated)
		if got2 := outputWindowStatus(total, dropped, truncated); got != got2 {
			t.Fatalf("non-deterministic: %q vs %q", got, got2)
		}
		switch got {
		case "evicted", "windowed", "all_retained":
		default:
			t.Fatalf("invalid status %q", got)
		}
		if dropped > 0 && got != "evicted" {
			t.Fatalf("dropped=%d but status=%q (eviction must dominate)", dropped, got)
		}
		if dropped <= 0 && truncated && got != "windowed" {
			t.Fatalf("no drop + truncated but status=%q", got)
		}
		if dropped <= 0 && !truncated && got != "all_retained" {
			t.Fatalf("no drop + not truncated but status=%q", got)
		}
	})
}

// FuzzFc2ToolStartDescription drives toolStartDescription — the pure description
// promotion lifted out of execTool: an explicit "purpose" wins over the legacy
// "description", else empty. Oracles (beyond never-panic):
//   - determinism;
//   - a non-empty string "purpose" is always returned verbatim;
//   - otherwise a non-empty string "description" is returned;
//   - otherwise the result is empty.
func FuzzFc2ToolStartDescription(f *testing.F) {
	f.Add("do the thing", "shell desc", true, true)
	f.Add("", "shell desc", false, true)
	f.Add("", "", false, false)
	f.Add("purpose only", "", true, false)

	f.Fuzz(func(t *testing.T, purpose, desc string, hasPurpose, hasDesc bool) {
		args := map[string]any{}
		// Vary the value TYPES too: only a string value should be honored.
		if hasPurpose {
			args["purpose"] = purpose
		} else {
			args["purpose"] = 42 // non-string: ignored
		}
		if hasDesc {
			args["description"] = desc
		} else {
			args["description"] = []any{"x"} // non-string: ignored
		}

		got := toolStartDescription(args)
		if got2 := toolStartDescription(args); got != got2 {
			t.Fatalf("non-deterministic: %q vs %q", got, got2)
		}

		wantPurpose := hasPurpose && purpose != ""
		wantDesc := hasDesc && desc != ""
		switch {
		case wantPurpose:
			if got != purpose {
				t.Fatalf("purpose set but got %q, want %q", got, purpose)
			}
		case wantDesc:
			if got != desc {
				t.Fatalf("description fallback but got %q, want %q", got, desc)
			}
		default:
			if got != "" {
				t.Fatalf("no usable field but got %q", got)
			}
		}
	})
}