package envctx

import (
	"strings"
	"testing"
)

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
