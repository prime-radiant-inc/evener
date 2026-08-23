package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// ---------------------------------------------------------------------------
// stableShellNotificationExcerpt
// ---------------------------------------------------------------------------

func TestStableShellNotificationExcerpt_NilRecord(t *testing.T) {
	got := stableShellNotificationExcerpt("/some/store/jobs.jsonl", nil)
	if got.text != "" || got.complete {
		t.Fatalf("expected empty excerpt for nil record, got %#v", got)
	}
}

func TestStableShellNotificationExcerpt_NonTerminalRecord(t *testing.T) {
	rec := &jobstore.JobRecord{JobID: "j1", Status: jobstore.StatusRunning}
	got := stableShellNotificationExcerpt("/some/store/jobs.jsonl", rec)
	if got.text != "" || got.complete {
		t.Fatalf("expected empty excerpt for non-terminal record, got %#v", got)
	}
}

func TestStableShellNotificationExcerpt_OutputPathError(t *testing.T) {
	rec := &jobstore.JobRecord{
		JobID:       "j1",
		Status:      jobstore.StatusCompleted,
		OutputPath:  "/nonexistent/path/output.log",
		OutputBytes: 0,
	}
	got := stableShellNotificationExcerpt("/some/store/jobs.jsonl", rec)
	// validatedOutputStatsForRecord will fail to stat the nonexistent path.
	if got.text != "" {
		t.Fatalf("expected empty excerpt for stat error, got %#v", got)
	}
}

func TestStableShellNotificationExcerpt_OutputBytesMismatch(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "output.log")
	if err := os.WriteFile(outputPath, []byte("hello world"), 0o644); err != nil {
		t.Fatalf("write output: %v", err)
	}
	rec := &jobstore.JobRecord{
		JobID:       "j1",
		Status:      jobstore.StatusCompleted,
		OutputPath:  outputPath,
		OutputBytes: 999, // mismatch with actual file size
	}
	got := stableShellNotificationExcerpt("/some/store/jobs.jsonl", rec)
	if got.text != "" {
		t.Fatalf("expected empty excerpt for bytes mismatch, got %#v", got)
	}
}

func TestStableShellNotificationExcerpt_ValidOutput(t *testing.T) {
	dir := t.TempDir()
	content := "line one\nline two\nline three\n"
	outputPath := filepath.Join(dir, "output.log")
	if err := os.WriteFile(outputPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write output: %v", err)
	}
	rec := &jobstore.JobRecord{
		JobID:       "j1",
		Status:      jobstore.StatusCompleted,
		OutputPath:  outputPath,
		OutputBytes: int64(len(content)),
	}
	got := stableShellNotificationExcerpt("/some/store/jobs.jsonl", rec)
	if got.text == "" {
		t.Fatalf("expected non-empty excerpt, got %#v", got)
	}
	if !got.complete {
		t.Fatal("expected complete excerpt")
	}
}

func TestStableShellNotificationExcerpt_EmptyOutputFile(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "output.log")
	if err := os.WriteFile(outputPath, []byte{}, 0o644); err != nil {
		t.Fatalf("write output: %v", err)
	}
	rec := &jobstore.JobRecord{
		JobID:       "j1",
		Status:      jobstore.StatusCompleted,
		OutputPath:  outputPath,
		OutputBytes: 0,
	}
	got := stableShellNotificationExcerpt("/some/store/jobs.jsonl", rec)
	if got.text != "" {
		t.Fatalf("expected empty excerpt for empty file, got %#v", got)
	}
}

func TestStableShellNotificationExcerpt_DefaultPath(t *testing.T) {
	dir := t.TempDir()
	jobsDir := filepath.Join(dir, "jobs")
	if err := os.MkdirAll(jobsDir, 0o755); err != nil {
		t.Fatalf("mkdir jobs: %v", err)
	}
	content := "some output\n"
	outputPath := filepath.Join(jobsDir, "job-shell.log")
	if err := os.WriteFile(outputPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write output: %v", err)
	}
	// No OutputPath set: the function derives it from storePath + jobs/<JobID>.log
	storePath := filepath.Join(dir, "jobs.jsonl")
	rec := &jobstore.JobRecord{
		JobID:       "job-shell",
		Status:      jobstore.StatusCompleted,
		OutputBytes: int64(len(content)),
	}
	got := stableShellNotificationExcerpt(storePath, rec)
	if got.text == "" {
		t.Fatalf("expected non-empty excerpt from default path, got %#v", got)
	}
}

