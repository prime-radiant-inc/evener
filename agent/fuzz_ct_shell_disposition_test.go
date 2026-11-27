//go:build evenerfuzz

package agent

import "testing"

// FuzzCtShellResultDisposition drives shellResultDisposition — the keep-vs-discard
// decision core lifted out of marshalCompleteOrHandleResult — over adversarial
// size inputs. Oracles: determinism; Keep matches the three-check predicate;
// the three reasons are mutually exclusive (embed short-circuits char-bound,
// which short-circuits line-bound); Keep monotone non-decreasing in output
// size, rendered length, and rendered line count; and Keep==false implies all
// sub-limits are satisfied.
func FuzzCtShellResultDisposition(f *testing.F) {
	f.Add(0, 0, 0, 1, 0, 6144)
	f.Add(100000, 50, 6000, 10, 120, 6144)
	f.Add(500, 100, 20, 10, 120, 6144)
	f.Add(1200, 1200, 6000, 151, 120, 6144)
	f.Add(9000, 5, 0, 1, 120, 6144)

	f.Fuzz(func(t *testing.T, rawOutputBytes, renderedRuneLen, maxChars, renderedLines, maxLines, rideWholeBytes int) {
		// Bound magnitudes so the +1 monotonicity probes cannot overflow int.
		rawOutputBytes %= 1 << 30
		renderedRuneLen %= 1 << 30
		maxChars %= 1 << 30
		renderedLines %= 1 << 30
		maxLines %= 1 << 30
		rideWholeBytes %= 1 << 30

		disp := shellResultDisposition(rawOutputBytes, renderedRuneLen, maxChars, renderedLines, maxLines, rideWholeBytes)
		if disp2 := shellResultDisposition(rawOutputBytes, renderedRuneLen, maxChars, renderedLines, maxLines, rideWholeBytes); disp != disp2 {
			t.Fatalf("non-deterministic: %+v vs %+v", disp, disp2)
		}

		wantKeep := rawOutputBytes > rideWholeBytes || (maxChars > 0 && renderedRuneLen > maxChars) || (maxLines > 0 && renderedLines > maxLines)
		if disp.Keep != wantKeep {
			t.Fatalf("Keep=%v want %v (raw=%d rendered=%d maxChars=%d lines=%d maxLines=%d ride=%d)",
				disp.Keep, wantKeep, rawOutputBytes, renderedRuneLen, maxChars, renderedLines, maxLines, rideWholeBytes)
		}
		reasons := 0
		for _, r := range []bool{disp.EmbedExceeded, disp.CharBoundExceeded, disp.LineBoundExceeded} {
			if r {
				reasons++
			}
		}
		if reasons > 1 {
			t.Fatalf("multiple reasons attributed: %+v", disp)
		}
		if !disp.Keep {
			if reasons != 0 {
				t.Fatalf("no keep but a reason set: %+v", disp)
			}
			if rawOutputBytes > rideWholeBytes {
				t.Fatalf("no keep but embed limit exceeded: %+v", disp)
			}
			if maxChars > 0 && renderedRuneLen > maxChars {
				t.Fatalf("no keep but char bound exceeded: %+v", disp)
			}
			if maxLines > 0 && renderedLines > maxLines {
				t.Fatalf("no keep but line bound exceeded: %+v", disp)
			}
		}
		// Monotone non-decreasing in rawOutputBytes, renderedRuneLen, and renderedLines.
		if disp.Keep {
			if !shellResultDisposition(rawOutputBytes+1, renderedRuneLen, maxChars, renderedLines, maxLines, rideWholeBytes).Keep {
				t.Fatalf("Keep regressed when raw output grew")
			}
			if !shellResultDisposition(rawOutputBytes, renderedRuneLen+1, maxChars, renderedLines, maxLines, rideWholeBytes).Keep {
				t.Fatalf("Keep regressed when rendered length grew")
			}
			if !shellResultDisposition(rawOutputBytes, renderedRuneLen, maxChars, renderedLines+1, maxLines, rideWholeBytes).Keep {
				t.Fatalf("Keep regressed when rendered lines grew")
			}
		}
	})
}
