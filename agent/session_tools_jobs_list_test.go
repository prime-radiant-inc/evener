package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	toolpkg "primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

func jobListState(t *testing.T, s *Session, args map[string]any) jobListResult {
	t.Helper()
	value, err := jobListTool(s, args, 1<<20)
	if err != nil {
		t.Fatalf("jobListTool: %v", err)
	}
	stateResult, ok := value.(toolpkg.StateResult)
	if !ok {
		t.Fatalf("jobListTool result = %T, want StateResult", value)
	}
	result, ok := stateResult.State.(jobListResult)
	if !ok {
		t.Fatalf("jobListTool state = %T, want jobListResult", stateResult.State)
	}
	return result
}

func TestJobListIncludesDelegateWithoutActivationAlias(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	started := time.Unix(100, 0).UTC()
	seedStableToolDelegate(t, s, "dlg_listed", "", started, started.Add(time.Second))

	result := jobListState(t, s, nil)
	if len(result.Items) != 1 {
		t.Fatalf("job_list items = %#v, want one stable delegate", result.Items)
	}
	item := result.Items[0]
	if item.ID != "dlg_listed" || item.Kind != delegateResourceType || item.Type != delegateResourceType {
		t.Fatalf("stable delegate row = %#v", item)
	}
	if item.JobID != "" || item.ParentJobID != nil {
		t.Fatalf("stable delegate row exposes activation aliases: %#v", item)
	}
}

func TestJobListRowIsLean(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	record, err := s.jobManager.createShell(createShellOpts{Command: "sleep 30", Description: "lean shell"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, s.jobManager, record.JobID) })

	result := jobListState(t, s, nil)
	if len(result.Items) != 1 {
		t.Fatalf("job_list items = %#v, want one shell", result.Items)
	}
	item := result.Items[0]
	if item.ID != record.JobID || item.Type != string(jobstore.JobShell) || item.TranscriptRef == nil || *item.TranscriptRef != shellTranscriptRef(record.JobID) || item.Resumable != nil {
		t.Fatalf("lean shell row = %#v", item)
	}
	rendered := formatJobList(result, jobToolResultDefaultMaxChar)
	for _, banned := range []string{"visible_to_session_id", "null", "{"} {
		if strings.Contains(rendered, banned) {
			t.Fatalf("lean job_list output contains %q:\n%s", banned, rendered)
		}
	}
	if !strings.Contains(rendered, "lean shell") || !strings.Contains(rendered, "bytes") {
		t.Fatalf("shell row lacks label or output size:\n%s", rendered)
	}
	if !strings.Contains(rendered, "transcript_ref "+shellTranscriptRef(record.JobID)) {
		t.Fatalf("shell row lacks transcript_ref:\n%s", rendered)
	}
}

func TestJobListDefaultListingOmitsDepth(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	record, err := s.jobManager.createShell(createShellOpts{Command: "sleep 1", Description: "job"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, s.jobManager, record.JobID) })
	value, err := jobListTool(s, nil, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(handlerJSON(t, value)), `"depth"`) {
		t.Fatalf("default job_list output contains depth: %s", handlerJSON(t, value))
	}
}

func TestJobListWatchesOmittedWhenNoneConfigured(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	result := jobListState(t, s, nil)
	if len(result.Watches) != 0 || len(result.RecentWatches) != 0 {
		t.Fatalf("idle job_list supervision = watches %#v recent %#v", result.Watches, result.RecentWatches)
	}
}

func TestJobListEnumeratesActiveWatch(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	record, err := s.jobManager.createShell(createShellOpts{Command: "sleep 30", Description: "watched shell"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, s.jobManager, record.JobID) })
	if _, err := s.jobManager.configureWatch(watchArgs{Target: record.JobID, OutputMatch: "ready"}); err != nil {
		t.Fatal(err)
	}
	result := jobListState(t, s, nil)
	if len(result.Watches) != 1 || result.Watches[0].Source != record.JobID || result.Watches[0].Condition != "output_match: ready" {
		t.Fatalf("active watches = %#v", result.Watches)
	}
	if _, err := time.Parse(time.RFC3339Nano, result.Watches[0].CreatedAt); err != nil {
		t.Fatalf("watch created_at = %q: %v", result.Watches[0].CreatedAt, err)
	}
}

func TestJobListWatchReflectsDeliveries(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.jobManager.enqueue = func(jobNotification) {}
	installWatchBelowValidation(t, s.jobManager, watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}})
	for range 3 {
		onSessionEventKD(s.jobManager, events.EventCommunicate, nil)
	}
	result := jobListState(t, s, nil)
	if len(result.Watches) != 1 || result.Watches[0].Deliveries != 3 {
		t.Fatalf("watch deliveries = %#v, want 3", result.Watches)
	}
}

func TestJobListReportsDelegationAllowance(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.delegationAllowance = 2
	if result := jobListState(t, s, nil); result.DelegationAllowance != 2 {
		t.Fatalf("delegation_allowance = %d, want 2", result.DelegationAllowance)
	}
}

