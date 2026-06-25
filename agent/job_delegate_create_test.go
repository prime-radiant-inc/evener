package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"encoding/json"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/llm"
)

func TestDelegateJobEventsCarrySubagentRunLinkage(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("parent done") },
	}})
	sess := newDelegateTestSession(t, c)

	ctx := context.WithValue(context.Background(), ctxToolCallID, "call_delegate_linkage")
	ctx = context.WithValue(ctx, ctxToolItemID, "item_delegate_linkage")
	res := sess.createDelegate(ctx, delegateArgs{
		Task:           "inspect linkage",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	if res.JobID == "" || res.DelegateID == "" {
		t.Fatalf("delegate result missing ids: %+v", res)
	}

	var started []events.JobStartedData
drain:
	for {
		select {
		case ev := <-sess.Events():
			data, ok := ev.Data.(events.JobStartedData)
			if ok && data.JobType == "delegate" {
				started = append(started, data)
			}
		default:
			break drain
		}
	}
	if len(started) == 0 {
		t.Fatalf("no delegate JOB_STARTED events captured")
	}
	got := started[len(started)-1]
	if got.JobID != res.JobID || got.DelegateID != res.DelegateID || got.Task != "inspect linkage" || got.TranscriptRef == "" || got.OriginToolCallID != "call_delegate_linkage" || got.OriginItemID != "item_delegate_linkage" {
		t.Fatalf("JOB_STARTED linkage = %+v, want job/delegate/task/transcript/origin call/origin item", got)
	}
}

func TestCreateDelegateForegroundCompletesWithStructuredResult(t *testing.T) {
	t.Parallel()
	var sawSchema bool
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				sawSchema = communicateOutputSchemaHasProperty(req, "summary")
				return communicateWithStructured("delegate prose", map[string]any{
					"message": "delegate prose",
					"summary": "structured summary",
					"count":   2,
				})
			},
		},
	})
	sess := newDelegateTestSession(t, c)

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "summarize the work",
		Background:     false,
		BlockTimeoutMS: 5000,
		ResultSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string"},
				"summary": map[string]any{"type": "string"},
				"count":   map[string]any{"type": "number"},
			},
			"required": []string{"message", "summary"},
		},
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	if res.JobID == "" {
		t.Fatal("job_id is empty")
	}
	if res.Type != string(jobstore.JobDelegate) || res.Status != jobstore.StatusCompleted {
		t.Fatalf("result = %+v, want completed delegate", res)
	}
	if !strings.HasPrefix(res.TranscriptRef, "local:") {
		t.Fatalf("transcript_ref = %q, want local ref", res.TranscriptRef)
	}
	if !strings.Contains(res.Output, "delegate prose") {
		t.Fatalf("output = %q, want prose result", res.Output)
	}
	if !res.StructuredResultValid {
		t.Fatal("structured_result_valid = false, want true")
	}
	structured, ok := res.StructuredResult.(map[string]any)
	if !ok {
		t.Fatalf("structured_result has type %T, want map", res.StructuredResult)
	}
	if structured["summary"] != "structured summary" {
		t.Fatalf("structured_result = %+v, want summary", structured)
	}
	if !sawSchema {
		t.Fatal("child communicate tool did not receive delegate result schema")
	}

	jobs := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(jobs) != 1 {
		t.Fatalf("jobs = %+v, want one delegate job", jobs)
	}
	if jobs[0].JobID != res.JobID || jobs[0].Status != jobstore.StatusCompleted {
		t.Fatalf("job record = %+v, want completed job %s", jobs[0], res.JobID)
	}
}