func TestStableShellNotificationExcerpt_TruncatedOutput(t *testing.T) {
	dir := t.TempDir()
	// Create output larger than terminalExcerptBytes.
	content := make([]byte, terminalExcerptBytes+100)
	for i := range content {
		content[i] = 'x'
	}
	outputPath := filepath.Join(dir, "output.log")
	if err := os.WriteFile(outputPath, content, 0o644); err != nil {
		t.Fatalf("write output: %v", err)
	}
	rec := &jobstore.JobRecord{
		JobID:       "j1",
		Status:      jobstore.StatusCompleted,
		OutputPath:  outputPath,
		OutputBytes: int64(len(content)),
	}
	got := stableShellNotificationExcerpt("/some/store/jobs.jsonl", rec)
	if got.text == "" {
		t.Fatalf("expected non-empty excerpt, got %#v", got)
	}
	if got.complete {
		t.Fatal("expected incomplete (truncated) excerpt")
	}
	if !contains(got.text, "[excerpt truncated]") {
		t.Fatalf("expected truncated marker, got %q", got.text)
	}
}

// ---------------------------------------------------------------------------
// stableShellAttentionContent
// ---------------------------------------------------------------------------

func TestStableShellAttentionContent(t *testing.T) {
	dir := t.TempDir()
	content := "completed output\n"
	outputPath := filepath.Join(dir, "output.log")
	if err := os.WriteFile(outputPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write output: %v", err)
	}
	rec := &jobstore.JobRecord{
		JobID:       "j1",
		Status:      jobstore.StatusCompleted,
		Type:        jobstore.JobShell,
		OutputPath:  outputPath,
		OutputBytes: int64(len(content)),
	}
	desc := delegatestore.Descriptor{
		ChildSessionID:  "child-sess",
		ToolNameCeiling: []string{"read_transcript"},
	}
	got := stableShellAttentionContent("/store/jobs.jsonl", desc, rec)
	if got == "" {
		t.Fatal("expected non-empty content")
	}
}

func TestStableShellAttentionContent_NoReadTranscriptCeiling(t *testing.T) {
	dir := t.TempDir()
	content := "completed output\n"
	outputPath := filepath.Join(dir, "output.log")
	if err := os.WriteFile(outputPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write output: %v", err)
	}
	rec := &jobstore.JobRecord{
		JobID:       "j1",
		Status:      jobstore.StatusCompleted,
		Type:        jobstore.JobShell,
		OutputPath:  outputPath,
		OutputBytes: int64(len(content)),
	}
	desc := delegatestore.Descriptor{
		ChildSessionID:  "child-sess",
		ToolNameCeiling: []string{"communicate"},
	}
	got := stableShellAttentionContent("/store/jobs.jsonl", desc, rec)
	if got == "" {
		t.Fatal("expected non-empty content")
	}
}

// ---------------------------------------------------------------------------
// repairStableShellAttentionForBootstrap
// ---------------------------------------------------------------------------

func TestRepairStableShellAttentionForBootstrap_NilController(t *testing.T) {
	err := repairStableShellAttentionForBootstrap(nil)
	if err == nil || !contains(err.Error(), "controller is nil") {
		t.Fatalf("expected nil controller error, got %v", err)
	}
}

func TestRepairStableShellAttentionForBootstrap_NoTargets(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	// No durable delegates, so no targets to repair.
	if err := repairStableShellAttentionForBootstrap(c); err != nil {
		t.Fatalf("expected nil for no targets, got %v", err)
	}
}

func TestRepairStableShellAttentionForBootstrap_NoStoreFile(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	// The jobs.jsonl for this delegate does not exist yet, so the target is skipped.
	if err := repairStableShellAttentionForBootstrap(c); err != nil {
		t.Fatalf("expected nil when store file absent, got %v", err)
	}
}

