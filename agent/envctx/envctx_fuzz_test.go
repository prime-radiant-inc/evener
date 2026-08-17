//go:build serffuzz

package envctx

import (
	"math"
	"strings"
	"testing"
)

// FuzzEnvctxTrackerDiff drives the two seams in this package that see input
// nobody controls: probe output parsed by parseLoad1, and the Snapshot values
// RenderDiff turns into a model-visible block.
//
// The oracles are the package's own documented invariants, not "no panic":
//
//   - State advances only on a non-empty render. RenderDiff's doc says a
//     non-empty return updates the tracker, "so the caller must deliver the
//     rendered block to the model". If state advanced on an empty render the
//     changes it swallowed would never be reported; if it failed to advance on
//     a non-empty one, the same block would repeat every turn and the injected
//     message would stop being near-zero tokens on a quiet environment.
//   - Quiescence. Rendering the same snapshot twice must produce nothing the
//     second time, which is the whole append-only, cache-safe premise.
//   - Framing. A non-empty render is always exactly one environment_context
//     block, because it is concatenated into a prompt.
//   - parseLoad1 is total over arbitrary probe output, and never reports ok
//     with a value that is not a real number — a NaN load would silently
//     disable the load warning, since every comparison against it is false.
func FuzzEnvctxTrackerDiff(f *testing.F) {
	f.Add("/repo", "2026-08-06 14:00 PDT", "off", "main", "", "", "", false, "{ 2.16 3.57 4.34 }")
	f.Add("", "", "", "", "load pressure: 9.0 (4 cores)", "", "", true, "2.16 3.57 4.34")
	f.Add("/repo", "", "on", "", "", "memory pressure", "disk pressure", true, "")
	f.Add("", "", "", "", "", "", "", false, "NaN")
	f.Add("", "", "", "", "", "", "", false, "{}")

	f.Fuzz(func(t *testing.T, cwd, date, sandbox, branch, load, mem, disk string, hasSent bool, probe string) {
		if v, ok := parseLoad1(probe); ok {
			if math.IsNaN(v) {
				t.Fatalf("parseLoad1(%q) reported ok with NaN; every load comparison against it is false", probe)
			}
			// loadWarning must stay total for whatever parseLoad1 accepted.
			for _, cores := range []int{-1, 0, 1, 8} {
				_ = loadWarning(v, cores)
			}
		}

		cur := Snapshot{
			Cwd:           cwd,
			LocalDateHour: date,
			Sandbox:       sandbox,
			GitBranch:     branch,
			Pressure:      Pressure{Load: load, Memory: mem, Disk: disk},
		}

		tracker := NewTracker(State{HasSent: hasSent})
		before := tracker.State()
		out := tracker.RenderDiff(cur)
		after := tracker.State()

		if out == "" {
			if after != before {
				t.Fatalf("empty render advanced tracker state from %+v to %+v", before, after)
			}
			return
		}

		if !strings.HasPrefix(out, "<environment_context>\n") || !strings.HasSuffix(out, "\n</environment_context>") {
			t.Fatalf("render is not a single environment_context block: %q", out)
		}
		if strings.Count(out, "<environment_context>") != 1 || strings.Count(out, "</environment_context>") != 1 {
			t.Fatalf("render contains more than one block: %q", out)
		}
		if !after.HasSent {
			t.Fatalf("non-empty render left HasSent false: %+v", after)
		}
		if after.Last != cur {
			t.Fatalf("non-empty render stored %+v, want the snapshot it rendered %+v", after.Last, cur)
		}

		// Quiescence: the same snapshot has nothing left to say.
		if again := tracker.RenderDiff(cur); again != "" {
			t.Fatalf("re-rendering an unchanged snapshot produced %q, want nothing", again)
		}
		if tracker.State() != after {
			t.Fatalf("quiet render moved state from %+v to %+v", after, tracker.State())
		}
	})
}
