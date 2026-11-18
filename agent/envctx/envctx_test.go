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

func TestRenderDiffWorkspaceChangeReportsCheckout(t *testing.T) {
	tr := NewTracker(State{Last: fullSnap(), HasSent: true})
	cur := fullSnap()
	cur.Cwd = "/Users/jesse/work/.worktrees/lane"
	var delta Tracker
	if !delta.ReplayBlock(tr.RenderDiff(cur)) || delta.State().Last != (Snapshot{Cwd: cur.Cwd, GitBranch: cur.GitBranch}) {
		t.Fatalf("workspace update = %+v", delta.State())
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

// TestRenderDiffInconclusiveProbeKeepsTheSavedBranchForTheSameCheckout pins the
// three answers a Git probe can give about a checkout, because treating two of
// them alike is what loses the model's last known branch:
//
//   - inconclusive for the checkout the saved prompt named: keep that branch,
//     say nothing about it, and leave the next observation owed;
//   - inconclusive for a checkout that moved: report uncertainty, because the
//     saved branch describes somewhere the session is no longer standing;
//   - confirmed outside a worktree: clear it, because the probe answered.
func TestRenderDiffInconclusiveProbeKeepsTheSavedBranchForTheSameCheckout(t *testing.T) {
	saved := fullSnap()
	unconfirmed := func(s Snapshot) Snapshot {
		s.gitBranchUnknown = true
		return s
	}

	t.Run("inconclusive for the same checkout keeps the saved branch", func(t *testing.T) {
		// A legacy transcript names the branch in its header but recorded no
		// launch fact, so the tracker starts owing an observation of that same
		// directory and holds no branch of its own to compare against.
		tr := NewTracker(State{Last: Snapshot{Cwd: saved.Cwd}})
		got := tr.RenderDiff(unconfirmed(saved))
		// What the model ends up believing is the thing that matters: it read
		// the saved prompt, and any block this session emits is applied on top.
		model := NewTracker(State{Last: saved})
		model.ReplayBlock(got)
		if strings.Contains(got, "git branch:") {
			t.Errorf("an inconclusive probe for the saved checkout must not supersede its branch, got %q", got)
		}
		if branch := model.State().Last.GitBranch; branch != saved.GitBranch {
			t.Errorf("the model's branch became %q after an inconclusive probe, want the saved %q", branch, saved.GitBranch)
		}
	})

	t.Run("inconclusive for a different checkout reports uncertainty", func(t *testing.T) {
		tr := NewTracker(State{Last: saved})
		next := saved
		next.Cwd = "/Users/jesse/work/.worktrees/lane"
		var delta Tracker
		if !delta.ReplayBlock(tr.RenderDiff(unconfirmed(next))) {
			t.Fatal("a moved checkout with no confirmed branch must still report")
		}
		if branch := delta.State().Last.GitBranch; branch != "" || !delta.State().GitBranchUnknown {
			t.Errorf("moved-and-inconclusive reported branch %q unknown %v, want unknown", branch, delta.State().GitBranchUnknown)
		}
	})

	t.Run("confirmed outside clears the saved branch", func(t *testing.T) {
		tr := NewTracker(State{Last: saved})
		outside := saved
		outside.GitBranch = ""
		got := tr.RenderDiff(outside)
		if !strings.Contains(got, "git branch: (not in a git repository)") {
			t.Fatalf("a confirmed absence must clear the saved branch explicitly, got %q", got)
		}
		if branch := tr.State().Last.GitBranch; branch != "" {
			t.Errorf("tracker kept branch %q after a confirmed absence, want cleared", branch)
		}
	})
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

func TestReplayBlockFollowsRenderedChanges(t *testing.T) {
	initial := fullSnap()
	initial.Cwd = "/work/</environment_context>/\"quoted\""
	initial.ScratchDir = "/tmp/parent's \"scratch\""
	initial.WritableRoots = "/work/\"project\"\nline two"
	initial.FileToolWrites = FileWritesSandbox
	initial.ShellNetwork = NetworkBlocked
	initial.DetachedShell = DetachUnavailable
	initial.Pressure.Load = "load pressure: warn level"
	initial.Pressure.Memory = "memory pressure: warn level"
	initial.Pressure.Disk = "disk pressure: 5% free"
	initial.SessionID = "child-a1"
	initial.Model = "provider/m1"
	initial.ExecutionMode = OneShotMode
	initial.AskUserState = QuestionsAvailable
	initial.ResultEndReason = EndReasonAvailable
	initial.ResultToolName = "report"
	initial.KnowledgeCutoff = "2025-01"
	initial.CommitTrailer = "Co-authored-by: Evener v1 (provider/m1) <child-a1@host>"
	writer := NewTracker(State{})
	first := writer.RenderDiff(initial)
	changed := initial
	changed.GitBranch = ""
	changed.ScratchDir = "/tmp/child"
	changed.WritableRoots = ""
	changed.FileToolWrites = FileWritesHost
	changed.ShellNetwork = NetworkAllowed
	changed.DetachedShell = DetachAvailable
	changed.Pressure = Pressure{}
	changed.Model = "provider/m2"
	changed.ExecutionMode = DaemonMode
	changed.AskUserState = QuestionsUnavailable
	changed.ResultEndReason = OmitEndReason
	changed.ResultToolName = "submit_result"
	changed.KnowledgeCutoff = ""
	changed.CommitTrailer = ""
	changed.OmitCommitTrailer = true
	second := writer.RenderDiff(changed)

	reader := NewTracker(State{})
	if !reader.ReplayBlock(first) || reader.State().Last != initial {
		t.Fatalf("initial environment did not round-trip: %+v", reader.State())
	}
	if !reader.ReplayBlock(second) {
		t.Fatal("ReplayBlock rejected the changed environment")
	}
	if got := reader.State(); got.Last != changed || !got.HasSent {
		t.Fatalf("replayed state = %+v, want %+v with HasSent", got, changed)
	}
	if reader.RenderDiff(changed) != "" {
		t.Fatal("unchanged environment was repeated after restore")
	}

	t.Run("fold retains only changed fields", func(t *testing.T) {
		nominal := fullSnap()
		nominal.GitBranch = ""
		observed := nominal
		observed.GitBranch = initial.GitBranch
		observed.Pressure = initial.Pressure
		observed.ScratchDir = changed.ScratchDir
		observed.FileToolWrites = FileWritesScratch
		observed.AskUserState = changed.AskUserState
		previous := NewTracker(State{Last: nominal, HasSent: true})
		retained := previous.RenderDiff(observed)
		var projection Tracker
		if !projection.ReplayBlock(retained) || projection.State().HasSent {
			t.Fatal("expected a retained partial environment")
		}
		nominal.ScratchDir = observed.ScratchDir
		nominal.FileToolWrites = observed.FileToolWrites
		nominal.AskUserState = observed.AskUserState
		refill := NewTracker(projection.State()).RenderDiff(nominal)
		var refilled Tracker
		if !refilled.ReplayBlock(refill) || refilled.State().Last.ScratchDir != "" || refilled.State().Last.FileToolWrites != "" || refilled.State().Last.AskUserState != "" {
			t.Fatal("refilling the environment repeated a retained update")
		}
		if !projection.ReplayBlock(refill) || projection.State() != (State{Last: nominal, HasSent: true}) {
			t.Fatalf("model projection after refill = %+v, want %+v", projection.State(), nominal)
		}
	})

	t.Run("unrecorded launch branch survives an inconclusive probe", func(t *testing.T) {
		observed := fullSnap()
		writer := NewTracker(State{Last: Snapshot{Cwd: observed.Cwd}, GitBranchUnknown: true})
		reader := NewTracker(writer.State())
		model := NewTracker(State{Last: Snapshot{Cwd: observed.Cwd, GitBranch: "launch"}})
		observed.GitBranch, observed.gitBranchUnknown = "", true
		block := writer.RenderDiff(observed)
		if !reader.ReplayBlock(block) || !reader.State().GitBranchUnknown || !model.ReplayBlock(block) || model.State().Last.GitBranch != "launch" {
			t.Fatal("an unrelated observation cleared the saved prompt's unknown branch")
		}
		observed.gitBranchUnknown = false
		update := reader.RenderDiff(observed)
		if !model.ReplayBlock(update) || model.State().Last.GitBranch != "" || !writer.ReplayBlock(update) || writer.State().GitBranchUnknown {
			t.Fatal("the confirmed absence did not clear the branch for both the model and the restored tracker")
		}
		if repeat := writer.RenderDiff(observed); repeat != "" {
			t.Fatal("an unchanged checkout repeated its correction")
		}
	})

	t.Run("branch-only record retained without its checkout", func(t *testing.T) {
		cur := fullSnap()
		cur.GitBranch, cur.gitBranchUnknown = "", true
		model := NewTracker(State{Last: Snapshot{Cwd: cur.Cwd}, GitBranchUnknown: true})
		// A saved branch-only block can outlive the turn that named its checkout.
		model.ReplayBlock("<environment_context>\ngit branch: linked-branch\n</environment_context>")
		writer := NewTracker(model.State())
		if !model.ReplayBlock(writer.RenderDiff(cur)) || model.State().Last.GitBranch != "" || !model.State().GitBranchUnknown || model.State() != writer.State() {
			t.Fatalf("the retained branch escaped its checkout: model %+v, writer %+v", model.State(), writer.State())
		}
		if block := writer.RenderDiff(cur); block != "" {
			t.Error("a second inconclusive probe repeated the correction")
		}
	})

	t.Run("branch observation follows its checkout", func(t *testing.T) {
		for _, inconclusive := range []bool{false, true} {
			start := fullSnap()
			writer := NewTracker(State{Last: start, HasSent: true})
			next := start
			next.Cwd += "/other"
			next.gitBranchUnknown = inconclusive
			first := writer.RenderDiff(next)
			projection := NewTracker(State{Last: start, HasSent: true})
			if !projection.ReplayBlock(first) || projection.State().Last.Cwd != next.Cwd || projection.State().GitBranchUnknown != inconclusive {
				t.Errorf("checkout changed with inconclusive=%t, model state %+v", inconclusive, projection.State())
			}
			next.gitBranchUnknown = false
			second := writer.RenderDiff(next)
			if second != "" {
				projection.ReplayBlock(second)
			}
			reported := NewTracker(State{GitBranchUnknown: true})
			reported.ReplayBlock(first)
			reported.ReplayBlock(second)
			if st := reported.State(); st.Last.Cwd != next.Cwd || st.GitBranchUnknown || st.Last.GitBranch != next.GitBranch || projection.State() != writer.State() {
				t.Errorf("the durable updates did not associate branch %q with checkout %q", next.GitBranch, next.Cwd)
			}
		}
	})
}
