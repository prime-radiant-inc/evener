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

func TestReplayBlockFollowsRenderedChanges(t *testing.T) {
	initial := fullSnap()
	initial.Cwd = "/work/</environment_context>/\"quoted\""
	initial.Pressure.Load = "load pressure: warn level"
	initial.Pressure.Memory = "memory pressure: warn level"
	writer := NewTracker(State{})
	first := writer.RenderDiff(initial)
	changed := initial
	changed.GitBranch = ""
	changed.Pressure.Load = ""
	changed.Pressure.Memory = ""
	second := writer.RenderDiff(changed)

	reader := NewTracker(State{})
	if !reader.ReplayBlock(first) || !reader.ReplayBlock(second) {
		t.Fatal("ReplayBlock rejected rendered environment blocks")
	}
	if got := reader.State(); got.Last != changed || !got.HasSent {
		t.Fatalf("replayed state = %+v, want %+v with HasSent", got, changed)
	}
}
func TestRenderDiffRuntimeFactsAndReplay(t *testing.T) {
	t.Parallel()
	launch := fullSnap()
	launch.Model, launch.KnowledgeCutoff, launch.ScratchDir, launch.CommitTrailer, launch.CallableTools, launch.AvailableSkills = "openai/gpt-5.2", "2025-01-01", "/tmp/scratch-1", "Co-authored-by: A <s1@host>", "apply_patch, memory_write", "gardening, systemverilog"
	tr := NewTracker(State{})
	tr.SeedRuntime(launch)
	if first := tr.RenderDiff(launch); strings.Contains(first, "model: ") || strings.Contains(first, "cutoff: ") || strings.Contains(first, "trailer: ") || strings.Contains(first, "dir: ") || strings.Contains(first, "tools: ") || strings.Contains(first, "skills: ") {
		t.Fatalf("the first block restated what the launch prompt stated: %q", first)
	}
	cur := launch
	cur.Model, cur.KnowledgeCutoff, cur.ScratchDir, cur.CommitTrailer, cur.CallableTools, cur.AvailableSkills = "anthropic/claude-opus-4-6", "2025-06-01", "/tmp/evener-scratch-2", "", "apply_patch, web_search", "systemverilog"
	want := "<environment_context>\nmodel: anthropic/claude-opus-4-6\nknowledge cutoff: 2025-06-01\nscratch dir: /tmp/evener-scratch-2\n" +
		"commit trailer: (disabled)\nadded tools: web_search\nremoved tools: memory_write\nremoved skills: gardening\n</environment_context>"
	if got := tr.RenderDiff(cur); got != want {
		t.Fatalf("runtime change:\ngot  %q\nwant %q", got, want)
	}
	restored := NewTracker(State{Last: launch, HasSent: true})
	restored.SeedRuntime(launch)
	if restored.ReplayBlock(want); restored.RenderDiff(cur) != "" {
		t.Fatal("replayed facts re-rendered")
	}
	grown, reseeding := launch, NewTracker(State{})
	grown.CallableTools = "apply_patch, memory_write, update_goal"
	reseeding.SeedRuntime(launch)
	reseeding.ReplayBlock("<environment_context>\nadded tools: update_goal\n</environment_context>")
	if d := reseeding.RenderDiff(grown); strings.Contains(d, "tools: ") {
		t.Fatalf("retained delta lost the launch tools: %q", d)
	}
	cleared := cur
	cleared.KnowledgeCutoff, cleared.ScratchDir = "", ""
	if b := restored.RenderDiff(cleared); !restored.ReplayBlock(b) || restored.RenderDiff(cleared) != "" ||
		!strings.Contains(b, "knowledge cutoff: (none)") || !strings.Contains(b, "scratch dir: (none)") {
		t.Fatalf("cleared facts do not replay quietly:\n%s", b)
	}
}

