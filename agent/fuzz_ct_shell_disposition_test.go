//go:build serffuzz

package agent

import "testing"

// FuzzCtShellResultDisposition drives shellResultDisposition — the keep-vs-discard
// decision core lifted out of marshalCompleteOrHandleResult — over adversarial
// size inputs. Oracles: determinism; Keep matches the two-layer predicate;
// the two reasons are mutually exclusive (embed short-circuits char-bound);
// Keep monotone non-decreasing in output size and rendered length; and
// Keep==false implies both sub-limits are satisfied.
func FuzzCtShellResultDisposition(f *testing.F) {
	f.Add(0, 0, 0, 8192)
	f.Add(100000, 50, 40000, 8192)
	f.Add(500, 100, 20, 8192)
	f.Add(9000, 5, 0, 8192)

	f.Fuzz(func(t *testing.T, rawOutputBytes, renderedRuneLen, maxChars, rideWholeBytes int) {
		// Bound magnitudes so the +1 monotonicity probes cannot overflow int.
		rawOutputBytes %= 1 << 30
		renderedRuneLen %= 1 << 30
		maxChars %= 1 << 30
		rideWholeBytes %= 1 << 30

		disp := shellResultDisposition(rawOutputBytes, renderedRuneLen, maxChars, rideWholeBytes)
		if disp2 := shellResultDisposition(rawOutputBytes, renderedRuneLen, maxChars, rideWholeBytes); disp != disp2 {
			t.Fatalf("non-deterministic: %+v vs %+v", disp, disp2)
		}

		wantKeep := rawOutputBytes > rideWholeBytes || (maxChars > 0 && renderedRuneLen > maxChars)
		if disp.Keep != wantKeep {
			t.Fatalf("Keep=%v want %v (raw=%d rendered=%d maxChars=%d ride=%d)",
				disp.Keep, wantKeep, rawOutputBytes, renderedRuneLen, maxChars, rideWholeBytes)
		}
		if disp.EmbedExceeded && disp.CharBoundExceeded {
			t.Fatalf("both reasons attributed: %+v", disp)
		}
		if !disp.Keep {
			if disp.EmbedExceeded || disp.CharBoundExceeded {
				t.Fatalf("no keep but a reason set: %+v", disp)
			}
			if rawOutputBytes > rideWholeBytes {
				t.Fatalf("no keep but embed limit exceeded: %+v", disp)
			}
			if maxChars > 0 && renderedRuneLen > maxChars {
				t.Fatalf("no keep but char bound exceeded: %+v", disp)
			}
		}
		// Monotone non-decreasing in rawOutputBytes and renderedRuneLen.
		if disp.Keep {
			if !shellResultDisposition(rawOutputBytes+1, renderedRuneLen, maxChars, rideWholeBytes).Keep {
				t.Fatalf("Keep regressed when raw output grew")
			}
			if !shellResultDisposition(rawOutputBytes, renderedRuneLen+1, maxChars, rideWholeBytes).Keep {
				t.Fatalf("Keep regressed when rendered length grew")
			}
		}
	})
}
