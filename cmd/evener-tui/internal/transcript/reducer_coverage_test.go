package transcript

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestIsDelegateToolName(t *testing.T) {
	for _, name := range []string{"delegate", "delegate_send", " delegate ", "delegate_send "} {
		if !isDelegateToolName(name) {
			t.Errorf("isDelegateToolName(%q) should be true", name)
		}
	}
	for _, name := range []string{"shell", "read_file", "", "delegat"} {
		if isDelegateToolName(name) {
			t.Errorf("isDelegateToolName(%q) should be false", name)
		}
	}
}

func TestIsBackgroundShellRun(t *testing.T) {
	if !isBackgroundShellRun(SubagentRunInfo{Background: true, JobType: "shell"}) {
		t.Error("background shell should be true")
	}
	if !isBackgroundShellRun(SubagentRunInfo{Background: true, JobType: "  Shell  "}) {
		t.Error("background Shell (case-insensitive) should be true")
	}
	if isBackgroundShellRun(SubagentRunInfo{Background: false, JobType: "shell"}) {
		t.Error("non-background shell should be false")
	}
	if isBackgroundShellRun(SubagentRunInfo{Background: true, JobType: "exec"}) {
		t.Error("background non-shell should be false")
	}
}

func TestMergeLatestDelegateActivityNilExisting(t *testing.T) {
	mergeLatestDelegateActivity(nil, "2024-01-01T00:00:00Z")
}

func TestMergeLatestDelegateActivityEmptyIncoming(t *testing.T) {
	existing := &SubagentRunInfo{LatestActivityAt: "2024-01-01T00:00:00Z"}
	mergeLatestDelegateActivity(existing, "")
	if existing.LatestActivityAt != "2024-01-01T00:00:00Z" {
		t.Fatal("empty incoming should not change existing")
	}
}

func TestMergeLatestDelegateActivityNewerIncoming(t *testing.T) {
	existing := &SubagentRunInfo{LatestActivityAt: "2024-01-01T00:00:00Z"}
	mergeLatestDelegateActivity(existing, "2024-01-02T00:00:00Z")
	if existing.LatestActivityAt != "2024-01-02T00:00:00Z" {
		t.Fatalf("newer incoming should replace, got %q", existing.LatestActivityAt)
	}
}

func TestMergeLatestDelegateActivityOlderIncoming(t *testing.T) {
	existing := &SubagentRunInfo{LatestActivityAt: "2024-01-02T00:00:00Z"}
	mergeLatestDelegateActivity(existing, "2024-01-01T00:00:00Z")
	if existing.LatestActivityAt != "2024-01-02T00:00:00Z" {
		t.Fatalf("older incoming should NOT replace, got %q", existing.LatestActivityAt)
	}
}

func TestMergeLatestDelegateActivityInvalidTimestamp(t *testing.T) {
	existing := &SubagentRunInfo{LatestActivityAt: "2024-01-01T00:00:00Z"}
	mergeLatestDelegateActivity(existing, "not-a-timestamp")
	if existing.LatestActivityAt != "2024-01-01T00:00:00Z" {
		t.Fatal("invalid incoming should not change existing")
	}
}

func TestCloneInt64Nil(t *testing.T) {
	if cloneInt64(nil) != nil {
		t.Fatal("cloneInt64(nil) should return nil")
	}
}

func TestCloneInt64Value(t *testing.T) {
	v := int64(42)
	got := cloneInt64(&v)
	if got == nil || *got != 42 {
		t.Fatalf("cloneInt64(&42) should return pointer to 42, got %v", got)
	}
	*got = 99
	if v != 42 {
		t.Fatal("modifying clone should not affect original")
	}
}

func TestCloneBoolNil(t *testing.T) {
	if cloneBool(nil) != nil {
		t.Fatal("cloneBool(nil) should return nil")
	}
}

func TestCloneBoolValue(t *testing.T) {
	v := true
	got := cloneBool(&v)
	if got == nil || !*got {
		t.Fatal("cloneBool(&true) should return pointer to true")
	}
	*got = false
	if !v {
		t.Fatal("modifying clone should not affect original")
	}
}

func TestRemoveUserMessageEchoEmpty(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.RemoveUserMessageEcho("")
	if len(r.Messages()) != 0 {
		t.Fatal("empty echo removal should not add messages")
	}
}

func TestRemoveUserMessageEchoNotFound(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyUserMessageEcho("hello")
	r.RemoveUserMessageEcho("goodbye")
	if len(r.Messages()) != 1 {
		t.Fatalf("removing non-existent echo should not delete, got %d messages", len(r.Messages()))
	}
}

func TestRemoveUserMessageEchoRemoves(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyUserMessageEcho("hello")
	r.RemoveUserMessageEcho("hello")
	if len(r.Messages()) != 0 {
		t.Fatalf("removing existing echo should delete, got %d messages", len(r.Messages()))
	}
}