func TestDelegateResultIncludesDurableDelegateAndStartedJobIDs(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("done") },
	}})
	s := newDelegateTestSession(t, c)

	res := s.createDelegate(context.Background(), delegateArgs{
		Task:           "finish",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	if !strings.HasPrefix(res.DelegateID, "dlg_") {
		t.Fatalf("DelegateID = %q, want dlg_ prefix", res.DelegateID)
	}
	if res.StartedJobID != res.JobID || res.LatestJobID != res.JobID {
		t.Fatalf("result ids = %+v, want started/latest equal concrete job", res)
	}
	rec := loadShellRecord(t, s.jobManager, res.JobID)
	if rec.DelegateID != res.DelegateID {
		t.Fatalf("record DelegateID = %q, want %q", rec.DelegateID, res.DelegateID)
	}
	delegates, err := s.jobManager.store.LoadDelegates()
	if err != nil {
		t.Fatalf("LoadDelegates: %v", err)
	}
	if delegates[res.DelegateID] == nil || delegates[res.DelegateID].LatestJobID != res.JobID {
		t.Fatalf("delegates = %+v, want latest job linked", delegates)
	}
}

func TestDelegateReadyResultSurfacesWatching(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			if !requestHasTool(req, "job_watch") {
				t.Fatalf("watch_parent child request missing job_watch")
			}
			return toolCallResponse(llm.ToolCallData{
				ID:   "create_parent_watch",
				Name: "job_watch",
				Arguments: json.RawMessage(`{
					"operation":"create",
					"source":"parent",
					"events":["assistant.tool"],
					"event_filter":{"tool_name":"read_file","status":"ok"}
				}`),
				Type: "function",
			})
		},
		func(req llm.Request) llm.Response {
			return communicateWithDefaultOutput("OBSERVER_READY")
		},
	}})
	s := newDelegateTestSession(t, c)

	res := s.createDelegate(context.Background(), delegateArgs{
		Task:           "install parent watch and report readiness",
		WatchParent:    true,
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	if res.Status != jobstore.StatusCompleted || res.RunningInBackground {
		t.Fatalf("delegate result = %+v, want completed foreground readiness activation", res)
	}
	if !res.Watching {
		t.Fatalf("watching = false, watches = %+v", res.Watches)
	}
	if len(res.Watches) != 1 {
		t.Fatalf("watches = %+v, want one parent watch", res.Watches)
	}
	watch := res.Watches[0]
	if watch.ID == "" || watch.Source != "parent" || watch.Deliveries != 0 {
		t.Fatalf("active watch = %+v, want undelivered parent watch", watch)
	}
	if !strings.Contains(watch.Condition, "read_file") {
		t.Fatalf("active watch condition = %q, want read_file signal", watch.Condition)
	}

	wire, err := marshalDelegateResult(res, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("marshalDelegateResult: %v", err)
	}
	var parsed delegateToolResult
	if err := json.Unmarshal(handlerJSON(t, wire), &parsed); err != nil {
		t.Fatalf("unmarshal delegate result: %v\n%s", err, wire)
	}
	if !parsed.Watching || len(parsed.Watches) != 1 {
		t.Fatalf("wire result = %+v, want active watch callback signal", parsed)
	}
}

func TestForegroundDelegateCompletionDoesNotArmTerminalNotification(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("done") },
	}})
	s := newDelegateTestSession(t, c)

	res := s.createDelegate(context.Background(), delegateArgs{
		Task:           "finish",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	if res.Status != jobstore.StatusCompleted {
		t.Fatalf("status = %q, want completed", res.Status)
	}

	rec := loadShellRecord(t, s.jobManager, res.JobID)
	if rec.NotifyState != jobstore.NotifyNotArmed {
		t.Fatalf("foreground delegate NotifyState = %q, want %q", rec.NotifyState, jobstore.NotifyNotArmed)
	}
}

func TestCreateDelegateEmptyResultSchemaIsNoSchema(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return finalResponse("plain result")
			},
		},
	})
	sess := newDelegateTestSession(t, c)

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "plain delegate",
		Background:     false,
		BlockTimeoutMS: 5000,
		ResultSchema:   map[string]any{},
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	if res.StructuredResultValidSet {
		t.Fatalf("structured_result_valid_set = true, want false for empty result_schema")
	}
	rec := findListedJob(sess.jobManager.list(listFilter{}), res.JobID)
	if rec == nil {
		t.Fatalf("job %q not found", res.JobID)
	}
	if rec.DelegateRestore != nil && rec.DelegateRestore.ResultSchema != nil {
		t.Fatalf("durable result_schema = %#v, want nil", rec.DelegateRestore.ResultSchema)
	}
	if rec.StructuredResultValid != nil || rec.StructuredResultReason != "" {
		t.Fatalf("durable structured result fields = valid:%v reason:%q, want unset", rec.StructuredResultValid, rec.StructuredResultReason)
	}
}

