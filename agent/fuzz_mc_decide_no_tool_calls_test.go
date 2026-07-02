//go:build serffuzz

package agent

import (
	"strings"
	"testing"
)

// clampNonNeg maps a fuzzed int to a bounded non-negative value so counter
// increments cannot overflow inside the decision core.
func clampNonNeg(v int) int {
	if v < 0 {
		v = -v
	}
	if v < 0 { // math.MinInt after negation
		v = 0
	}
	return v % (1 << 20)
}

// FuzzMcDecideNoToolCalls drives decideNoToolCalls — the pure decision core lifted
// out of handleNoToolCalls — over adversarial counter snapshots, asserting the
// invariants the effectful wrapper relies on. Oracles (beyond never-panic):
//   - determinism: the same inputs yield the same decision;
//   - Retry XOR TerminalKind!=none;
//   - SteeringText!="" iff Retry;
//   - counters never decrease except the documented consecutiveEmpty=0 reset on the
//     bare-text branch;
//   - on the noContent path consecutiveEmpty & totalEmpty both increment;
//   - empty-budget exhaustion ⇒ emptyExhausted;
//   - the three empty-steering variants map to consecutiveEmpty 1/2/>=3.
func FuzzMcDecideNoToolCalls(f *testing.F) {
	f.Add(true, 0, 0, 0, "communicate")  // first empty -> variant 1
	f.Add(true, 1, 1, 0, "communicate")  // second empty -> variant 2
	f.Add(true, 2, 2, 0, "result")       // third empty -> variant >=3
	f.Add(true, 3, 3, 0, "communicate")  // empty budget exhausted
	f.Add(true, 0, 8, 0, "communicate")  // total-empty budget exhausted
	f.Add(false, 5, 5, 0, "communicate") // bare text, retry
	f.Add(false, 0, 0, 3, "result_tool") // bare text budget exhausted

	f.Fuzz(func(t *testing.T, noContent bool, consecutiveEmpty, totalEmpty, consecutiveBareText int, resultToolName string) {
		in := retryTracker{
			consecutiveEmpty:    clampNonNeg(consecutiveEmpty),
			totalEmpty:          clampNonNeg(totalEmpty),
			consecutiveBareText: clampNonNeg(consecutiveBareText),
		}

		dec := decideNoToolCalls(noContent, in, resultToolName)
		if dec2 := decideNoToolCalls(noContent, in, resultToolName); dec != dec2 {
			t.Fatalf("non-deterministic: %+v vs %+v", dec, dec2)
		}

		// Retry XOR terminal.
		terminal := dec.TerminalKind != noToolTerminalNone
		if dec.Retry == terminal {
			t.Fatalf("Retry and terminal not mutually exclusive: %+v", dec)
		}
		if (dec.SteeringText != "") != dec.Retry {
			t.Fatalf("SteeringText!=\"\" must match Retry: %+v", dec)
		}

		// Counter accounting.
		if noContent {
			if dec.Tracker.consecutiveEmpty != in.consecutiveEmpty+1 {
				t.Fatalf("consecutiveEmpty not incremented: %d -> %d", in.consecutiveEmpty, dec.Tracker.consecutiveEmpty)
			}
			if dec.Tracker.totalEmpty != in.totalEmpty+1 {
				t.Fatalf("totalEmpty not incremented: %d -> %d", in.totalEmpty, dec.Tracker.totalEmpty)
			}
			if dec.Tracker.consecutiveBareText != in.consecutiveBareText {
				t.Fatalf("consecutiveBareText changed on empty path: %d -> %d", in.consecutiveBareText, dec.Tracker.consecutiveBareText)
			}
			// Empty-budget exhaustion.
			exhausted := dec.Tracker.consecutiveEmpty > maxEmptyRetries || dec.Tracker.totalEmpty > maxTotalEmptyResponses
			if exhausted {
				if dec.TerminalKind != noToolTerminalEmptyExhausted {
					t.Fatalf("expected emptyExhausted, got %v", dec.TerminalKind)
				}
			} else {
				if !dec.Retry {
					t.Fatalf("expected retry within budget: %+v", dec)
				}
				// Empty-steering variant mapping.
				switch dec.Tracker.consecutiveEmpty {
				case 1:
					if !strings.Contains(dec.SteeringText, "Your previous response was empty") {
						t.Fatalf("variant 1 mismatch: %q", dec.SteeringText)
					}
				case 2:
					if !strings.Contains(dec.SteeringText, "empty again") {
						t.Fatalf("variant 2 mismatch: %q", dec.SteeringText)
					}
				default:
					if !strings.Contains(dec.SteeringText, "multiple empty responses") {
						t.Fatalf("variant >=3 mismatch: %q", dec.SteeringText)
					}
				}
			}
		} else {
			if dec.Tracker.consecutiveEmpty != 0 {
				t.Fatalf("consecutiveEmpty not reset on bare-text branch: %d", dec.Tracker.consecutiveEmpty)
			}
			if dec.Tracker.consecutiveBareText != in.consecutiveBareText+1 {
				t.Fatalf("consecutiveBareText not incremented: %d -> %d", in.consecutiveBareText, dec.Tracker.consecutiveBareText)
			}
			if dec.Tracker.totalEmpty != in.totalEmpty {
				t.Fatalf("totalEmpty changed on bare-text path: %d -> %d", in.totalEmpty, dec.Tracker.totalEmpty)
			}
			if dec.Tracker.consecutiveBareText > maxBareTextRetries {
				if dec.TerminalKind != noToolTerminalBareTextExhausted {
					t.Fatalf("expected bareTextExhausted, got %v", dec.TerminalKind)
				}
			}
		}
	})
}