func TestJobListSurfacesRecentWatches(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	record, err := s.jobManager.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, s.jobManager, record.JobID) })
	if _, err := s.jobManager.configureWatch(watchArgs{Target: record.JobID, OutputMatch: "ready"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.jobManager.configureWatch(watchArgs{Target: record.JobID, Clear: true}); err != nil {
		t.Fatal(err)
	}
	result := jobListState(t, s, nil)
	if len(result.RecentWatches) != 1 || result.RecentWatches[0].Source != record.JobID || result.RecentWatches[0].EndReason != "cleared" {
		t.Fatalf("recent watches = %#v", result.RecentWatches)
	}
}

func seedShellJobRecords(t *testing.T, s *Session, count int) {
	t.Helper()
	base := time.Unix(1_700_000_000, 0).UTC()
	for i := range count {
		started := base.Add(time.Duration(i) * time.Second)
		jobID := fmt.Sprintf("job_seed_%03d", i)
		if err := s.jobManager.appendJobEvents([]jobstore.Event{
			{Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell, OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: &started},
			{Kind: jobstore.EventJobFinished, TS: started, JobID: jobID, Status: jobstore.StatusCompleted},
		}); err != nil {
			t.Fatalf("seed job %d: %v", i, err)
		}
	}
}

func callJobListText(t *testing.T, s *Session, argsJSON string) string {
	t.Helper()
	call := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{ID: "list", Name: "job_list", Arguments: json.RawMessage(argsJSON)})
	if call.IsError {
		t.Fatalf("job_list returned error: %s", call.Output)
	}
	return call.Output
}

func TestJobListOffsetWindowing(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	seedShellJobRecords(t, s, 120)
	for args, want := range map[string]string{
		`{"limit":50,"offset":50}`:  "showing 51-100 of 120 jobs.",
		`{"limit":50,"offset":100}`: "showing 101-120 of 120 jobs.",
		`{"limit":100}`:             "showing 1-100 of 120 jobs.",
		`{"limit":50,"offset":150}`: "showing none of 120 jobs (offset 150 past end).",
	} {
		if output := callJobListText(t, s, args); !strings.Contains(output, want) {
			t.Fatalf("job_list %s lacks %q:\n%s", args, want, output)
		}
	}
	call := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{ID: "list", Name: "job_list", Arguments: json.RawMessage(`{"offset":-1}`)})
	if !call.IsError {
		t.Fatal("negative offset accepted")
	}
}

func TestJobListShowsTurnSlotOccupancy(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.treeCounter = newTreeCounter(50)
	s.driveCounter = newTreeCounter(defaultMaxConcurrentDriveTurns)
	if !s.treeCounter.reserve(slotKindJob) || !s.driveCounter.reserve(slotKindDrive) {
		t.Fatal("setup reserves failed")
	}
	output := callJobListText(t, s, `{}`)
	if !strings.Contains(output, "delegate turn slots: 1/50 in use (1 jobs, 1 drive turns).") {
		t.Fatalf("occupancy line missing: %q", output)
	}
}

func TestJobListDelegateByteCountsAndPhase(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	started := time.Unix(100, 0).UTC()
	ended := started.Add(time.Second)

	seedStableToolRunningDelegate(t, s, "dlg_run", "", started)
	seedStableToolDelegate(t, s, "dlg_done", "", started, ended)

	result := jobListState(t, s, nil)
	if len(result.Items) != 2 {
		t.Fatalf("job_list items = %d, want 2", len(result.Items))
	}

	var runItem, doneItem *jobListEntry
	for i := range result.Items {
		switch result.Items[i].ID {
		case "dlg_run":
			runItem = &result.Items[i]
		case "dlg_done":
			doneItem = &result.Items[i]
		}
	}
	if runItem == nil || doneItem == nil {
		t.Fatalf("missing expected delegates: %#v", result.Items)
	}
	if runItem.Phase != "running" {
		t.Errorf("running delegate phase = %q, want running", runItem.Phase)
	}
	if doneItem.Phase != "idle" {
		t.Errorf("finished delegate phase = %q, want idle", doneItem.Phase)
	}

	output := callJobListText(t, s, `{}`)
	for line := range strings.SplitSeq(output, "\n") {
		if strings.HasPrefix(line, "dlg_run") {
			if strings.Contains(line, "bytes") {
				t.Errorf("running delegate must omit bytes token entirely: %q", line)
			}
			if !strings.Contains(line, "phase running") {
				t.Errorf("running delegate lacks phase: %q", line)
			}
			if !strings.Contains(line, "running_for_ms") || !strings.Contains(line, "quiet_for_ms") {
				t.Errorf("running delegate lacks running/quiet clocks: %q", line)
			}
			if !strings.Contains(line, "transcript_ref local:child-dlg_run") {
				t.Errorf("running delegate lacks transcript_ref: %q", line)
			}
		}
		if strings.HasPrefix(line, "dlg_done") {
			if strings.Contains(line, "bytes") {
				t.Errorf("finished delegate must omit bytes token entirely: %q", line)
			}
			if !strings.Contains(line, "phase idle") {
				t.Errorf("finished delegate lacks phase: %q", line)
			}
			if !strings.Contains(line, "transcript_ref local:child-dlg_done") {
				t.Errorf("finished delegate lacks transcript_ref: %q", line)
			}
		}
	}
}

func TestJobListRowRendersOrientationFieldsShell(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	record, err := s.jobManager.createShell(createShellOpts{Command: "sleep 30", Description: "running shell"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, s.jobManager, record.JobID) })

	output := callJobListText(t, s, `{}`)
	var shellLine string
	for line := range strings.SplitSeq(output, "\n") {
		if strings.HasPrefix(line, record.JobID) {
			shellLine = line
			break
		}
	}
	if shellLine == "" {
		t.Fatalf("job_list output missing shell row: %q", output)
	}
	if !strings.Contains(shellLine, "phase process_running") {
		t.Errorf("shell row lacks phase: %q", shellLine)
	}
	if !strings.Contains(shellLine, "running_for_ms") {
		t.Errorf("shell row lacks running_for_ms: %q", shellLine)
	}
	if !strings.Contains(shellLine, "quiet_for_ms") {
		t.Errorf("shell row lacks quiet_for_ms: %q", shellLine)
	}
	if !strings.Contains(shellLine, "transcript_ref "+shellTranscriptRef(record.JobID)) {
		t.Errorf("shell row lacks transcript_ref: %q", shellLine)
	}
}