// TestDelegateRejectsAllowanceGEOwn pins spec §1: the grant rule. A session may
// grant a child a delegation_allowance strictly less than its own. A session
// with allowance 2 may grant 1 (succeeds) but not 2 (rejected with the exact
// invalid_request message naming its own allowance).
func TestDelegateRejectsAllowanceGEOwn(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return finalResponse("granted child result")
			},
		},
	})
	sess := newDelegateTestSession(t, c)
	sess.mu.Lock()
	sess.delegationAllowance = 2
	sess.mu.Unlock()

	// Granting >= own allowance is rejected with the exact message.
	rejected := sess.createDelegate(context.Background(), delegateArgs{
		Task:                "over-grant",
		DelegationAllowance: 2,
	})
	if rejected.Err == nil {
		t.Fatalf("delegate(delegation_allowance=2) with own allowance 2 should be rejected, got result %+v", rejected)
	}
	const wantMsg = "invalid_request: delegation_allowance must be less than your own allowance (2); valid grants: 0..1"
	if rejected.Err.Error() != wantMsg {
		t.Fatalf("rejection message = %q, want %q", rejected.Err.Error(), wantMsg)
	}

	// Granting strictly less than own allowance succeeds.
	ok := sess.createDelegate(context.Background(), delegateArgs{
		Task:                "grant one",
		DelegationAllowance: 1,
		Background:          false,
		BlockTimeoutMS:      5000,
	})
	if ok.Err != nil {
		t.Fatalf("delegate(delegation_allowance=1) with own allowance 2 should succeed, got error: %v", ok.Err)
	}
	if ok.JobID == "" {
		t.Fatalf("granted delegate has empty job_id: %+v", ok)
	}
}

func TestCreateDelegateBackgroundReturnsRunningJob(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	var releaseOnce sync.Once
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				<-release
				return communicateWithStructured("background complete", map[string]any{
					"message": "background complete",
				})
			},
		},
	})
	sess := newDelegateTestSession(t, c)

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "run in the background",
		Background: true,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	if res.JobID == "" || res.TranscriptRef == "" {
		t.Fatalf("result = %+v, want job_id and transcript_ref", res)
	}
	if res.Type != string(jobstore.JobDelegate) ||
		res.Status != jobstore.StatusRunning ||
		!res.RunningInBackground ||
		res.TimedOut {
		t.Fatalf("result = %+v, want running background delegate", res)
	}

	_, _ = sess.jobManager.stop(res.JobID)
	releaseOnce.Do(func() { close(release) })
	waitForShellDone(t, sess.jobManager, res.JobID)
}

func TestCreateDelegateStartupDoesNotLeaveUnreturnedRunningJob(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess := newDelegateTestSession(t, c)

	var sawDelegateStart atomic.Bool
	origAppend := sess.jobManager.appendEvent
	origAppendEvents := sess.jobManager.appendEvents
	sess.jobManager.appendEvent = func(e jobstore.Event) error {
		if sawDelegateStart.Load() && e.Kind == jobstore.EventJobSessionAssigned {
			return errors.New("redundant assignment append must not be required")
		}
		return origAppend(e)
	}
	sess.jobManager.appendEvents = func(events []jobstore.Event) error {
		for _, event := range events {
			if event.Kind == jobstore.EventJobStarted && event.Type == jobstore.JobDelegate {
				sawDelegateStart.Store(true)
			}
		}
		return origAppendEvents(events)
	}

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "fail after delegate start",
		Background: true,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	if res.JobID == "" {
		t.Fatal("successful startup returned empty job_id")
	}
	if !sawDelegateStart.Load() {
		t.Fatal("delegate job_started append was not attempted")
	}

	jobs := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(jobs) != 1 {
		t.Fatalf("delegate jobs = %+v, want one returned live job", jobs)
	}
	if jobs[0].JobID != res.JobID || jobs[0].Status != jobstore.StatusRunning || jobs[0].TranscriptRef == "" {
		t.Fatalf("durable delegate = %+v, result = %+v; want same running job with transcript_ref", jobs[0], res)
	}

	sess.jobManager.appendEvent = origAppend
	sess.jobManager.appendEvents = origAppendEvents
	_, _ = sess.jobManager.stop(res.JobID)
	waitForShellDone(t, sess.jobManager, res.JobID)
}