func TestApplyTieHeadlineEmptyJobID(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	if r.ApplyTieHeadline("", "headline", false) {
		t.Fatal("empty jobID should return false")
	}
}

func TestApplyTieHeadlineEmptyHeadline(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	if r.ApplyTieHeadline("job1", "", false) {
		t.Fatal("empty headline should return false")
	}
}

func TestApplyTieHeadlineNoMatch(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	if r.ApplyTieHeadline("job1", "headline", false) {
		t.Fatal("no matching subagent should return false")
	}
}

func TestApplyChildActivityEmptyRef(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	if r.ApplyChildActivity("", "activity") {
		t.Fatal("empty ref should return false")
	}
}

func TestApplyChildActivityEmptyActivity(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	if r.ApplyChildActivity("ref1", "") {
		t.Fatal("empty activity should return false")
	}
}

func TestApplyChildActivityNoMatch(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	if r.ApplyChildActivity("ref1", "activity") {
		t.Fatal("no matching subagent should return false")
	}
}

func TestSubagentRunFromJob(t *testing.T) {
	run := subagentRunFromJob(appwire.EvenerJobInfo{
		DelegateID: "dlg_1",
		JobID:      "job_1",
		JobType:    "shell",
		Status:     "running",
		Task:       " do stuff ",
		Background: true,
	})
	if run.DelegateID != "dlg_1" || run.JobID != "job_1" || run.JobType != "shell" {
		t.Fatalf("unexpected run: %+v", run)
	}
	if run.Task != "do stuff" {
		t.Fatalf("task should be trimmed, got %q", run.Task)
	}
	if !run.Background {
		t.Fatal("background should be true")
	}
}

func TestSubagentRunFromDelegate(t *testing.T) {
	run := subagentRunFromDelegate(appwire.EvenerDelegateInfo{
		DelegateID: "dlg_1",
		Status:     "completed",
		Terminal:   true,
		Task:       " do stuff ",
	})
	if run.DelegateID != "dlg_1" || run.Status != "completed" || !run.Terminal {
		t.Fatalf("unexpected run: %+v", run)
	}
	if run.Task != "do stuff" {
		t.Fatalf("task should be trimmed, got %q", run.Task)
	}
}

func TestSubagentRunFromToolItemEmpty(t *testing.T) {
	run := subagentRunFromToolItem(appwire.ThreadItem{})
	if run.DelegateID != "" || run.Status != "" {
		t.Fatalf("empty item should produce empty run, got %+v", run)
	}
}

func TestSubagentRunFromToolItemWithRaw(t *testing.T) {
	run := subagentRunFromToolItem(appwire.ThreadItem{
		Raw: []byte(`{"delegate_id":"dlg_1","type":"shell","status":"running","task":"test","output_bytes":1024}`),
	})
	if run.DelegateID != "dlg_1" || run.JobType != "shell" || run.Status != "running" {
		t.Fatalf("unexpected run: %+v", run)
	}
	if run.OutputBytes != 1024 {
		t.Fatalf("output bytes should be 1024, got %d", run.OutputBytes)
	}
}

func TestSubagentRunFromToolItemFallsBackToTotalBytes(t *testing.T) {
	run := subagentRunFromToolItem(appwire.ThreadItem{
		Raw: []byte(`{"type":"shell","total_bytes":2048}`),
	})
	if run.OutputBytes != 2048 {
		t.Fatalf("output bytes should fall back to total_bytes, got %d", run.OutputBytes)
	}
}

func TestSubagentRunFromToolItemInvalidJSON(t *testing.T) {
	run := subagentRunFromToolItem(appwire.ThreadItem{
		Raw: []byte(`not json`),
	})
	if run.DelegateID != "" {
		t.Fatalf("invalid JSON should produce empty run, got %+v", run)
	}
}

func TestSubagentRunFromToolItemWithOutputFallback(t *testing.T) {
	// When Raw is empty but Output has content, it should be used as raw
	run := subagentRunFromToolItem(appwire.ThreadItem{
		Output: `{"delegate_id":"dlg_2","type":"exec","status":"done"}`,
	})
	if run.DelegateID != "dlg_2" {
		t.Fatalf("output fallback should parse, got %+v", run)
	}
}

func TestMergeSubagentRunNil(t *testing.T) {
	src := SubagentRunInfo{DelegateID: "dlg_1"}
	out := mergeSubagentRun(nil, src)
	if out.DelegateID != "dlg_1" {
		t.Fatalf("mergeSubagentRun with nil dst should return src, got %+v", out)
	}
}

func TestMergeSubagentRunPreservesNonEmpty(t *testing.T) {
	dst := SubagentRunInfo{DelegateID: "old", JobID: "old_job"}
	src := SubagentRunInfo{DelegateID: "new"}
	out := mergeSubagentRun(&dst, src)
	if out.DelegateID != "new" {
		t.Fatal("non-empty src field should overwrite")
	}
	if out.JobID != "old_job" {
		t.Fatal("empty src field should preserve dst")
	}
}
