package envctx

import (
	"strings"
	"testing"
)

func TestParseLoad1RejectsNonFiniteProbeOutput(t *testing.T) {
	// A load average is a finite number. strconv.ParseFloat happily accepts
	// "NaN" and "Inf", and the ok bool exists precisely to say "this probe
	// output was not a load average" — reporting ok for a non-finite value
	// defeats it. Nor can it be caught downstream: every comparison against NaN
	// is false, so loadWarning's `load1 <= 2*cores` guard falls through and
	// injects a nonsense pressure line into the model's context.
	for _, probe := range []string{"NaN", "nan", "+Inf", "-Inf", "inf"} {
		if v, ok := parseLoad1(probe); ok {
			t.Errorf("parseLoad1(%q) = (%v, true), want ok=false", probe, v)
			if w := loadWarning(v, 8); w != "" {
				t.Errorf("  and it renders as %q", w)
			}
		}
	}

	// Ordinary probe output must keep working, in both supported shapes.
	for probe, want := range map[string]float64{
		"{ 2.16 3.57 4.34 }": 2.16,
		"2.16 3.57 4.34":     2.16,
		"0.00 0.00 0.00":     0,
	} {
		if v, ok := parseLoad1(probe); !ok || v != want {
			t.Errorf("parseLoad1(%q) = (%v, %v), want (%v, true)", probe, v, ok, want)
		}
	}
}

func fullSnap() Snapshot {
	return Snapshot{
		Cwd:           "/Users/jesse/work",
		LocalDateHour: "2026-08-06 14:00 PDT",
		Sandbox:       "off",
		GitBranch:     "main",
	}
}

func TestRenderDiffFirstEmissionRendersAllNonEmptyFields(t *testing.T) {
	tr := NewTracker(State{})
	got := tr.RenderDiff(fullSnap())
	want := "<environment_context>\n" +
		"cwd: \"/Users/jesse/work\"\n" +
		"date: 2026-08-06 14:00 PDT\n" +
		"sandbox: off\n" +
		"git branch: main\n" +
		"</environment_context>"
	if got != want {
		t.Fatalf("first emission:\ngot  %q\nwant %q", got, want)
	}
	if st := tr.State(); !st.HasSent || st.Last != fullSnap() {
		t.Fatalf("state after emission: %+v", st)
	}
}

func TestRenderDiffNoChangeRendersNothing(t *testing.T) {
	tr := NewTracker(State{Last: fullSnap(), HasSent: true})
	if got := tr.RenderDiff(fullSnap()); got != "" {
		t.Fatalf("unchanged snapshot rendered %q", got)
	}
}

func TestRenderDiffSingleFieldChange(t *testing.T) {
	tr := NewTracker(State{Last: fullSnap(), HasSent: true})
	cur := fullSnap()
	cur.Cwd = "/Users/jesse/work/.worktrees/lane"
	got := tr.RenderDiff(cur)
	want := "<environment_context>\n" +
		"cwd: \"/Users/jesse/work/.worktrees/lane\"\n" +
		"</environment_context>"
	if got != want {
		t.Fatalf("cwd change:\ngot  %q\nwant %q", got, want)
	}
}

func TestRenderDiffGitBranchGoneRendersPlaceholder(t *testing.T) {
	tr := NewTracker(State{Last: fullSnap(), HasSent: true})
	cur := fullSnap()
	cur.GitBranch = ""
	got := tr.RenderDiff(cur)
	if !strings.Contains(got, "git branch: (not in a git repository)") {
		t.Fatalf("branch->empty must render placeholder, got %q", got)
	}
}

func TestRenderDiffPressureAppearAndClear(t *testing.T) {
	tr := NewTracker(State{Last: fullSnap(), HasSent: true})

	over := fullSnap()
	over.Pressure.Memory = "memory pressure: warn level"
	got := tr.RenderDiff(over)
	if !strings.Contains(got, "memory pressure: warn level") {
		t.Fatalf("pressure onset not rendered: %q", got)
	}

	// Clearing renders the back-to-normal line exactly once.
	got = tr.RenderDiff(fullSnap())
	if !strings.Contains(got, "memory pressure: back to normal") {
		t.Fatalf("pressure clear not rendered: %q", got)
	}
	if got := tr.RenderDiff(fullSnap()); got != "" {
		t.Fatalf("steady nominal must render nothing, got %q", got)
	}
}

func TestRenderDiffFirstEmissionSkipsNominalPressure(t *testing.T) {
	tr := NewTracker(State{})
	got := tr.RenderDiff(fullSnap())
	if strings.Contains(got, "pressure") {
		t.Fatalf("nominal pressure must not appear on first emission: %q", got)
	}
}