func TestCreateDelegateStartupBatchFailureDoesNotLeavePhantomDelegate(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess := newDelegateTestSession(t, c)

	appendErr := errors.New("delegate startup batch failed")
	origAppendEvents := sess.jobManager.appendEvents
	sess.jobManager.appendEvents = func(events []jobstore.Event) error {
		for _, event := range events {
			if event.Kind == jobstore.EventJobStarted && event.Type == jobstore.JobDelegate {
				return appendErr
			}
		}
		return origAppendEvents(events)
	}
	defer func() { sess.jobManager.appendEvents = origAppendEvents }()

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "fail before durable start",
		Background: true,
	})
	if !errors.Is(res.Err, appendErr) {
		t.Fatalf("createDelegate err = %v, want %v", res.Err, appendErr)
	}
	delegates, err := sess.jobManager.store.LoadDelegates()
	if err != nil {
		t.Fatalf("LoadDelegates: %v", err)
	}
	if len(delegates) != 0 {
		t.Fatalf("delegates = %+v, want none after failed startup batch", delegates)
	}
	jobs := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(jobs) != 0 {
		t.Fatalf("delegate jobs = %+v, want none after failed startup batch", jobs)
	}
}

func TestCreateDelegateDescriptorDurableBeforeFirstModelRequest(t *testing.T) {
	t.Parallel()
	var (
		sess                 *Session
		recAtFirstModel      *jobstore.JobRecord
		captureAtFirstModel  error
		firstModelRequest    = make(chan struct{})
		releaseModelResponse = make(chan struct{})
		releaseOnce          sync.Once
	)
	defer releaseOnce.Do(func() { close(releaseModelResponse) })

	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				recs, err := sess.jobManager.store.Load()
				if err != nil {
					captureAtFirstModel = err
				}
				for _, rec := range recs {
					if rec.Type == jobstore.JobDelegate {
						recAtFirstModel = rec
						break
					}
				}
				close(firstModelRequest)
				<-releaseModelResponse
				return communicateWithDefaultOutput("descriptor ready")
			},
		},
	})
	workDir := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(workDir)
	env.EnvPolicy = execenv.EnvPolicyCoreOnly
	stateDir := t.TempDir()
	var err error
	sess, err = NewSession(c, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	sess.pluginAgents = map[string]plugin.Agent{
		"reviewer": {
			Name:         "reviewer",
			Description:  "Reviews work",
			Model:        "inherit",
			Tools:        []string{"read_file"},
			SystemPrompt: "Review carefully.",
			PluginName:   "test-plugin",
		},
	}

	origAppendEvents := sess.jobManager.appendEvents
	sess.jobManager.appendEvents = func(events []jobstore.Event) error {
		for _, event := range events {
			if event.Kind == jobstore.EventJobStarted && event.Type == jobstore.JobDelegate {
				time.Sleep(250 * time.Millisecond)
				break
			}
		}
		return origAppendEvents(events)
	}
	t.Cleanup(func() { sess.jobManager.appendEvents = origAppendEvents })

	resultSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message": map[string]any{"type": "string"},
		},
		"required": []string{"message"},
	}
	done := make(chan delegateResult, 1)
	ctx := context.WithValue(context.Background(), ctxToolCallID, "call_delegate_descriptor")
	go func() {
		done <- sess.createDelegate(ctx, delegateArgs{
			Task:            "inspect the patch",
			AgentType:       "reviewer",
			Model:           "gpt-5.3",
			ReasoningEffort: "high",
			Background:      true,
			WatchParent:     true,
			ResultSchema:    resultSchema,
		})
	}()

	select {
	case <-firstModelRequest:
	case <-time.After(2 * time.Second):
		t.Fatal("child model request did not start")
	}
	if captureAtFirstModel != nil {
		t.Fatalf("load jobs at first model request: %v", captureAtFirstModel)
	}
	if recAtFirstModel == nil {
		t.Fatal("delegate job record was not durable before first child model request")
	}
	desc := recAtFirstModel.DelegateRestore
	if desc == nil {
		t.Fatalf("delegate restore descriptor was not durable before first child model request: %+v", recAtFirstModel)
	}
	if desc.Version != 1 {
		t.Fatalf("descriptor version = %d, want 1", desc.Version)
	}
	if recAtFirstModel.TranscriptRef == "" || desc.TranscriptRef != recAtFirstModel.TranscriptRef {
		t.Fatalf("descriptor transcript_ref = %q, record transcript_ref = %q", desc.TranscriptRef, recAtFirstModel.TranscriptRef)
	}
	_, childID, err := decodeRef(recAtFirstModel.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	if desc.ChildSessionID != childID {
		t.Fatalf("descriptor child_session_id = %q, want %q", desc.ChildSessionID, childID)
	}
	if desc.ParentSessionID != sess.ID() || desc.ParentJobID != recAtFirstModel.JobID {
		t.Fatalf("descriptor parent ids = (%q, %q), want (%q, %q)", desc.ParentSessionID, desc.ParentJobID, sess.ID(), recAtFirstModel.JobID)
	}
	if desc.OwnerSessionID != sess.ID() || desc.VisibleSessionID != sess.ID() {
		t.Fatalf("descriptor owner/visible = (%q, %q), want parent session %q", desc.OwnerSessionID, desc.VisibleSessionID, sess.ID())
	}
	if desc.OriginToolCallID != "call_delegate_descriptor" || desc.OriginTurnID != "" {
		t.Fatalf("descriptor origin ids = (%q, %q), want tool call only", desc.OriginTurnID, desc.OriginToolCallID)
	}
	if desc.Task != "inspect the patch" || desc.AgentType != "reviewer" || desc.RequestedModel != "gpt-5.3" {
		t.Fatalf("descriptor launch fields = task %q agent_type %q requested_model %q", desc.Task, desc.AgentType, desc.RequestedModel)
	}
	if desc.ResolvedProfileID != "openai" || desc.ResolvedModel != "gpt-5.3" {
		t.Fatalf("descriptor resolved profile/model = %q/%q, want openai/gpt-5.3", desc.ResolvedProfileID, desc.ResolvedModel)
	}
	if desc.ReasoningEffort != "high" || desc.AgentName != "reviewer" {
		t.Fatalf("descriptor reasoning/agent_name = %q/%q, want high/reviewer", desc.ReasoningEffort, desc.AgentName)
	}
	if desc.FrozenRolePrompt != "Review carefully." {
		t.Fatalf("descriptor frozen role prompt = %q", desc.FrozenRolePrompt)
	}
	if !hasString(desc.FrozenToolNames, "read_file") || !hasString(desc.FrozenToolNames, "task_list") || !hasString(desc.FrozenToolNames, "job_watch") {
		t.Fatalf("descriptor frozen tool names = %+v, want read_file, task_list, and job_watch", desc.FrozenToolNames)
	}
	if desc.ParentWatchGranted != true {
		t.Fatalf("descriptor parent_watch_granted = %t, want true", desc.ParentWatchGranted)
	}
	if hasString(desc.FrozenToolNames, "delegate") {
		t.Fatalf("descriptor frozen tool names = %+v, must not grant delegate for watch_parent leaf", desc.FrozenToolNames)
	}
	if desc.WorkingDir != workDir || desc.LocalEnvPolicy != "core_only" {
		t.Fatalf("descriptor env = %q/%q, want %q/core_only", desc.WorkingDir, desc.LocalEnvPolicy, workDir)
	}
	schema, ok := desc.ResultSchema.(map[string]any)
	if !ok || schema["type"] != "object" {
		t.Fatalf("descriptor result_schema = %#v, want object schema", desc.ResultSchema)
	}
	if len(desc.ExplicitToolGrants) != 0 || len(desc.FrozenSkillNames) != 0 || len(desc.FrozenSkillBodies) != 0 || desc.FrozenTaskPrompt != "" {
		t.Fatalf("descriptor optional fields must be empty when omitted: grants=%v skills=%v bodies=%v task_prompt=%q", desc.ExplicitToolGrants, desc.FrozenSkillNames, desc.FrozenSkillBodies, desc.FrozenTaskPrompt)
	}

	releaseOnce.Do(func() { close(releaseModelResponse) })
	var res delegateResult
	select {
	case res = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("createDelegate did not return")
	}
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	_, _ = sess.jobManager.stop(res.JobID)
	waitForShellDone(t, sess.jobManager, res.JobID)

	reopened, err := newJobManager(stateDir, sess.ID(), func(jobNotification) {})
	if err != nil {
		t.Fatalf("reopen job manager: %v", err)
	}
	t.Cleanup(func() { _ = reopened.store.Close() })
	reopenedRec := loadShellRecord(t, reopened, res.JobID)
	if reopenedRec.DelegateRestore == nil || reopenedRec.DelegateRestore.ChildSessionID != childID {
		t.Fatalf("reopened descriptor = %+v, want child %q", reopenedRec.DelegateRestore, childID)
	}
	if reopenedRec.Resumable == nil || !*reopenedRec.Resumable || reopenedRec.NotResumableWhy != "" {
		t.Fatalf("reopened resumability = %v/%q, want resumable terminal delegate", reopenedRec.Resumable, reopenedRec.NotResumableWhy)
	}
}

