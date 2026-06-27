package msgrender

import (
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
)

func TestDiffBodyTintsAddLines(t *testing.T) {
	withTestColorProfile(t)
	diff := strings.Join([]string{
		"@@ -1,3 +1,3 @@",
		" context line",
		"-removed",
		"+added",
	}, "\n")
	got := diffBody(ToolArgs{}, diff, 60)
	// Each + line should carry a background tint; we can detect via ANSI bg escape.
	// Lipgloss may combine fg+bg in one sequence (e.g. \x1b[38;2;...;48;2;...m)
	// or emit separate \x1b[48;...m sequences.
	hasBg := strings.Contains(got, "\x1b[48") ||
		strings.Contains(got, ";48;") ||
		strings.Contains(got, ";48m")
	if !hasBg {
		t.Errorf("diffBody should set background on +/− lines: %q", got)
	}
}

func TestDiffBodyHandlesEmptyInput(t *testing.T) {
	got := diffBody(ToolArgs{}, "", 60)
	if got != "" {
		t.Errorf("diffBody on empty input should be empty; got %q", got)
	}
}

func TestFileBodyShowsFirstLines(t *testing.T) {
	lines := []string{}
	for i := 1; i <= 20; i++ {
		lines = append(lines, "line"+strconv.Itoa(i))
	}
	args := ToolArgs{"file_path": "x.txt"}
	got := fileBody(args, strings.Join(lines, "\n"), 60)
	if !strings.Contains(got, "line1") {
		t.Errorf("fileBody should contain first lines: %q", got)
	}
	if !strings.Contains(got, "show 15 more lines") && !strings.Contains(got, "more lines") {
		t.Errorf("fileBody should show truncation hint: %q", got)
	}
}

func TestTaskListBodyRendersPerTaskRows(t *testing.T) {
	// task_list output is JSON-shaped: array of {name, status}.
	output := `[
		{"name":"Understand task","status":"done"},
		{"name":"Do the work","status":"in_progress"},
		{"name":"Verify","status":"pending"}
	]`
	got := taskListBody(ToolArgs{}, output, 60)
	for _, want := range []string{"Understand task", "Do the work", "Verify", "[✓]", "[ ]"} {
		if !strings.Contains(got, want) {
			t.Errorf("taskListBody missing %q in: %q", want, got)
		}
	}
}

func TestDelegateBodyShowsSummaryWhenChildUnavailable(t *testing.T) {
	args := ToolArgs{"job_id": "job_01NONEXISTENT", "status": "completed"}
	got := delegateBody(args, "", 60)
	if !strings.Contains(got, "completed") || strings.Contains(got, "turns") {
		t.Errorf("delegateBody should show status without obsolete turn count: %q", got)
	}
}

func TestDelegateBodyHandlesNarrowWidth(t *testing.T) {
	args := ToolArgs{"job_id": "job_01ABCD"}
	got := delegateBody(args, "", 10)
	if strings.Contains(got, "panic") {
		t.Errorf("delegateBody should not panic at narrow width")
	}
}

func TestShellBodyHighlightsOutput(t *testing.T) {
	got := ShellBody(ToolArgs{"command": "ls"}, "file1.go\nfile2.go\nfile3.go", 60)
	if got == "" {
		t.Errorf("ShellBody should return non-empty for non-empty output")
	}
}

func TestWebSearchBodyFormatsResults(t *testing.T) {
	output := strings.Join([]string{
		"Result 1 title — https://a.com",
		"Result 2 title — https://b.com",
	}, "\n")
	got := webSearchBody(ToolArgs{}, output, 60)
	if !strings.Contains(got, "Result 1") || !strings.Contains(got, "Result 2") {
		t.Errorf("webSearchBody should include results: %q", got)
	}
}

func TestRenderSubagentRailConsolidates(t *testing.T) {
	withTestColorProfile(t)
	runs := []transcript.SubagentRunInfo{
		{JobID: "j1", Task: "port webhook verification", Status: "running"},
		{JobID: "j2", Task: "trace retry callers", Status: "running"},
		{JobID: "j3", Task: "update docs", Status: "completed"},
		{JobID: "j4", Task: "audit deps", Status: "failed", Reason: "2 high CVEs"},
	}
	out := RenderSubagentRail(runs, 80)
	// Header tallies the workers.
	for _, want := range []string{"Subagents", "2 running", "1 done", "1 failed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("header missing %q: %q", want, out)
		}
	}
	// Running entries are listed; the failure surfaces with its reason.
	for _, want := range []string{"port webhook verification", "trace retry callers", "audit deps", "2 high CVEs"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %q", want, out)
		}
	}
	// The settled pile folds to a count — the done entry's name is NOT listed.
	if strings.Contains(out, "update docs") {
		t.Fatalf("done should fold to a count, not list 'update docs': %q", out)
	}
	if !strings.Contains(out, "✓ 1 done") {
		t.Fatalf("done count missing: %q", out)
	}
}

func TestRenderSubagentRailNamesBackgroundShellByCommand(t *testing.T) {
	withTestColorProfile(t)
	out := RenderSubagentRail([]transcript.SubagentRunInfo{
		{JobID: "jbg", JobType: "shell", Background: true, Command: "go test ./... -count=1", Status: "running"},
	}, 80)
	if !strings.Contains(out, "go test ./... -count=1") {
		t.Fatalf("a background shell should be named by its command: %q", out)
	}
}

func TestRenderSubagentRailShowsLiveActivity(t *testing.T) {
	withTestColorProfile(t)
	out := RenderSubagentRail([]transcript.SubagentRunInfo{
		{JobID: "j1", Task: "port webhook", Status: "running", Activity: "shell: go test ./...", Steps: 3},
	}, 80)
	if !strings.Contains(out, "shell: go test ./...") || !strings.Contains(out, "· 3") {
		t.Fatalf("running row should show the live activity + step count: %q", out)
	}
}

func TestRenderSubagentRailKeepsTiedDoneRowVisible(t *testing.T) {
	withTestColorProfile(t)
	out := RenderSubagentRail([]transcript.SubagentRunInfo{
		{JobID: "j1", Task: "port webhook", Status: "completed", Headline: "go test passed · 4ad69c0"},
		{JobID: "j2", Task: "trace callers", Status: "completed"},
	}, 80)
	if !strings.Contains(out, "port webhook") || !strings.Contains(out, "go test passed · 4ad69c0") {
		t.Fatalf("a tied done row should stay visible with its headline: %q", out)
	}
	if strings.Contains(out, "trace callers") {
		t.Fatalf("a plain done row should fold, not list 'trace callers': %q", out)
	}
	if !strings.Contains(out, "✓ 2 done") {
		t.Fatalf("tally should count both done: %q", out)
	}
}
