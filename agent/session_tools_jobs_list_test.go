package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/jobstore"
	tooldefs "primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

func TestJobListIncludesDelegatesRecoverySurface(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("done") },
	}})
	s := newDelegateTestSession(t, c)
	res := s.createDelegate(context.Background(), delegateArgs{Task: "finish", Background: false, BlockTimeoutMS: 5000})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}

	call := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "list",
		Name:      "job_list",
		Arguments: json.RawMessage(`{}`),
	})
	if call.IsError {
		t.Fatalf("job_list returned error: %s", call.Output)
	}
	var out struct {
		Delegates []struct {
			DelegateID   string `json:"delegate_id"`
			CurrentJobID string `json:"current_job_id"`
			LatestJobID  string `json:"latest_job_id"`
			Status       string `json:"status"`
			Resumable    bool   `json:"resumable"`
		} `json:"delegates"`
		Jobs []struct {
			JobID      string `json:"job_id"`
			DelegateID string `json:"delegate_id"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(toolResultJSON(call), &out); err != nil {
		t.Fatalf("unmarshal job_list: %v", err)
	}
	if len(out.Delegates) != 1 || out.Delegates[0].DelegateID != res.DelegateID || out.Delegates[0].LatestJobID != res.JobID {
		t.Fatalf("delegates = %+v, want delegate recovery row", out.Delegates)
	}
	if len(out.Jobs) == 0 || out.Jobs[0].DelegateID != res.DelegateID {
		t.Fatalf("jobs = %+v, want job annotated with delegate_id", out.Jobs)
	}
	if !strings.Contains(call.Output, "delegate_id "+res.DelegateID) {
		t.Fatalf("job_list output must show delegate_id %q:\n%s", res.DelegateID, call.Output)
	}
	if !strings.Contains(call.Output, "delegate "+res.DelegateID) {
		t.Fatalf("job_list output must show delegate recovery row for %q:\n%s", res.DelegateID, call.Output)
	}
}

func TestJobListHidesDescendantDelegateHandles(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	now := time.Unix(100, 0).UTC()
	ownedStart := now.Add(time.Second)
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateCreated,
		TS:         now,
		DelegateID: "dlg_owned",
		Delegate: &jobstore.DelegateEvent{
			ChildSessionID:   "OWNED",
			TranscriptRef:    encodeRef("", "OWNED"),
			OwnerSessionID:   s.id,
			VisibleSessionID: s.id,
			Generation:       "dg_owned",
			Resumable:        true,
		},
	}); err != nil {
		t.Fatalf("append owned delegate: %v", err)
	}
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               ownedStart,
		JobID:            "job_owned",
		Type:             jobstore.JobDelegate,
		DelegateID:       "dlg_owned",
		OwnerSessionID:   s.id,
		VisibleToSession: s.id,
		TranscriptRef:    encodeRef("", "OWNED"),
		StartedAt:        &ownedStart,
	}); err != nil {
		t.Fatalf("append owned delegate job: %v", err)
	}
	legacyStart := now.Add(1500 * time.Millisecond)
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateCreated,
		TS:         legacyStart,
		DelegateID: "dlg_legacy",
		Delegate: &jobstore.DelegateEvent{
			ChildSessionID: "LEGACY",
			TranscriptRef:  encodeRef("", "LEGACY"),
			Generation:     "dg_legacy",
			Resumable:      true,
		},
	}); err != nil {
		t.Fatalf("append legacy delegate: %v", err)
	}
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:          jobstore.EventJobStarted,
		TS:            legacyStart,
		JobID:         "job_legacy",
		Type:          jobstore.JobDelegate,
		DelegateID:    "dlg_legacy",
		TranscriptRef: encodeRef("", "LEGACY"),
		StartedAt:     &legacyStart,
	}); err != nil {
		t.Fatalf("append legacy delegate job: %v", err)
	}
	descStart := now.Add(2 * time.Second)
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               descStart,
		JobID:            "job_descendant",
		Type:             jobstore.JobDelegate,
		DelegateID:       "dlg_descendant",
		OwnerSessionID:   "CHILD",
		VisibleToSession: s.id,
		TranscriptRef:    encodeRef("", "DESCENDANT"),
		StartedAt:        &descStart,
	}); err != nil {
		t.Fatalf("append descendant delegate job: %v", err)
	}

	out, err := jobListTool(s, map[string]any{}, 1<<20)
	if err != nil {
		t.Fatalf("jobListTool: %v", err)
	}
	var parsed jobListResult
	if err := json.Unmarshal(handlerJSON(t, out), &parsed); err != nil {
		t.Fatalf("unmarshal job_list: %v", err)
	}
	delegates := make(map[string]bool)
	for _, delegate := range parsed.Delegates {
		delegates[delegate.DelegateID] = true
	}
	if !delegates["dlg_owned"] || !delegates["dlg_legacy"] || delegates["dlg_descendant"] {
		t.Fatalf("delegates = %+v, want owned and legacy only", parsed.Delegates)
	}
	for _, job := range parsed.Jobs {
		if job.JobID == "job_descendant" && job.DelegateID != "" {
			t.Fatalf("descendant job row exposes delegate_id %q; want no control handle", job.DelegateID)
		}
		if job.JobID == "job_legacy" && job.DelegateID != "dlg_legacy" {
			t.Fatalf("legacy job row delegate_id = %q, want dlg_legacy", job.DelegateID)
		}
	}
	rendered := out.(tooldefs.StateResult).Output
	if strings.Contains(rendered, "delegate dlg_descendant") || strings.Contains(rendered, "delegate_id dlg_descendant") {
		t.Fatalf("job_list output exposes descendant delegate handle:\n%s", rendered)
	}
}

func TestJobListDelegatesFollowReturnedJobs(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	now := time.Unix(200, 0).UTC()
	appendDelegateJob := func(delegateID, jobID string, started time.Time) {
		t.Helper()
		if err := s.jobManager.appendEvent(jobstore.Event{
			Kind:       jobstore.EventDelegateCreated,
			TS:         started.Add(-time.Millisecond),
			DelegateID: delegateID,
			Delegate: &jobstore.DelegateEvent{
				ChildSessionID:   strings.ToUpper(delegateID),
				TranscriptRef:    encodeRef("", strings.ToUpper(delegateID)),
				OwnerSessionID:   s.id,
				VisibleSessionID: s.id,
				Generation:       "dg_" + delegateID,
				Resumable:        true,
			},
		}); err != nil {
			t.Fatalf("append delegate %s: %v", delegateID, err)
		}
		if err := s.jobManager.appendEvent(jobstore.Event{
			Kind:             jobstore.EventJobStarted,
			TS:               started,
			JobID:            jobID,
			Type:             jobstore.JobDelegate,
			DelegateID:       delegateID,
			OwnerSessionID:   s.id,
			VisibleToSession: s.id,
			TranscriptRef:    encodeRef("", strings.ToUpper(delegateID)),
			StartedAt:        &started,
		}); err != nil {
			t.Fatalf("append delegate job %s: %v", jobID, err)
		}
	}
	appendDelegateJob("dlg_old", "job_old", now)
	appendDelegateJob("dlg_new", "job_new", now.Add(time.Second))

	out, err := jobListTool(s, map[string]any{"limit": 1}, 1<<20)
	if err != nil {
		t.Fatalf("jobListTool(limit=1): %v", err)
	}
	var parsed jobListResult
	if err := json.Unmarshal(handlerJSON(t, out), &parsed); err != nil {
		t.Fatalf("unmarshal job_list: %v", err)
	}
	if len(parsed.Jobs) != 1 || parsed.Jobs[0].JobID != "job_new" || parsed.Jobs[0].DelegateID != "dlg_new" {
		t.Fatalf("jobs = %+v, want newest delegate job only", parsed.Jobs)
	}
	if len(parsed.Delegates) != 1 || parsed.Delegates[0].DelegateID != "dlg_new" {
		t.Fatalf("delegates = %+v, want only delegate for returned job", parsed.Delegates)
	}
	rendered := out.(tooldefs.StateResult).Output
	if strings.Contains(rendered, "dlg_old") {
		t.Fatalf("job_list(limit=1) leaked delegate outside returned jobs:\n%s", rendered)
	}

	filtered, err := jobListTool(s, map[string]any{"type": []any{"shell"}}, 1<<20)
	if err != nil {
		t.Fatalf("jobListTool(type=shell): %v", err)
	}
	var filteredParsed jobListResult
	if err := json.Unmarshal(handlerJSON(t, filtered), &filteredParsed); err != nil {
		t.Fatalf("unmarshal filtered job_list: %v", err)
	}
	if len(filteredParsed.Jobs) != 0 || len(filteredParsed.Delegates) != 0 {
		t.Fatalf("filtered job_list = jobs %+v delegates %+v, want no delegate side channel", filteredParsed.Jobs, filteredParsed.Delegates)
	}
}

func TestJobListDelegatesDoNotExposeFilteredCurrentJob(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	now := time.Unix(300, 0).UTC()
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateCreated,
		TS:         now,
		DelegateID: "dlg_history",
		Delegate: &jobstore.DelegateEvent{
			ChildSessionID:   "HISTORY",
			TranscriptRef:    encodeRef("", "HISTORY"),
			OwnerSessionID:   s.id,
			VisibleSessionID: s.id,
			Generation:       "dg_history",
			Resumable:        true,
		},
	}); err != nil {
		t.Fatalf("append delegate: %v", err)
	}
	oldStart := now.Add(time.Second)
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               oldStart,
		JobID:            "job_old",
		Type:             jobstore.JobDelegate,
		DelegateID:       "dlg_history",
		OwnerSessionID:   s.id,
		VisibleToSession: s.id,
		TranscriptRef:    encodeRef("", "HISTORY"),
		StartedAt:        &oldStart,
	}); err != nil {
		t.Fatalf("append old delegate job: %v", err)
	}
	oldEnd := oldStart.Add(time.Second)
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:     jobstore.EventJobFinished,
		TS:       oldEnd,
		JobID:    "job_old",
		Status:   jobstore.StatusCompleted,
		EndedAt:  &oldEnd,
		Reason:   "done",
		ExitCode: intPtr(0),
	}); err != nil {
		t.Fatalf("finish old delegate job: %v", err)
	}
	newStart := oldEnd.Add(time.Second)
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               newStart,
		JobID:            "job_new",
		Type:             jobstore.JobDelegate,
		DelegateID:       "dlg_history",
		OwnerSessionID:   s.id,
		VisibleToSession: s.id,
		TranscriptRef:    encodeRef("", "HISTORY"),
		StartedAt:        &newStart,
	}); err != nil {
		t.Fatalf("append new delegate job: %v", err)
	}

	out, err := jobListTool(s, map[string]any{"status": []any{"completed"}}, 1<<20)
	if err != nil {
		t.Fatalf("jobListTool(status=completed): %v", err)
	}
	var parsed jobListResult
	if err := json.Unmarshal(handlerJSON(t, out), &parsed); err != nil {
		t.Fatalf("unmarshal job_list: %v", err)
	}
	if len(parsed.Jobs) != 1 || parsed.Jobs[0].JobID != "job_old" {
		t.Fatalf("jobs = %+v, want only completed historical job", parsed.Jobs)
	}
	if len(parsed.Delegates) != 0 {
		t.Fatalf("delegates = %+v, want no current/latest leak for filtered running job", parsed.Delegates)
	}
	rendered := out.(tooldefs.StateResult).Output
	if strings.Contains(rendered, "job_new") || strings.Contains(rendered, "current_job_id") || strings.Contains(rendered, "latest_job_id") {
		t.Fatalf("job_list(status=completed) leaked hidden current job:\n%s", rendered)
	}
}

// TestJobListRowIsLean pins that a job_list scan row drops detail-only fields
// and null/empty fields: no transcript_ref/resumable/visible_to_session_id, no
// explicit nulls, and no empty recent_watches array.
func TestJobListRowIsLean(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
	})
	if res.IsError {
		t.Fatalf("shell returned error: %s", res.Output)
	}
	var out struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal(toolResultJSON(res), &out)
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(out.JobID)
		waitForShellDone(t, s.jobManager, out.JobID)
	})

	listRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "l1",
		Name:      "job_list",
		Arguments: json.RawMessage(`{}`),
	})
	if listRes.IsError {
		t.Fatalf("job_list returned error: %s", listRes.Output)
	}
	// The model-facing job_list is plain text: a lean one-line row carrying no JSON
	// noise (no null fields, internal keys, or resumable/transcript clutter for an
	// ordinary running shell job).
	body := listRes.Output
	for _, banned := range []string{"transcript_ref", "resumable", "not_resumable_reason", "visible_to_session_id", "recent_watches", "null", "{"} {
		if strings.Contains(body, banned) {
			t.Errorf("lean job_list row must not contain %q:\n%s", banned, body)
		}
	}
	// A shell job's row identifies it by its command and reports its output size.
	if !strings.Contains(body, "sleep 30") {
		t.Errorf("shell job_list row must include its command:\n%s", body)
	}
	if !strings.Contains(body, "bytes") {
		t.Errorf("shell job_list row must report its output size:\n%s", body)
	}
	// The structured row (State) names the size field total_bytes everywhere the
	// agent reads it (shell result, job_read_output, job_list) — not output_bytes.
	state := string(toolResultJSON(listRes))
	if !strings.Contains(state, "total_bytes") || strings.Contains(state, "output_bytes") {
		t.Errorf("job_list state must use total_bytes, not output_bytes:\n%s", state)
	}
}

func TestJobListToolIncludeNestedSurfacesForwardedRecords(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	child.jobManager.forward = parent.jobManager.forwardEvent
	child.jobManager.parentJobID = "job_PARENTDELEGATE"

	parentRec, err := parent.jobManager.createShell(createShellOpts{Command: "sleep 1", Description: "parent"})
	if err != nil {
		t.Fatalf("create parent shell: %v", err)
	}
	childRec, err := child.jobManager.createShell(createShellOpts{Command: "sleep 1", Description: "nested"})
	if err != nil {
		t.Fatalf("create child shell: %v", err)
	}
	t.Cleanup(func() {
		finishRunningTestJob(t, parent.jobManager, parentRec.JobID)
		finishRunningTestJob(t, child.jobManager, childRec.JobID)
	})

	runList := func(args string) jobListToolOutput {
		t.Helper()
		res := parent.reg.ExecuteCall(context.Background(), parent.env, llm.ToolCallData{
			ID:        "list",
			Name:      "job_list",
			Arguments: json.RawMessage(args),
		})
		if res.IsError {
			t.Fatalf("job_list returned error: %s", res.Output)
		}
		var out jobListToolOutput
		if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
			t.Fatalf("unmarshal job_list output: %v (output: %s)", err, res.Output)
		}
		return out
	}

	defaultOut := runList(`{}`)
	if jobListToolOutputContains(defaultOut.Jobs, childRec.JobID) {
		t.Fatalf("default job_list jobs = %+v, want nested job hidden", defaultOut.Jobs)
	}
	if !jobListToolOutputContains(defaultOut.Jobs, parentRec.JobID) {
		t.Fatalf("default job_list jobs = %+v, want parent job %q", defaultOut.Jobs, parentRec.JobID)
	}

	nestedOut := runList(`{"include_nested":true}`)
	nestedJob := findJobListToolOutput(nestedOut.Jobs, childRec.JobID)
	if nestedJob == nil {
		t.Fatalf("include_nested job_list jobs = %+v, want nested job %q", nestedOut.Jobs, childRec.JobID)
	}
	if nestedJob.ParentJobID == nil || *nestedJob.ParentJobID != "job_PARENTDELEGATE" {
		t.Fatalf("nested job parent_job_id = %v, want job_PARENTDELEGATE", nestedJob.ParentJobID)
	}
}

// TestJobListDefaultListingOmitsDepth proves the default listing does not
// serialize the depth field: projectJobRecord never sets Depth for default rows
// (only walkDescendantJobs does for include_descendants), so a zero Depth must be
// omitted, not emitted as "depth":0.

// TestJobListDefaultListingOmitsDepth proves the default listing does not
// serialize the depth field: projectJobRecord never sets Depth for default rows
// (only walkDescendantJobs does for include_descendants), so a zero Depth must be
// omitted, not emitted as "depth":0.
func TestJobListDefaultListingOmitsDepth(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rec, err := s.jobManager.createShell(createShellOpts{Command: "sleep 1", Description: "job"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, s.jobManager, rec.JobID) })

	out, err := jobListTool(s, decodeJobListArgs(t, `{}`), 1<<20)
	if err != nil {
		t.Fatalf("jobListTool: %v", err)
	}
	if strings.Contains(string(handlerJSON(t, out)), `"depth"`) {
		t.Fatalf("default job_list output contains depth key: %s", out)
	}
}

// jobListDescendantEntry parses the descendant-walk row fields: the existing
// owner_session_id plus the new depth annotation, and the resumability
// projection (which must key on the owner session, not the root caller).

func TestJobListIncludeDescendantsHidesChildDelegateHandles(t *testing.T) {
	t.Parallel()
	rootJM := newWalkJobManager(t, "ROOT")
	childJM := newWalkJobManager(t, "CHILD")
	t.Cleanup(func() {
		_ = rootJM.store.Close()
		_ = childJM.store.Close()
	})

	now := time.Unix(150, 0).UTC()
	if err := childJM.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateCreated,
		TS:         now,
		DelegateID: "dlg_child",
		Delegate: &jobstore.DelegateEvent{
			ChildSessionID:   "GRANDCHILD",
			TranscriptRef:    encodeRef("", "GRANDCHILD"),
			OwnerSessionID:   "CHILD",
			VisibleSessionID: "CHILD",
			Generation:       "dg_child",
			Resumable:        true,
		},
	}); err != nil {
		t.Fatalf("append child delegate: %v", err)
	}
	started := now.Add(time.Millisecond)
	if err := childJM.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               started,
		JobID:            "job_child_delegate",
		Type:             jobstore.JobDelegate,
		DelegateID:       "dlg_child",
		OwnerSessionID:   "CHILD",
		VisibleToSession: "CHILD",
		TranscriptRef:    encodeRef("", "GRANDCHILD"),
		StartedAt:        &started,
	}); err != nil {
		t.Fatalf("append child delegate job: %v", err)
	}
	legacyStarted := now.Add(2 * time.Millisecond)
	if err := childJM.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateCreated,
		TS:         legacyStarted,
		DelegateID: "dlg_child_legacy",
		Delegate: &jobstore.DelegateEvent{
			ChildSessionID: "LEGACY_GRANDCHILD",
			TranscriptRef:  encodeRef("", "LEGACY_GRANDCHILD"),
			Generation:     "dg_child_legacy",
			Resumable:      true,
		},
	}); err != nil {
		t.Fatalf("append legacy child delegate: %v", err)
	}
	if err := childJM.appendEvent(jobstore.Event{
		Kind:          jobstore.EventJobStarted,
		TS:            legacyStarted,
		JobID:         "job_child_legacy_delegate",
		Type:          jobstore.JobDelegate,
		DelegateID:    "dlg_child_legacy",
		TranscriptRef: encodeRef("", "LEGACY_GRANDCHILD"),
		StartedAt:     &legacyStarted,
	}); err != nil {
		t.Fatalf("append legacy child delegate job: %v", err)
	}

	child := &Session{id: "CHILD", jobManager: childJM, subagents: newSubagentManager(nil)}
	root := &Session{id: "ROOT", jobManager: rootJM, subagents: newSubagentManager(nil)}
	root.subagents.track(&subagent{id: "CHILD", sess: child, status: SubagentRunning})

	out, err := jobListTool(root, decodeJobListArgs(t, `{"include_descendants":true}`), 1<<20)
	if err != nil {
		t.Fatalf("jobListTool(include_descendants): %v", err)
	}
	var parsed jobListDescendantOutput
	if err := json.Unmarshal(handlerJSON(t, out), &parsed); err != nil {
		t.Fatalf("unmarshal job_list output: %v (output: %s)", err, out)
	}
	row := findDescendantRow(parsed.Jobs, "job_child_delegate")
	if row == nil {
		t.Fatalf("include_descendants jobs = %+v, want child delegate job", parsed.Jobs)
	}
	if row.DelegateID != "" {
		t.Fatalf("child delegate row exposes delegate_id %q; want no parent-visible control handle", row.DelegateID)
	}
	legacyRow := findDescendantRow(parsed.Jobs, "job_child_legacy_delegate")
	if legacyRow == nil {
		t.Fatalf("include_descendants jobs = %+v, want legacy child delegate job", parsed.Jobs)
	}
	if legacyRow.DelegateID != "" {
		t.Fatalf("legacy child delegate row exposes delegate_id %q; want no parent-visible control handle", legacyRow.DelegateID)
	}
	rendered := out.(tooldefs.StateResult).Output
	if strings.Contains(rendered, "delegate_id dlg_child") ||
		strings.Contains(rendered, "delegate dlg_child") ||
		strings.Contains(rendered, "delegate_id dlg_child_legacy") ||
		strings.Contains(rendered, "delegate dlg_child_legacy") {
		t.Fatalf("include_descendants output exposes child delegate handle:\n%s", rendered)
	}
}

// newWalkJobManager builds an isolated jobManager for a single tree level.

// TestJobListIncludeDescendantsWalksLiveTree drives a depth-3 live tree
// (root -> coordinator -> worker) plus a dead coordinator branch, then asserts
// job_list(include_descendants=true) returns one row per job at its real owner
// depth, suppresses forwarded copies whose owner appears live, surfaces only the
// terminal forwarded copy for a dead coordinator (no recursion into the gone
// session), and leaves default + include_nested semantics unchanged.
func TestJobListIncludeDescendantsWalksLiveTree(t *testing.T) {
	t.Parallel()
	rootJM := newWalkJobManager(t, "ROOT")
	coordJM := newWalkJobManager(t, "COORD")
	workerJM := newWalkJobManager(t, "WORK")
	deadJM := newWalkJobManager(t, "DEAD")
	deadChildJM := newWalkJobManager(t, "DEADCHILD")
	t.Cleanup(func() {
		_ = rootJM.store.Close()
		_ = coordJM.store.Close()
		_ = workerJM.store.Close()
		_ = deadJM.store.Close()
		_ = deadChildJM.store.Close()
	})

	// One-hop forwarding: each child forwards into its direct parent's store.
	coordJM.forward = rootJM.forwardEvent
	coordJM.parentJobID = "job_root_delegate_coord"
	workerJM.forward = coordJM.forwardEvent
	workerJM.parentJobID = "job_coord_delegate_worker"
	deadJM.forward = rootJM.forwardEvent
	deadJM.parentJobID = "job_root_delegate_dead"
	deadChildJM.forward = deadJM.forwardEvent
	deadChildJM.parentJobID = "job_dead_delegate_child"

	// Owner records, each forwarded one hop into its parent's store.
	rootRec, err := rootJM.createShell(createShellOpts{Command: "sleep 1", Description: "root job"})
	if err != nil {
		t.Fatalf("create root shell: %v", err)
	}
	coordRec, err := coordJM.createShell(createShellOpts{Command: "sleep 1", Description: "coordinator job"})
	if err != nil {
		t.Fatalf("create coordinator shell: %v", err)
	}
	workerRec, err := workerJM.createShell(createShellOpts{Command: "sleep 1", Description: "worker job"})
	if err != nil {
		t.Fatalf("create worker shell: %v", err)
	}
	deadRec, err := deadJM.createShell(createShellOpts{Command: "sleep 1", Description: "dead coordinator job"})
	if err != nil {
		t.Fatalf("create dead coordinator shell: %v", err)
	}
	// A job owned by the dead coordinator's own child, forwarded only one hop
	// into the dead coordinator's store. To surface it the walk would have to
	// recurse INTO the dead (closed) coordinator; its absence proves live-only
	// recursion ("resume it to dig deeper").
	deadGrandRec, err := deadChildJM.createShell(createShellOpts{Command: "sleep 1", Description: "dead grandchild job"})
	if err != nil {
		t.Fatalf("create dead grandchild shell: %v", err)
	}
	t.Cleanup(func() {
		finishRunningTestJob(t, rootJM, rootRec.JobID)
		finishRunningTestJob(t, coordJM, coordRec.JobID)
		finishRunningTestJob(t, workerJM, workerRec.JobID)
		finishRunningTestJob(t, deadChildJM, deadGrandRec.JobID)
	})

	// The dead coordinator finalized its forwarded job before dying; the terminal
	// forwarded copy survives in the root's store.
	deadExit := 0
	if err := deadJM.finalize(deadRec.JobID, jobstore.StatusCompleted, "exit_zero", &deadExit); err != nil {
		t.Fatalf("finalize dead coordinator job: %v", err)
	}

	worker := &Session{id: "WORK", jobManager: workerJM, subagents: newSubagentManager(nil)}
	coordinator := &Session{id: "COORD", jobManager: coordJM, subagents: newSubagentManager(nil)}
	coordinator.subagents.track(&subagent{id: "WORK", sess: worker, status: SubagentRunning})
	dead := &Session{id: "DEAD", jobManager: deadJM, subagents: newSubagentManager(nil)}

	root := &Session{id: "ROOT", jobManager: rootJM, subagents: newSubagentManager(nil)}
	root.subagents.track(&subagent{id: "COORD", sess: coordinator, status: SubagentRunning})
	root.subagents.track(&subagent{id: "DEAD", sess: dead, status: SubagentCompleted, closed: true})

	run := func(args string) jobListDescendantOutput {
		t.Helper()
		out, err := jobListTool(root, decodeJobListArgs(t, args), 1<<20)
		if err != nil {
			t.Fatalf("jobListTool(%s): %v", args, err)
		}
		var parsed jobListDescendantOutput
		if err := json.Unmarshal(handlerJSON(t, out), &parsed); err != nil {
			t.Fatalf("unmarshal job_list output: %v (output: %s)", err, out)
		}
		return parsed
	}

	descendants := run(`{"include_descendants":true}`)

	// Each owner job appears exactly once, at its real owner depth.
	rootRow := findDescendantRow(descendants.Jobs, rootRec.JobID)
	if rootRow == nil || rootRow.OwnerSessionID != "ROOT" || rootRow.Depth != 0 {
		t.Fatalf("root row = %+v, want owner=ROOT depth=0", rootRow)
	}
	coordRow := findDescendantRow(descendants.Jobs, coordRec.JobID)
	if coordRow == nil || coordRow.OwnerSessionID != "COORD" || coordRow.Depth != 1 {
		t.Fatalf("coordinator row = %+v, want owner=COORD depth=1", coordRow)
	}
	workerRow := findDescendantRow(descendants.Jobs, workerRec.JobID)
	if workerRow == nil || workerRow.OwnerSessionID != "WORK" || workerRow.Depth != 2 {
		t.Fatalf("worker row = %+v, want owner=WORK depth=2", workerRow)
	}

	// Dedupe: exactly one row per job_id (forwarded copies of live-owner jobs
	// are suppressed in favor of the owner record found by recursion).
	for _, jobID := range []string{rootRec.JobID, coordRec.JobID, workerRec.JobID} {
		count := 0
		for _, row := range descendants.Jobs {
			if row.JobID == jobID {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("job_id %q appears %d times, want exactly 1: %+v", jobID, count, descendants.Jobs)
		}
	}

	// Dead coordinator: only the terminal forwarded copy surfaces (from the root
	// store, depth 0). No recursion into the gone session.
	deadRow := findDescendantRow(descendants.Jobs, deadRec.JobID)
	if deadRow == nil || deadRow.OwnerSessionID != "DEAD" || deadRow.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("dead coordinator row = %+v, want owner=DEAD completed terminal copy", deadRow)
	}
	if findDescendantRow(descendants.Jobs, deadGrandRec.JobID) != nil {
		t.Fatalf("dead grandchild job %q leaked into walk: %+v", deadGrandRec.JobID, descendants.Jobs)
	}

	// Default job_list: own jobs only, forwarded copies hidden, no descendants.
	defaultOut := run(`{}`)
	if findDescendantRow(defaultOut.Jobs, rootRec.JobID) == nil {
		t.Fatalf("default job_list = %+v, want root job", defaultOut.Jobs)
	}
	for _, hidden := range []string{coordRec.JobID, workerRec.JobID} {
		if findDescendantRow(defaultOut.Jobs, hidden) != nil {
			t.Fatalf("default job_list leaked nested job %q: %+v", hidden, defaultOut.Jobs)
		}
	}

	// include_nested: one hop only — root's own forwarded copies (coordinator,
	// dead coordinator) are visible; the worker (two hops) is not.
	nestedOut := run(`{"include_nested":true}`)
	if findDescendantRow(nestedOut.Jobs, coordRec.JobID) == nil {
		t.Fatalf("include_nested job_list = %+v, want forwarded coordinator job", nestedOut.Jobs)
	}
	if findDescendantRow(nestedOut.Jobs, workerRec.JobID) != nil {
		t.Fatalf("include_nested job_list leaked two-hop worker job %q: %+v", workerRec.JobID, nestedOut.Jobs)
	}
}

// TestJobListIncludeDescendantsSurfacesOwnStoreError proves the depth-0 (own
// store) error is surfaced, not swallowed: a closed own store makes both plain
// job_list and job_list(include_descendants=true) return the same ErrStoreClosed.
// Before the fix the descendant walk swallowed the depth-0 error and returned an
// empty list with success — a silent regression from the plain path.

// TestJobListIncludeDescendantsSurfacesOwnStoreError proves the depth-0 (own
// store) error is surfaced, not swallowed: a closed own store makes both plain
// job_list and job_list(include_descendants=true) return the same ErrStoreClosed.
// Before the fix the descendant walk swallowed the depth-0 error and returned an
// empty list with success — a silent regression from the plain path.
func TestJobListIncludeDescendantsSurfacesOwnStoreError(t *testing.T) {
	t.Parallel()
	rootJM := newWalkJobManager(t, "ROOT")
	root := &Session{id: "ROOT", jobManager: rootJM, subagents: newSubagentManager(nil)}

	// Close the depth-0 (own) store before the call so both paths hit
	// ErrStoreClosed from the same store.
	if err := rootJM.store.Close(); err != nil {
		t.Fatalf("close root store: %v", err)
	}

	// Parity: plain job_list surfaces the closed-store error today.
	if _, plainErr := jobListTool(root, decodeJobListArgs(t, `{}`), 1<<20); !errors.Is(plainErr, jobstore.ErrStoreClosed) {
		t.Fatalf("plain job_list error = %v, want ErrStoreClosed", plainErr)
	}

	_, descErr := jobListTool(root, decodeJobListArgs(t, `{"include_descendants":true}`), 1<<20)
	if descErr == nil {
		t.Fatalf("job_list(include_descendants=true) error = nil, want ErrStoreClosed surfaced from the own store")
	}
	if !errors.Is(descErr, jobstore.ErrStoreClosed) {
		t.Fatalf("job_list(include_descendants=true) error = %v, want ErrStoreClosed", descErr)
	}
}

// TestJobListIncludeDescendantsProjectsRuntimeLostViaOwner proves the
// include_descendants walk projects each descendant row against its OWNER
// session, not the root caller. A worker-owned runtime_lost delegate's
// resumability is assessed by assessDelegateResumability, which gates on the
// assessing session's identity (descriptor.ParentSessionID == session ID).
// Projecting against the root mis-reads that gate (parent_linkage_unavailable);
// the owner projection clears it and reports a different, downstream reason. The
// list row must match the owner projection.

// TestJobListIncludeDescendantsProjectsRuntimeLostViaOwner proves the
// include_descendants walk projects each descendant row against its OWNER
// session, not the root caller. A worker-owned runtime_lost delegate's
// resumability is assessed by assessDelegateResumability, which gates on the
// assessing session's identity (descriptor.ParentSessionID == session ID).
// Projecting against the root mis-reads that gate (parent_linkage_unavailable);
// the owner projection clears it and reports a different, downstream reason. The
// list row must match the owner projection.
func TestJobListIncludeDescendantsProjectsRuntimeLostViaOwner(t *testing.T) {
	t.Parallel()
	rootJM := newWalkJobManager(t, "ROOT")
	coordJM := newWalkJobManager(t, "COORD")
	workerJM := newWalkJobManager(t, "WORK")
	t.Cleanup(func() {
		_ = rootJM.store.Close()
		_ = coordJM.store.Close()
		_ = workerJM.store.Close()
	})

	// One-hop forwarding: each child forwards into its direct parent's store.
	coordJM.forward = rootJM.forwardEvent
	coordJM.parentJobID = "job_root_delegate_coord"
	workerJM.forward = coordJM.forwardEvent
	workerJM.parentJobID = "job_coord_delegate_worker"

	// A worker-owned runtime_lost delegate (descriptor.ParentSessionID == "WORK"),
	// forwarded one hop up into the coordinator's store.
	delegRec := workerOwnedRuntimeLostDelegate(t, workerJM, "WORK")

	worker := &Session{id: "WORK", jobManager: workerJM, subagents: newSubagentManager(nil)}
	coordinator := &Session{id: "COORD", jobManager: coordJM, subagents: newSubagentManager(nil)}
	coordinator.subagents.track(&subagent{id: "WORK", sess: worker, status: SubagentRunning})
	root := &Session{id: "ROOT", jobManager: rootJM, subagents: newSubagentManager(nil)}
	root.subagents.track(&subagent{id: "COORD", sess: coordinator, status: SubagentRunning})

	// Oracle: projecting against the true owner (worker) clears the parent-linkage
	// gate; projecting against the root does not.
	viaOwner := projectJobRecord(worker, delegRec)
	viaRoot := projectJobRecord(root, delegRec)
	if viaRoot.NotResumableReason == nil || *viaRoot.NotResumableReason != notResumableParentLinkageUnavailable {
		t.Fatalf("root projection reason = %v, want %q (mis-projection oracle)", viaRoot.NotResumableReason, notResumableParentLinkageUnavailable)
	}
	if viaOwner.NotResumableReason != nil && *viaOwner.NotResumableReason == notResumableParentLinkageUnavailable {
		t.Fatalf("owner projection reason = %q, want owner to clear the parent-linkage gate", *viaOwner.NotResumableReason)
	}

	out, err := jobListTool(root, decodeJobListArgs(t, `{"include_descendants":true}`), 1<<20)
	if err != nil {
		t.Fatalf("jobListTool(include_descendants): %v", err)
	}
	var parsed jobListDescendantOutput
	if err := json.Unmarshal(handlerJSON(t, out), &parsed); err != nil {
		t.Fatalf("unmarshal job_list output: %v (output: %s)", err, out)
	}

	row := findDescendantRow(parsed.Jobs, delegRec.JobID)
	if row == nil {
		t.Fatalf("worker runtime_lost delegate %q missing from descendant walk: %+v", delegRec.JobID, parsed.Jobs)
	}
	// The row must match the OWNER projection, NOT the root mis-projection.
	if row.NotResumableReason != nil && *row.NotResumableReason == notResumableParentLinkageUnavailable {
		t.Fatalf("list row reason = %q, want owner projection (root mis-projection leaked)", *row.NotResumableReason)
	}
	if !stringPtrEqual(row.NotResumableReason, viaOwner.NotResumableReason) {
		t.Fatalf("list row not_resumable_reason = %v, want owner projection %v", row.NotResumableReason, viaOwner.NotResumableReason)
	}
	if !boolPtrEqual(row.Resumable, viaOwner.Resumable) {
		t.Fatalf("list row resumable = %v, want owner projection %v", row.Resumable, viaOwner.Resumable)
	}
}

func TestJobListWatchesOmittedWhenNoneConfigured(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	out := runJobListTool(t, s)
	if len(out.Watches) != 0 {
		t.Fatalf("job_list watches = %+v, want omitted when none configured", out.Watches)
	}
	// The empty watches array is omitted from the wire entirely (lean scan), not
	// serialized as `"watches":[]`.
	raw := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID: "lraw", Name: "job_list", Arguments: json.RawMessage(`{}`),
	})
	if strings.Contains(raw.Output, "\"watches\"") {
		t.Fatalf("job_list must omit the empty watches key:\n%s", raw.Output)
	}
}

func TestJobListEnumeratesActiveWatches(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	jm := s.jobManager

	rec, err := jm.createShell(createShellOpts{Command: "sleep 30", Description: "watched shell"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })

	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"}); err != nil {
		t.Fatalf("configure output_match watch: %v", err)
	}
	seedWatchSendDelegateTarget(t, jm, "dlg_obs")
	if _, err := jm.configureWatch(watchArgs{
		Target: rec.JobID,
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "dlg_obs", Message: "fyi"},
	}); err != nil {
		t.Fatalf("configure event watch with send: %v", err)
	}

	out := runJobListTool(t, s)
	if len(out.Watches) != 2 {
		t.Fatalf("job_list watches = %+v, want 2 entries", out.Watches)
	}

	notify := findJobListToolWatch(out.Watches, rec.JobID, "output_match: ready")
	if notify == nil {
		t.Fatalf("job_list watches = %+v, want notify-caller output_match watch", out.Watches)
	}
	if notify.Condition != "output_match: ready" {
		t.Fatalf("notify watch condition = %q, want %q", notify.Condition, "output_match: ready")
	}
	if notify.CreatedAt == "" {
		t.Fatalf("notify watch created_at must be populated, got empty")
	}
	if _, err := time.Parse(time.RFC3339Nano, notify.CreatedAt); err != nil {
		t.Fatalf("notify watch created_at = %q, not RFC3339Nano: %v", notify.CreatedAt, err)
	}

	sidecar := findJobListToolWatch(out.Watches, rec.JobID, "events: [job.notification]")
	if sidecar == nil {
		t.Fatalf("job_list watches = %+v, want sidecar event watch", out.Watches)
	}
	if sidecar.Condition != "events: [job.notification]" {
		t.Fatalf("sidecar watch condition = %q, want %q", sidecar.Condition, "events: [job.notification]")
	}
}

func TestJobListWatchConditionSummaryFormats(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	jm := s.jobManager
	jm.enqueue = func(jobNotification) {}

	// progress watch on a running shell.
	rec, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, ProgressIntervalMS: 2000}); err != nil {
		t.Fatalf("configure progress watch: %v", err)
	}

	// every-Nth event watch with a send (legal: not a self-delivery back to caller).
	seedWatchSendDelegateTarget(t, jm, "dlg_obs")
	if _, err := jm.configureWatch(watchArgs{
		Target: rec.JobID,
		Events: []string{"communicate"},
		Every:  5,
		Send:   &watchSendArgs{To: "dlg_obs"},
	}); err != nil {
		t.Fatalf("configure every-N event watch: %v", err)
	}

	out := runJobListTool(t, s)

	progress := findJobListToolWatch(out.Watches, rec.JobID, "progress_interval_ms: 2000")
	if progress == nil {
		t.Fatalf("watches = %+v, want progress watch", out.Watches)
	}
	if progress.Condition != "progress_interval_ms: 2000" {
		t.Fatalf("progress condition = %q, want %q", progress.Condition, "progress_interval_ms: 2000")
	}

	every := findJobListToolWatch(out.Watches, rec.JobID, "events: [communicate] every 5")
	if every == nil {
		t.Fatalf("watches = %+v, want every-N watch", out.Watches)
	}
	if every.Condition != "events: [communicate] every 5" {
		t.Fatalf("every-N condition = %q, want %q", every.Condition, "events: [communicate] every 5")
	}
}

func TestJobListWatchReflectsDeliveries(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	jm := s.jobManager
	jm.enqueue = func(jobNotification) {}

	// A no-send caller event watch counts one delivery per fired event.
	installWatchBelowValidation(t, jm, watchArgs{Target: "caller", Events: []string{"communicate"}})
	for i := 0; i < 3; i++ {
		onSessionEventKD(jm, events.EventCommunicate, nil)
	}

	out := runJobListTool(t, s)
	w := findJobListToolWatch(out.Watches, "self", "events: [communicate]")
	if w == nil {
		t.Fatalf("watches = %+v, want caller watch", out.Watches)
	}
	if w.Deliveries != 3 {
		t.Fatalf("caller watch deliveries = %d, want 3", w.Deliveries)
	}
}

func TestJobListExcludesTerminalFlushWatches(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	jm := s.jobManager

	// A config that lives ONLY in terminalFlush (with a pending send) must not be
	// enumerated: F2 reads jm.watches exclusively.
	flushCfg := &watchConfig{target: "job_GONE", send: &watchSendArgs{To: "dlg_obs"}}
	jm.mu.Lock()
	if jm.terminalFlush == nil {
		jm.terminalFlush = make(map[*watchConfig]bool)
	}
	jm.terminalFlush[flushCfg] = true
	jm.mu.Unlock()

	out := runJobListTool(t, s)
	if findJobListToolWatch(out.Watches, "job_GONE", "dlg_obs") != nil {
		t.Fatalf("terminal-flush watch leaked into job_list watches: %+v", out.Watches)
	}
	if len(out.Watches) != 0 {
		t.Fatalf("watches = %+v, want only live watches (none)", out.Watches)
	}
}

func TestDefJobListDescriptionMentionsActiveWatches(t *testing.T) {
	t.Parallel()
	desc := tooldefs.DefJobList().Description
	if !strings.Contains(desc, "The result also includes your active watches.") {
		t.Fatalf("DefJobList description = %q, want it to mention active watches", desc)
	}
}

func TestJobListStoppedDelegateResumableAssessmentIsDynamicAndPure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		breakState func(*testing.T, *Session, *jobstore.JobRecord)
		wantReason string
	}{
		{
			name: "resumable",
		},
		{
			name: "missing descriptor",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore = nil
				replaceStoredDelegateRecord(t, s, rec)
			},
			wantReason: "missing_delegate_resume_metadata",
		},
		{
			name: "bad linkage",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.ParentSessionID = "other"
				replaceStoredDelegateRecord(t, s, rec)
			},
			wantReason: "parent_linkage_unavailable",
		},
		{
			name: "missing local env policy",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.LocalEnvPolicy = ""
				replaceStoredDelegateRecord(t, s, rec)
			},
			wantReason: "parent_linkage_unavailable",
		},
		{
			name: "invalid local env policy",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.LocalEnvPolicy = "all-ish"
				replaceStoredDelegateRecord(t, s, rec)
			},
			wantReason: "parent_linkage_unavailable",
		},
		{
			name: "missing working dir",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.WorkingDir = ""
				replaceStoredDelegateRecord(t, s, rec)
			},
			wantReason: "parent_linkage_unavailable",
		},
		{
			name: "missing meta",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				removeChildSessionMeta(t, s, rec)
			},
			wantReason: "missing_child_session_meta",
		},
		{
			name: "corrupt meta",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				writeChildSessionMeta(t, s, rec, []byte(`{`))
			},
			wantReason: "corrupt_child_session_meta",
		},
		{
			name: "wrong meta id",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				meta, err := schema.LoadSessionMeta(s.stateDir, rec.DelegateRestore.ChildSessionID)
				if err != nil {
					t.Fatalf("load child meta: %v", err)
				}
				meta.ID = "other-child"
				data, err := json.Marshal(meta)
				if err != nil {
					t.Fatalf("marshal child meta: %v", err)
				}
				writeChildSessionMeta(t, s, rec, data)
			},
			wantReason: "corrupt_child_session_meta",
		},
		{
			name: "empty meta id",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				meta, err := schema.LoadSessionMeta(s.stateDir, rec.DelegateRestore.ChildSessionID)
				if err != nil {
					t.Fatalf("load child meta: %v", err)
				}
				meta.ID = ""
				data, err := json.Marshal(meta)
				if err != nil {
					t.Fatalf("marshal child meta: %v", err)
				}
				writeChildSessionMeta(t, s, rec, data)
			},
			wantReason: "corrupt_child_session_meta",
		},
		{
			name: "missing transcript",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				removeChildTranscript(t, s, rec)
			},
			wantReason: "missing_child_transcript",
		},
		{
			name: "corrupt transcript",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				appendChildTranscript(t, s, rec, "\n{not-json}\n")
			},
			wantReason: "corrupt_child_transcript",
		},
		{
			name: "corrupt transcript misleading kind",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				appendChildTranscript(t, s, rec, "\n{\"kind\":\"transcript_session_mismatch\"}\n")
			},
			wantReason: "corrupt_child_transcript",
		},
		{
			name: "oversized transcript line",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				s.strictTranscriptMaxLineBytes = 512
				appendChildTranscript(t, s, rec, "\n"+strings.Repeat("x", 513)+"\n")
			},
			wantReason: "corrupt_child_transcript",
		},
		{
			name: "session mismatch",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				writeChildTranscript(t, s, rec, []byte(`{"kind":"header","format_version":1,"session_id":"other"}`+"\n"))
			},
			wantReason: "transcript_session_mismatch",
		},
		{
			name: "corrupt transcript header shape",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				writeChildTranscript(t, s, rec, []byte(fmt.Sprintf(`{"session_id":%q}`+"\n", rec.DelegateRestore.ChildSessionID)))
			},
			wantReason: "corrupt_child_transcript",
		},
		{
			name: "busy child",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				s.subagents.track(&subagent{
					id:      rec.DelegateRestore.ChildSessionID,
					sess:    newTestSession(t),
					running: true,
					done:    make(chan struct{}),
				})
			},
			wantReason: "child_session_busy",
		},
		{
			name: "profile unavailable",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				meta, err := schema.LoadSessionMeta(s.stateDir, rec.DelegateRestore.ChildSessionID)
				if err != nil {
					t.Fatalf("load child meta: %v", err)
				}
				meta.Model = "missing/gpt-5.2"
				if err := schema.SaveSessionMeta(s.stateDir, meta); err != nil {
					t.Fatalf("save child meta: %v", err)
				}
				s.resolveProfile = func(ref string) (*provider.Profile, error) {
					return nil, fmt.Errorf("no profile for %s", ref)
				}
			},
			wantReason: "profile_unavailable",
		},
		{
			name: "descriptor profile unavailable while meta model valid",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.ResolvedProfileID = "openai"
				rec.DelegateRestore.ResolvedModel = "stale-model"
				replaceStoredDelegateRecord(t, s, rec)
				s.resolveProfile = func(ref string) (*provider.Profile, error) {
					return nil, fmt.Errorf("no profile for %s", ref)
				}
			},
			wantReason: "profile_unavailable",
		},
		{
			name: "descriptor profile id without model",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.ResolvedProfileID = "openai"
				rec.DelegateRestore.ResolvedModel = ""
				replaceStoredDelegateRecord(t, s, rec)
			},
			wantReason: "profile_unavailable",
		},
		{
			name: "descriptor model without profile id",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.ResolvedProfileID = ""
				rec.DelegateRestore.ResolvedModel = "gpt-5.2"
				replaceStoredDelegateRecord(t, s, rec)
			},
			wantReason: "profile_unavailable",
		},
		{
			name: "descriptor missing resolved profile fields",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.ResolvedProfileID = ""
				rec.DelegateRestore.ResolvedModel = ""
				replaceStoredDelegateRecord(t, s, rec)
			},
			wantReason: "profile_unavailable",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := llm.NewClient()
			c.Register(&fakeAdapter{name: "openai"})
			s := newDelegateRestorePreflightSession(t, c)
			rec := seedStoppedDelegateRestoreRecord(t, s)
			if tc.breakState != nil {
				tc.breakState(t, s, rec)
			}
			beforeEvents := len(loadJobStoreEvents(t, s.jobManager))

			raw, err := jobListTool(s, map[string]any{"type": []any{"delegate"}}, jobToolResultDefaultMaxChar)
			if err != nil {
				t.Fatalf("jobListTool: %v", err)
			}
			if got := len(loadJobStoreEvents(t, s.jobManager)); got != beforeEvents {
				t.Fatalf("jobstore event count = %d, want unchanged %d", got, beforeEvents)
			}
			var out jobListToolOutput
			if err := json.Unmarshal(handlerJSON(t, raw), &out); err != nil {
				t.Fatalf("unmarshal job_list: %v (output: %s)", err, raw)
			}
			listed := findJobListToolOutput(out.Jobs, rec.JobID)
			if listed == nil {
				t.Fatalf("job_list jobs = %+v, want %s", out.Jobs, rec.JobID)
			}
			if tc.wantReason == "" {
				if listed.Resumable == nil || !*listed.Resumable || listed.NotResumableReason != nil {
					t.Fatalf("listed job = %+v, want resumable with no reason", listed)
				}
				return
			}
			if listed.Resumable == nil || *listed.Resumable {
				t.Fatalf("listed job = %+v, want not resumable", listed)
			}
			if listed.NotResumableReason == nil || *listed.NotResumableReason != tc.wantReason {
				t.Fatalf("not_resumable_reason = %v, want %s", listed.NotResumableReason, tc.wantReason)
			}
		})
	}
}

func TestJobListStoppedDelegateResumabilityDoesNotBuildResumeHistory(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	s.delegateRestoreResumeHistory = func(entries []transcript.Entry) []schema.Turn {
		t.Errorf("job_list built resume history from %d entries", len(entries))
		return nil
	}

	raw, err := jobListTool(s, map[string]any{"type": []any{"delegate"}}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("jobListTool: %v", err)
	}
	var out jobListToolOutput
	if err := json.Unmarshal(handlerJSON(t, raw), &out); err != nil {
		t.Fatalf("unmarshal job_list: %v (output: %s)", err, raw)
	}
	listed := findJobListToolOutput(out.Jobs, rec.JobID)
	if listed == nil {
		t.Fatalf("job_list jobs = %+v, want %s", out.Jobs, rec.JobID)
	}
	if listed.Resumable == nil || !*listed.Resumable || listed.NotResumableReason != nil {
		t.Fatalf("listed job = %+v, want resumable without building history", listed)
	}
}

func TestJobListReportsDelegationAllowance(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.delegationAllowance = 2

	out, err := jobListTool(s, decodeJobListArgs(t, `{}`), 1<<20)
	if err != nil {
		t.Fatalf("jobListTool: %v", err)
	}
	var got struct {
		DelegationAllowance int `json:"delegation_allowance"`
	}
	if err := json.Unmarshal(handlerJSON(t, out), &got); err != nil {
		t.Fatalf("unmarshal job_list output: %v (out=%s)", err, out)
	}
	if got.DelegationAllowance != 2 {
		t.Fatalf("delegation_allowance = %d, want 2", got.DelegationAllowance)
	}
}

func TestJobListSurfacesRecentWatches(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	jm := s.jobManager
	rec, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	out, err := jobListTool(s, decodeJobListArgs(t, `{}`), 1<<20)
	if err != nil {
		t.Fatalf("jobListTool: %v", err)
	}
	var got struct {
		RecentWatches []struct {
			ID        string `json:"id"`
			Source    string `json:"source"`
			EndReason string `json:"end_reason"`
		} `json:"recent_watches"`
	}
	if err := json.Unmarshal(handlerJSON(t, out), &got); err != nil {
		t.Fatalf("unmarshal: %v (out=%s)", err, out)
	}
	if len(got.RecentWatches) != 1 || got.RecentWatches[0].EndReason != "cleared" || got.RecentWatches[0].Source != rec.JobID {
		t.Fatalf("recent_watches = %+v, want one cleared entry on %s", got.RecentWatches, rec.JobID)
	}
}