func TestCreateDelegateDescriptorOmitsOptionalLaunchDefaults(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	var releaseOnce sync.Once
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				<-release
				return communicateWithDefaultOutput("done")
			},
		},
	})
	sess := newDelegateTestSession(t, c)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "use defaults",
		Background: true,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	rec := loadShellRecord(t, sess.jobManager, res.JobID)
	if rec.DelegateRestore == nil {
		t.Fatalf("missing delegate descriptor: %+v", rec)
	}
	desc := rec.DelegateRestore
	if desc.AgentType != "" || desc.RequestedModel != "" || desc.ReasoningEffort != "" {
		t.Fatalf("optional launch settings = agent_type %q requested_model %q reasoning %q, want empty", desc.AgentType, desc.RequestedModel, desc.ReasoningEffort)
	}
	if len(desc.ExplicitToolGrants) != 0 || len(desc.FrozenSkillNames) != 0 || len(desc.FrozenSkillBodies) != 0 || desc.FrozenTaskPrompt != "" {
		t.Fatalf("optional descriptor fields = grants %v skills %v bodies %v task_prompt %q, want empty", desc.ExplicitToolGrants, desc.FrozenSkillNames, desc.FrozenSkillBodies, desc.FrozenTaskPrompt)
	}

	releaseOnce.Do(func() { close(release) })
	waitForShellDone(t, sess.jobManager, res.JobID)
}