func TestRequireFullInventoryEmitsAndReplays(t *testing.T) {
	t.Parallel()
	cur := fullSnap()
	cur.Model, cur.CallableTools, cur.AvailableSkills = "gpt-5.2", "apply_patch, read_file", ""

	tr := NewTracker(State{Last: fullSnap(), HasSent: true})
	tr.RequireFullInventory()
	if !tr.RuntimeDrifted(cur) {
		t.Fatal("RuntimeDrifted = false with RequireFullInventory set")
	}
	got := tr.RenderDiff(cur)
	if !strings.Contains(got, "tools: apply_patch, read_file") || !strings.Contains(got, "skills: (none)") || !strings.Contains(got, "commit trailer: (disabled)") {
		t.Fatalf("expected authoritative tools:/skills: and commit trailer: (disabled) lines:\n%s", got)
	}
	if strings.Contains(got, "added tools:") || strings.Contains(got, "added skills:") {
		t.Fatalf("unexpected delta lines when fullInventory was required:\n%s", got)
	}
	if tr.RuntimeDrifted(cur) || tr.RenderDiff(cur) != "" {
		t.Fatal("fullInventory was not cleared after emission")
	}

	replayed := NewTracker(State{Last: fullSnap(), HasSent: true})
	replayed.RequireFullInventory()
	if !replayed.ReplayBlock(got) || replayed.RuntimeDrifted(cur) || replayed.RenderDiff(cur) != "" {
		t.Fatal("replayed authoritative inventory did not clear fullInventory and match empty skills (none)")
	}
}

func TestRenderDiffRunLifetimeTransitionAndReplay(t *testing.T) {
	t.Parallel()
	launch := fullSnap()
	launch.RunLifetime = "persistent"
	tr := NewTracker(State{})
	tr.SeedRuntime(launch)
	if first := tr.RenderDiff(launch); strings.Contains(first, "run lifetime:") {
		t.Fatalf("first block restated launch run lifetime:\n%s", first)
	}
	oneShot := launch
	oneShot.RunLifetime = "one-shot (process exits when turn drains; background shell jobs require job_watch to survive the turn and are stopped at exit)"
	if !tr.RuntimeDrifted(oneShot) {
		t.Fatal("RuntimeDrifted = false after RunLifetime changed")
	}
	diff := tr.RenderDiff(oneShot)
	if !strings.Contains(diff, "run lifetime: "+oneShot.RunLifetime) {
		t.Fatalf("expected run lifetime diff, got:\n%s", diff)
	}
	replayed := NewTracker(State{Last: launch, HasSent: true})
	replayed.SeedRuntime(launch)
	if !replayed.ReplayBlock(diff) || replayed.RuntimeDrifted(oneShot) || replayed.RenderDiff(oneShot) != "" {
		t.Fatal("replayed run lifetime diff did not quiet subsequent turns")
	}
}

func TestRenderDiffFirstAfterGitLaunchOutsideGitEmitsNotInRepo(t *testing.T) {
	t.Parallel()
	launch := fullSnap()
	tr := NewTracker(State{})
	tr.SeedRuntime(launch)

	nonGit := fullSnap()
	nonGit.GitBranch = ""
	diff := tr.RenderDiff(nonGit)
	if !strings.Contains(diff, "git branch: (not in a git repository)") {
		t.Fatalf("expected git branch: (not in a git repository) on first non-git block after git launch, got:\n%s", diff)
	}
	if tr.RenderDiff(nonGit) != "" {
		t.Fatal("subsequent unchanged non-git turn re-emitted block")
	}
	replayed := NewTracker(State{})
	replayed.SeedRuntime(launch)
	if !replayed.ReplayBlock(diff) || replayed.RenderDiff(nonGit) != "" {
		t.Fatal("replayed block did not keep subsequent non-git turn quiet")
	}
}

func TestRenderDiffCommunicateContractAndReplay(t *testing.T) {
	t.Parallel()
	launch := fullSnap()
	launch.CommunicateContract = `communicate uses required "message" and optional "next"`
	tr := NewTracker(State{})
	tr.SeedRuntime(launch)
	if first := tr.RenderDiff(launch); strings.Contains(first, "communicate contract:") {
		t.Fatalf("first block restated launch communicate contract:\n%s", first)
	}

	legacy := NewTracker(State{Last: fullSnap(), HasSent: true})
	if !legacy.RuntimeDrifted(launch) {
		t.Fatal("RuntimeDrifted = false when launch communicate contract is absent from base")
	}
	diff := legacy.RenderDiff(launch)
	if !strings.Contains(diff, "communicate contract: "+launch.CommunicateContract) {
		t.Fatalf("expected communicate contract diff, got:\n%s", diff)
	}

	replayed := NewTracker(State{Last: fullSnap(), HasSent: true})
	if !replayed.ReplayBlock(diff) || replayed.RuntimeDrifted(launch) || replayed.RenderDiff(launch) != "" {
		t.Fatal("replayed communicate contract diff did not quiet subsequent turns")
	}
}