func TestRepairStableShellAttentionForBootstrap_DirectoryStoreFile(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	// Create a directory where jobs.jsonl should be — os.Stat succeeds but it's not a regular file.
	storePath := filepath.Join(jobsDir(c.stateDir, "child-dlg_target"), "jobs.jsonl")
	if err := os.MkdirAll(storePath, 0o755); err != nil {
		t.Fatalf("mkdir store path: %v", err)
	}
	err := repairStableShellAttentionForBootstrap(c)
	if err == nil || !contains(err.Error(), "not a regular file") {
		t.Fatalf("expected not-a-regular-file error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// collectShellRuntimeLossEvidence
// ---------------------------------------------------------------------------

func TestCollectShellRuntimeLossEvidence_Empty(t *testing.T) {
	path := seedDelegateShellStore(t, false, false)
	got, err := collectShellRuntimeLossEvidence(path)
	if err != nil {
		t.Fatalf("collectShellRuntimeLossEvidence: %v", err)
	}
	if len(got.runningJobIDs) != 0 || len(got.pendingNotification) != 0 {
		t.Fatalf("expected empty evidence, got %#v", got)
	}
}

func TestCollectShellRuntimeLossEvidence_RunningAndPending(t *testing.T) {
	path := seedDelegateShellStore(t, true, true)
	got, err := collectShellRuntimeLossEvidence(path)
	if err != nil {
		t.Fatalf("collectShellRuntimeLossEvidence: %v", err)
	}
	if len(got.runningJobIDs) != 1 || got.runningJobIDs[0] != "job-shell" {
		t.Fatalf("expected one running job 'job-shell', got %#v", got.runningJobIDs)
	}
	if len(got.pendingNotification) != 1 || got.pendingNotification[0].jobID != "job-terminal" {
		t.Fatalf("expected one pending notification 'job-terminal', got %#v", got.pendingNotification)
	}
}

// ---------------------------------------------------------------------------
// executeDelegateShellRepair error paths
// ---------------------------------------------------------------------------

func TestExecuteDelegateShellRepair_EmptyStorePath(t *testing.T) {
	plan := delegateShellRepairPlan{delegateID: "dlg_1", storePath: ""}
	err := executeDelegateShellRepair(plan, testNow())
	if err == nil || !contains(err.Error(), "store path is empty") {
		t.Fatalf("expected empty store path error, got %v", err)
	}
}

func TestExecuteDelegateShellRepair_DirectoryStorePath(t *testing.T) {
	plan := delegateShellRepairPlan{
		delegateID: "dlg_1",
		storePath:  t.TempDir(), // a directory, not a file
	}
	err := executeDelegateShellRepair(plan, testNow())
	if err == nil || !contains(err.Error(), "not a regular file") {
		t.Fatalf("expected not-a-regular-file error, got %v", err)
	}
}

func TestExecuteDelegateShellRepair_NonexistentStorePath(t *testing.T) {
	plan := delegateShellRepairPlan{
		delegateID: "dlg_1",
		storePath:  "/nonexistent/path/jobs.jsonl",
	}
	err := executeDelegateShellRepair(plan, testNow())
	if err == nil {
		t.Fatalf("expected error for nonexistent store path")
	}
}

// ---------------------------------------------------------------------------
// collectDelegateReconcileEvidence
// ---------------------------------------------------------------------------

func TestCollectDelegateReconcileEvidence_Empty(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	req := c.ReconcileRequirements()
	got, err := collectDelegateReconcileEvidence(c.stateDir, req)
	if err != nil {
		t.Fatalf("collectDelegateReconcileEvidence: %v", err)
	}
	if len(got.shells) != 0 || len(got.attention) != 0 {
		t.Fatalf("expected empty evidence, got %#v", got)
	}
}

func TestCollectDelegateReconcileEvidence_ShellStoreOnly(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	path := filepath.Join(jobsDir(c.stateDir, "child-dlg_target"), "jobs.jsonl")
	seedDelegateShellStoreAt(t, path)
	req := c.ReconcileRequirements()
	got, err := collectDelegateReconcileEvidence(c.stateDir, req)
	if err != nil {
		t.Fatalf("collectDelegateReconcileEvidence: %v", err)
	}
	if _, ok := got.shells["dlg_target"]; !ok {
		t.Fatalf("expected shell evidence for dlg_target, got %#v", got.shells)
	}
}

// testNow returns a fixed time for deterministic tests.
func testNow() time.Time {
	return time.Unix(100, 0).UTC()
}