func TestCreateDelegateForegroundTimeoutLeavesChildRunning(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	var releaseOnce sync.Once
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				<-release
				return communicateWithStructured("timeout child complete", map[string]any{
					"message": "timeout child complete",
				})
			},
		},
	})
	sess := newDelegateTestSession(t, c)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "wait past foreground timeout",
		Background:     false,
		BlockTimeoutMS: 1000,
		ResultSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string"},
			},
			"required": []string{"message"},
		},
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	if res.Reason != "foreground_timeout" || !res.RunningInBackground || !res.TimedOut {
		t.Fatalf("result = %+v, want foreground_timeout/background/timed_out", res)
	}
	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := sess.subagents.get(childID)
	if sub == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	sub.mu.Lock()
	cancelled := sub.cancelRequested
	running := sub.running
	sub.mu.Unlock()
	if cancelled || !running {
		t.Fatalf("child cancelled=%v running=%v, want not cancelled and running", cancelled, running)
	}

	releaseOnce.Do(func() { close(release) })
	waitForShellDone(t, sess.jobManager, res.JobID)
	rec := loadShellRecord(t, sess.jobManager, res.JobID)
	if rec.Status != jobstore.StatusCompleted {
		t.Fatalf("record after releasing child = %+v, want completed", rec)
	}
}

func TestDelegateStopMapsToCancelled(t *testing.T) {
	t.Parallel()
	adapter := &cancelAwareDelegateAdapter{name: "openai", started: make(chan struct{})}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "run until stopped",
		Background: true,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	select {
	case <-adapter.started:
	case <-time.After(2 * time.Second):
		t.Fatal("delegate child did not start")
	}

	out, err := jobStopTool(context.Background(), sess, map[string]any{
		"job_id":      res.JobID,
		"max_wait_ms": 1000,
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("jobStopTool: %v", err)
	}
	var stop jobStopResult
	if err := json.Unmarshal(handlerJSON(t, out), &stop); err != nil {
		t.Fatalf("unmarshal job_stop output: %v (output: %s)", err, out)
	}
	if stop.JobID != res.JobID || stop.Status != string(jobstore.StatusCancelled) || stop.Reason == nil || *stop.Reason != "stopped_by_parent" {
		t.Fatalf("job_stop = %+v, want cancelled/stopped_by_parent", stop)
	}
	waitForShellDone(t, sess.jobManager, res.JobID)
	rec := loadShellRecord(t, sess.jobManager, res.JobID)
	if rec.Status != jobstore.StatusCancelled || rec.Reason != "stopped_by_parent" {
		t.Fatalf("record = %+v, want cancelled/stopped_by_parent", rec)
	}
	sub := sess.subagents.get(childID)
	if sub == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	sub.mu.Lock()
	cancelRequested := sub.cancelRequested
	status := sub.status
	sub.mu.Unlock()
	if !cancelRequested || status != SubagentCancelled {
		t.Fatalf("child cancelRequested=%v status=%q, want cancelRequested=true status=%q", cancelRequested, status, SubagentCancelled)
	}
}

func TestCreateDelegateSignalCancelsChildAfterSubagentDrain(t *testing.T) {
	t.Parallel()
	adapter := &cancelAwareDelegateAdapter{name: "openai", started: make(chan struct{})}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "run until signaled after drain",
		Background: true,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	select {
	case <-adapter.started:
	case <-time.After(2 * time.Second):
		t.Fatal("delegate child did not start")
	}
	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := sess.subagents.get(childID)
	if sub == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	drained := sess.subagents.drainForClose()
	t.Cleanup(func() {
		for _, drainedSub := range drained {
			drainedSub.sess.close(false)
		}
	})
	if got := sess.subagents.get(childID); got != nil {
		t.Fatalf("subagent %s still tracked after drain", childID)
	}

	run := runningDelegateJob(t, sess.jobManager, res.JobID)
	run.signal()

	sub.mu.Lock()
	cancelRequested := sub.cancelRequested
	sub.mu.Unlock()
	if !cancelRequested {
		t.Fatal("delegate signal did not mark drained child cancelRequested")
	}

	waitForShellDone(t, sess.jobManager, res.JobID)
	rec := loadShellRecord(t, sess.jobManager, res.JobID)
	if rec.Status != jobstore.StatusCancelled || rec.Reason != "stopped_by_parent" {
		t.Fatalf("record = %+v, want cancelled/stopped_by_parent after drained-map signal", rec)
	}
}

func TestCreateDelegateDurableRecordKeepsOutputPathAndTranscriptRef(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithStructured("durable delegate complete", map[string]any{
					"message": "durable delegate complete",
				})
			},
		},
	})
	sess := newDelegateTestSession(t, c)

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "write durable delegate metadata",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	sess.jobManager.mu.Lock()
	run := sess.jobManager.running[res.JobID]
	sess.jobManager.mu.Unlock()
	if run != nil {
		t.Fatalf("job %s still running after foreground completion", res.JobID)
	}
	rec := loadShellRecord(t, sess.jobManager, res.JobID)
	if rec.OutputPath == "" {
		t.Fatalf("durable record missing output_path: %+v", rec)
	}
	if rec.TranscriptRef != res.TranscriptRef {
		t.Fatalf("transcript_ref = %q, want %q", rec.TranscriptRef, res.TranscriptRef)
	}
}

func TestCreateDelegateDurableRecordKeepsStructuredResult(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithStructured("durable structured delegate complete", map[string]any{
					"summary": "durable",
				})
			},
		},
	})
	sess := newDelegateTestSession(t, c)

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "persist structured delegate result",
		Background:     true,
		BlockTimeoutMS: 5000,
		ResultSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary": map[string]any{"type": "string"},
			},
			"required": []string{"summary"},
		},
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	waitForShellDone(t, sess.jobManager, res.JobID)
	rec := loadShellRecord(t, sess.jobManager, res.JobID)
	structured, ok := rec.StructuredResult.(map[string]any)
	if !ok || structured["summary"] != "durable" {
		t.Fatalf("durable structured result = %+v, want summary=durable", rec.StructuredResult)
	}
	if rec.StructuredResultValid == nil || !*rec.StructuredResultValid {
		t.Fatalf("structured_result_valid = %v, want true", rec.StructuredResultValid)
	}
	output, _, _, err := sess.jobManager.readOutput(res.JobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("read delegate output: %v", err)
	}
	if !strings.Contains(output, "durable structured delegate complete") {
		t.Fatalf("delegate output = %q, want prose copied to job output", output)
	}
}

func TestCreateDelegateDropsOversizedStructuredResultBeforePersistence(t *testing.T) {
	t.Parallel()
	large := strings.Repeat("x", maxPersistedStructuredResultJSONBytes+1)
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithStructured("oversized structured delegate complete", map[string]any{
					"payload": large,
				})
			},
		},
	})
	sess := newDelegateTestSession(t, c)

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "persist oversized structured delegate result",
		Background:     true,
		BlockTimeoutMS: 5000,
		ResultSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"payload": map[string]any{"type": "string"},
			},
			"required": []string{"payload"},
		},
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	waitForShellDone(t, sess.jobManager, res.JobID)

	reopened, err := jobstore.Open(sess.jobManager.dir + "/jobs.jsonl")
	if err != nil {
		t.Fatalf("reopen job store: %v", err)
	}
	defer reopened.Close()
	recs, err := reopened.Load()
	if err != nil {
		t.Fatalf("load reopened job store: %v", err)
	}
	rec := recs[res.JobID]
	if rec == nil {
		t.Fatalf("job %s missing after reopen", res.JobID)
	}
	if rec.StructuredResult != nil {
		t.Fatalf("structured_result persisted oversized value: %T", rec.StructuredResult)
	}
	if rec.StructuredResultValid == nil || *rec.StructuredResultValid {
		t.Fatalf("structured_result_valid = %v, want false", rec.StructuredResultValid)
	}
	if rec.StructuredResultReason != "schema_result_too_large" {
		t.Fatalf("structured_result_reason = %q, want schema_result_too_large", rec.StructuredResultReason)
	}
}

func TestCreateDelegateMarksChildConsumedAfterDurableFinish(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("consume child result")
			},
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("resume still works")
			},
		},
	})
	sess := newDelegateTestSession(t, c)

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "consume retained child after durable ownership",
		Background: true,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	waitForShellDone(t, sess.jobManager, res.JobID)
	_, childID, err := decodeRef(res.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := sess.subagents.get(childID)
	if sub == nil {
		t.Fatalf("subagent %s not retained", childID)
	}
	sub.mu.Lock()
	consumed := sub.resultConsumed
	closed := sub.closed
	sub.mu.Unlock()
	if !consumed || closed {
		t.Fatalf("child consumed=%v closed=%v, want consumed and retained open", consumed, closed)
	}

	resume := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:        res.DelegateID,
		Message:       "resume after consumption",
		OnIdle:        "start",
		Background:    false,
		BackgroundSet: true,
	})
	if resume.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", resume.Err)
	}
	if resume.Status != jobstore.StatusCompleted || !strings.Contains(resume.Output, "resume still works") {
		t.Fatalf("resume result = %+v, want completed resumed delegate", resume)
	}
}

func TestDelegateNotificationCarriesTranscriptRef(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithStructured("notification delegate complete", map[string]any{
					"message": "notification delegate complete",
				})
			},
		},
	})
	sess := newDelegateTestSession(t, c)

	var mu sync.Mutex
	var queued []jobNotification
	sess.jobManager.enqueue = func(n jobNotification) {
		mu.Lock()
		defer mu.Unlock()
		queued = append(queued, n)
	}

	res := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "finish and notify",
		Background: true,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate returned error: %v", res.Err)
	}
	if res.JobID == "" || res.TranscriptRef == "" {
		t.Fatalf("result = %+v, want job_id and transcript_ref", res)
	}

	var got []jobNotification
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got = append([]jobNotification(nil), queued...)
		mu.Unlock()
		if len(got) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(got) != 1 {
		t.Fatalf("queued notifications = %+v, want exactly one", got)
	}

	rec := loadShellRecord(t, sess.jobManager, res.JobID)
	n := got[0]
	if n.JobID != res.JobID {
		t.Fatalf("notification job_id = %q, want %q", n.JobID, res.JobID)
	}
	if n.JobType != string(jobstore.JobDelegate) {
		t.Fatalf("notification job_type = %q, want %q", n.JobType, jobstore.JobDelegate)
	}
	if n.TranscriptRef == "" || n.TranscriptRef != res.TranscriptRef || n.TranscriptRef != rec.TranscriptRef {
		t.Fatalf("notification transcript_ref = %q, result = %q, record = %q", n.TranscriptRef, res.TranscriptRef, rec.TranscriptRef)
	}
}

func TestDelegateGrantErrorEnumeratesValidRange(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ownAllowance int
		wantContains string
	}{
		{ownAllowance: 1, wantContains: "valid grants: 0"},
		{ownAllowance: 3, wantContains: "valid grants: 0..2"},
	}
	for _, tc := range cases {
		s := newTestSession(t)
		s.delegationAllowance = tc.ownAllowance
		res := s.createDelegate(context.Background(), delegateArgs{
			Task:                "do a thing",
			DelegationAllowance: tc.ownAllowance, // >= own => rejected
		})
		if res.Err == nil {
			t.Fatalf("own=%d: expected grant error, got nil", tc.ownAllowance)
		}
		if !strings.Contains(res.Err.Error(), tc.wantContains) {
			t.Fatalf("own=%d: error %q missing %q", tc.ownAllowance, res.Err.Error(), tc.wantContains)
		}
	}
}
