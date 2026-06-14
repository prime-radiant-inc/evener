package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/skill"
	taskpkg "primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

func TestCreateDelegateForegroundCompletesWithStructuredResult(t *testing.T) {
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

func TestCreateDelegateEmptyResultSchemaIsNoSchema(t *testing.T) {
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
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess := newDelegateTestSession(t, c)

	var sawDelegateStart atomic.Bool
	origAppend := sess.jobManager.appendEvent
	sess.jobManager.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobStarted && e.Type == jobstore.JobDelegate {
			sawDelegateStart.Store(true)
			return origAppend(e)
		}
		if sawDelegateStart.Load() && e.Kind == jobstore.EventJobSessionAssigned {
			return errors.New("redundant assignment append must not be required")
		}
		return origAppend(e)
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
	_, _ = sess.jobManager.stop(res.JobID)
	waitForShellDone(t, sess.jobManager, res.JobID)
}

func TestCreateDelegateDescriptorDurableBeforeFirstModelRequest(t *testing.T) {
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

	origAppend := sess.jobManager.appendEvent
	sess.jobManager.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobStarted && e.Type == jobstore.JobDelegate {
			time.Sleep(250 * time.Millisecond)
		}
		return origAppend(e)
	}

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
	if !hasString(desc.FrozenToolNames, "read_file") || !hasString(desc.FrozenToolNames, "task_list") {
		t.Fatalf("descriptor frozen tool names = %+v, want read_file and task_list", desc.FrozenToolNames)
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
	if err := json.Unmarshal([]byte(out), &stop); err != nil {
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

func TestFinalizeDelegatePersistsSchemaValidationFailedReason(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	child.mu.Lock()
	child.comm.structured = map[string]any{"count": "not a number"}
	child.mu.Unlock()
	sub := completedDelegateSubagent(child, "validation failed")
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJobWithID(parent.jobManager, child.ID(), "validate structured result", sub, jobstore.NewJobID(), map[string]any{
		"type": "object",
		"properties": map[string]any{
			"count": map[string]any{"type": "number"},
		},
		"required": []string{"count"},
	}, false)
	if err != nil {
		t.Fatalf("attachDelegateJobWithID: %v", err)
	}

	if err := parent.finalizeDelegate(run.rec.JobID, child.ID(), sub); err != nil {
		t.Fatalf("finalizeDelegate: %v", err)
	}
	waitForShellDone(t, parent.jobManager, run.rec.JobID)
	rec := loadShellRecord(t, parent.jobManager, run.rec.JobID)
	if rec.StructuredResult != nil {
		t.Fatalf("structured_result persisted invalid value: %T", rec.StructuredResult)
	}
	if rec.StructuredResultValid == nil || *rec.StructuredResultValid {
		t.Fatalf("structured_result_valid = %v, want false", rec.StructuredResultValid)
	}
	if rec.StructuredResultReason != "schema_validation_failed" {
		t.Fatalf("structured_result_reason = %q, want schema_validation_failed", rec.StructuredResultReason)
	}
}

func TestCreateDelegateMarksChildConsumedAfterDurableFinish(t *testing.T) {
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
		Target:        res.JobID,
		Message:       "resume after consumption",
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

func TestCreateDelegateForegroundFinalizeFailureRetriesUntilDurable(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("finalize failure child complete")
			},
		},
	})
	sess := newDelegateTestSession(t, c)
	var finishAttempts atomic.Int32
	origAppend := sess.jobManager.appendEvent
	defer func() { sess.jobManager.appendEvent = origAppend }()
	sess.jobManager.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobFinished && finishAttempts.Add(1) <= 2 {
			return errors.New("append failed")
		}
		return origAppend(e)
	}

	done := make(chan delegateResult, 1)
	go func() {
		done <- sess.createDelegate(context.Background(), delegateArgs{
			Task:           "finish while append fails",
			Background:     false,
			BlockTimeoutMS: 5000,
		})
	}()

	var res delegateResult
	select {
	case res = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("createDelegate did not retry finalization append failure")
	}
	if res.Err != nil {
		t.Fatalf("createDelegate returned error after retry: %v", res.Err)
	}
	if res.Status != jobstore.StatusCompleted {
		t.Fatalf("result = %+v, want completed", res)
	}
	if finishAttempts.Load() < 3 {
		t.Fatalf("job_finished attempts = %d, want retry until success", finishAttempts.Load())
	}
	waitForShellDone(t, sess.jobManager, res.JobID)
}

func TestFinalizeDelegateRetryAfterDurableFailureDoesNotDuplicateOutput(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := &subagent{
		id:      child.ID(),
		sess:    child,
		running: false,
		status:  SubagentCompleted,
		result:  "retry complete",
		done:    make(chan struct{}),
	}
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "retry terminal", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}

	appendErr := errors.New("job_finished failed")
	var finishAttempts atomic.Int32
	origAppend := parent.jobManager.appendEvent
	parent.jobManager.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobFinished {
			if finishAttempts.Add(1) == 1 {
				return appendErr
			}
		}
		return origAppend(e)
	}
	err = parent.finalizeDelegate(run.rec.JobID, child.ID(), sub)
	if err != nil {
		t.Fatalf("finalizeDelegate after retry: %v", err)
	}
	waitForShellDone(t, parent.jobManager, run.rec.JobID)

	output, _, _, err := parent.jobManager.readOutput(run.rec.JobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if got := strings.Count(output, "retry complete"); got != 1 {
		t.Fatalf("output contains delegate result %d times, want 1: %q", got, output)
	}
	rec := loadShellRecord(t, parent.jobManager, run.rec.JobID)
	if rec.Status != jobstore.StatusCompleted {
		t.Fatalf("record status = %q, want completed", rec.Status)
	}
}

func TestFinalizeDelegateRetriesJobFinishedAppendUntilDurable(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	child.mu.Lock()
	child.comm.structured = map[string]any{"summary": "retry structured"}
	child.mu.Unlock()
	sub := completedDelegateSubagent(child, "retry finished append")
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "retry terminal", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}

	var finishAttempts atomic.Int32
	origAppend := parent.jobManager.appendEvent
	parent.jobManager.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobFinished && finishAttempts.Add(1) <= 2 {
			return errors.New("job_finished failed")
		}
		return origAppend(e)
	}

	if err := parent.finalizeDelegate(run.rec.JobID, child.ID(), sub); err != nil {
		t.Fatalf("finalizeDelegate: %v", err)
	}
	if finishAttempts.Load() < 3 {
		t.Fatalf("job_finished attempts = %d, want retry until success", finishAttempts.Load())
	}
	waitForShellDone(t, parent.jobManager, run.rec.JobID)
	rec := loadShellRecord(t, parent.jobManager, run.rec.JobID)
	if rec.Status != jobstore.StatusCompleted {
		t.Fatalf("record status = %q, want completed", rec.Status)
	}
	structured, ok := rec.StructuredResult.(map[string]any)
	if !ok || structured["summary"] != "retry structured" {
		t.Fatalf("structured_result = %+v, want retry structured", rec.StructuredResult)
	}
	if rec.StructuredResultValid == nil || !*rec.StructuredResultValid {
		t.Fatalf("structured_result_valid = %v, want true", rec.StructuredResultValid)
	}
}

func TestFinalizeDelegateRetriesOutputAppendWithoutClosingDone(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := completedDelegateSubagent(child, "retry output append")
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "retry output", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}
	if err := run.output.Close(); err != nil {
		t.Fatalf("close delegate output: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- parent.finalizeDelegate(run.rec.JobID, child.ID(), sub)
	}()
	select {
	case err := <-done:
		t.Fatalf("finalizeDelegate returned before output was writable: %v", err)
	case <-run.done:
		t.Fatal("delegate done closed before output append was durable")
	case <-time.After(100 * time.Millisecond):
	}

	reopened, err := jobstore.OpenOutput(run.rec.OutputPath, 0)
	if err != nil {
		t.Fatalf("reopen delegate output: %v", err)
	}
	parent.jobManager.mu.Lock()
	run.output = reopened
	parent.jobManager.mu.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("finalizeDelegate after output recovery: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("finalizeDelegate did not retry after output recovery")
	}
	waitForShellDone(t, parent.jobManager, run.rec.JobID)
}

func TestFinalizeDelegateOutputPostWriteFailureDoesNotDuplicateOutput(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	prose := "post-write append failure"
	sub := completedDelegateSubagent(child, prose)
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "retry post-write output", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}

	metaTmpPath := run.rec.OutputPath + ".meta.json.tmp"
	if err := os.Mkdir(metaTmpPath, 0o755); err != nil {
		t.Fatalf("create metadata temp directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(metaTmpPath) })

	done := make(chan error, 1)
	go func() {
		done <- parent.finalizeDelegate(run.rec.JobID, child.ID(), sub)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		raw, readErr := os.ReadFile(run.rec.OutputPath)
		if readErr != nil {
			t.Fatalf("read output file: %v", readErr)
		}
		if strings.Contains(string(raw), prose) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("delegate output was not written before append error")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("finalizeDelegate returned before metadata write recovered: %v", err)
	default:
	}

	if err := os.RemoveAll(metaTmpPath); err != nil {
		t.Fatalf("remove metadata temp directory: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("finalizeDelegate after metadata recovery: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("finalizeDelegate did not retry after metadata recovery")
	}
	waitForShellDone(t, parent.jobManager, run.rec.JobID)
	output, _, _, err := parent.jobManager.readOutput(run.rec.JobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("read delegate output: %v", err)
	}
	if got := strings.Count(output, prose); got != 1 {
		t.Fatalf("delegate output contains terminal prose %d times, want 1: %q", got, output)
	}
}

func TestFinalizeDelegateRetriesNotificationPendingAppendKeepsTerminalResult(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := completedDelegateSubagent(child, "retry notification append")
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "retry notification", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}

	var pendingAttempts atomic.Int32
	origAppend := parent.jobManager.appendEvent
	parent.jobManager.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobNotificationPending && pendingAttempts.Add(1) <= 2 {
			return errors.New("notification pending failed")
		}
		return origAppend(e)
	}

	if err := parent.finalizeDelegate(run.rec.JobID, child.ID(), sub); err != nil {
		t.Fatalf("finalizeDelegate: %v", err)
	}
	if pendingAttempts.Load() < 3 {
		t.Fatalf("notification pending attempts = %d, want retry until success", pendingAttempts.Load())
	}
	waitForShellDone(t, parent.jobManager, run.rec.JobID)
	rec := loadShellRecord(t, parent.jobManager, run.rec.JobID)
	if rec.Status != jobstore.StatusCompleted {
		t.Fatalf("record status = %q, want completed", rec.Status)
	}
	output, _, _, err := parent.jobManager.readOutput(run.rec.JobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(output, "retry notification append") {
		t.Fatalf("output = %q, want retained terminal result", output)
	}
}

func TestFinalizeDelegateDuringManagerCloseDoesNotLeaveDoneOpen(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := completedDelegateSubagent(child, "close finalization")
	sub.running = true
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "close finalization", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}
	var signalOnce sync.Once
	parent.jobManager.mu.Lock()
	run.signal = func() {
		signalOnce.Do(func() {
			sub.mu.Lock()
			sub.running = false
			sub.status = SubagentCancelled
			sub.result = "closed during manager shutdown"
			done := sub.done
			sub.mu.Unlock()
			close(done)
		})
	}
	parent.jobManager.mu.Unlock()

	finalizeDone := make(chan error, 1)
	go func() {
		<-sub.done
		finalizeDone <- parent.finalizeDelegate(run.rec.JobID, child.ID(), sub)
	}()

	closeDone := make(chan error, 1)
	start := time.Now()
	go func() {
		closeDone <- parent.jobManager.close()
	}()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("jobManager.close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("jobManager.close waited for abandoned delegate timeout")
	}
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Fatalf("jobManager.close took %s, want no abandonment timeout", elapsed)
	}
	select {
	case err := <-finalizeDone:
		if err != nil && !errors.Is(err, errJobManagerClosing) {
			t.Fatalf("finalizeDelegate during close error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("finalizeDelegate did not return during close")
	}
	select {
	case <-run.done:
	default:
		t.Fatal("delegate run.done left open after manager close")
	}
	parent.jobManager.mu.Lock()
	stuck := parent.jobManager.running[run.rec.JobID]
	parent.jobManager.mu.Unlock()
	if stuck != nil {
		t.Fatalf("delegate runtime still registered after manager close: %+v", stuck.rec)
	}
}

func TestFinalizeDelegateDuplicateTerminalNotificationsAreIdempotent(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := completedDelegateSubagent(child, "duplicate terminal")
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "duplicate terminal", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}
	var finished, pending atomic.Int32
	origAppend := parent.jobManager.appendEvent
	parent.jobManager.appendEvent = func(e jobstore.Event) error {
		if e.JobID == run.rec.JobID {
			switch e.Kind {
			case jobstore.EventJobFinished:
				finished.Add(1)
			case jobstore.EventJobNotificationPending:
				pending.Add(1)
			}
		}
		return origAppend(e)
	}

	if err := parent.finalizeDelegate(run.rec.JobID, child.ID(), sub); err != nil {
		t.Fatalf("first finalizeDelegate: %v", err)
	}
	if err := parent.finalizeDelegate(run.rec.JobID, child.ID(), sub); err != nil {
		t.Fatalf("second finalizeDelegate: %v", err)
	}
	waitForShellDone(t, parent.jobManager, run.rec.JobID)

	output, _, _, err := parent.jobManager.readOutput(run.rec.JobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if got := strings.Count(output, "duplicate terminal"); got != 1 {
		t.Fatalf("output contains delegate result %d times, want 1: %q", got, output)
	}
	if finished.Load() != 1 || pending.Load() != 1 {
		t.Fatalf("terminal events finished=%d pending=%d, want one each", finished.Load(), pending.Load())
	}
}

func TestCreateDelegateForegroundOutputAppendFailureReturns(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				startedOnce.Do(func() { close(started) })
				<-release
				return communicateWithDefaultOutput("append failure child complete")
			},
		},
	})
	sess := newDelegateTestSession(t, c)

	done := make(chan delegateResult, 1)
	go func() {
		done <- sess.createDelegate(context.Background(), delegateArgs{
			Task:           "finish while output append fails",
			Background:     false,
			BlockTimeoutMS: 5000,
		})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("delegate child did not start")
	}
	run := waitForRunningDelegateJob(t, sess.jobManager)
	appendErr := run.output.Close()
	if appendErr != nil {
		t.Fatalf("close delegate output: %v", appendErr)
	}

	releaseOnce.Do(func() { close(release) })

	select {
	case res := <-done:
		t.Fatalf("createDelegate returned before output append recovered: %+v", res)
	case <-time.After(100 * time.Millisecond):
	}

	reopened, err := jobstore.OpenOutput(run.rec.OutputPath, 0)
	if err != nil {
		t.Fatalf("reopen delegate output: %v", err)
	}
	sess.jobManager.mu.Lock()
	run.output = reopened
	sess.jobManager.mu.Unlock()

	var res delegateResult
	select {
	case res = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("createDelegate did not retry after output append recovery")
	}
	if res.Err != nil {
		t.Fatalf("createDelegate returned error after output recovery: %v", res.Err)
	}
	if res.Status != jobstore.StatusCompleted {
		t.Fatalf("result = %+v, want completed", res)
	}
	waitForShellDone(t, sess.jobManager, res.JobID)
}

func TestSendDelegateMessageTerminalDelegateResumeCreatesNewJob(t *testing.T) {
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("first complete")
			},
		},
	}
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)
	resultSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message": map[string]any{"type": "string"},
		},
		"required": []string{"message"},
	}

	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "finish first",
		Background:     false,
		BlockTimeoutMS: 5000,
		ResultSchema:   resultSchema,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	if first.Status != jobstore.StatusCompleted {
		t.Fatalf("first result = %+v, want completed", first)
	}
	resultSchema["required"] = []string{"mutated"}
	adapter.mu.Lock()
	adapter.steps = append(adapter.steps, func(req llm.Request) llm.Response {
		return communicateWithDefaultOutput("second complete")
	})
	adapter.mu.Unlock()

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  first.JobID,
		Message: "run again",
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Action != "resumed" ||
		res.JobID == "" ||
		res.JobID == first.JobID ||
		res.ResumedFromJobID != first.JobID ||
		res.TranscriptRef != first.TranscriptRef ||
		res.Status != jobstore.StatusRunning ||
		!res.RunningInBackground {
		t.Fatalf("result = %+v, want resumed new running delegate job from %s", res, first.JobID)
	}

	waitForShellDone(t, sess.jobManager, res.JobID)
	rec := loadShellRecord(t, sess.jobManager, res.JobID)
	if rec.Status != jobstore.StatusCompleted || rec.TranscriptRef != first.TranscriptRef {
		t.Fatalf("resumed record = %+v, want completed with same transcript ref", rec)
	}
	if rec.DelegateRestore == nil {
		t.Fatalf("resumed record missing delegate restore descriptor: %+v", rec)
	}
	schema, ok := rec.DelegateRestore.ResultSchema.(map[string]any)
	if !ok {
		t.Fatalf("resumed result_schema = %#v, want object schema", rec.DelegateRestore.ResultSchema)
	}
	required, ok := schema["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "message" {
		t.Fatalf("resumed result_schema.required = %#v, want [message]", schema["required"])
	}
	output, _, _, err := sess.jobManager.readOutput(res.JobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("read resumed output: %v", err)
	}
	if !strings.Contains(output, "second complete") {
		t.Fatalf("resumed output = %q, want second run output", output)
	}
	jobs := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(jobs) != 2 {
		t.Fatalf("delegate jobs = %+v, want two durable jobs", jobs)
	}
}

// TestSendDelegateMessageOwnDirectDelegatesAtDepth: a depth-1 coordinator may
// message its OWN direct worker delegate by job_id (spec §3:
// "own direct delegates at every level"). Today the depth>0 guard rejects every
// concrete delegate target as "root-only"; the coordinator's own worker delegate
// must instead resume.
func TestSendDelegateMessageOwnDirectDelegatesAtDepth(t *testing.T) {
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("worker first complete")
			},
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("worker resumed complete")
			},
		},
	}
	c.Register(adapter)
	coordinator := newDelegateTestSession(t, c)

	// The coordinator spawns its own direct worker delegate (allowance lets a
	// non-root session delegate; depth 0 here so createDelegate's own gates pass).
	worker := coordinator.createDelegate(context.Background(), delegateArgs{
		Task:           "worker first run",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if worker.Err != nil {
		t.Fatalf("createDelegate worker returned error: %v", worker.Err)
	}
	if worker.Status != jobstore.StatusCompleted {
		t.Fatalf("worker first run = %+v, want completed", worker)
	}

	// Make the coordinator a depth-1 session: it is no longer the root. The worker
	// delegate is still its OWN direct delegate.
	coordinator.mu.Lock()
	coordinator.depth = 1
	coordinator.mu.Unlock()

	res := coordinator.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  worker.JobID,
		Message: "worker, run again",
	})
	if res.Err != nil {
		t.Fatalf("depth-1 coordinator messaging its own direct worker delegate: %v", res.Err)
	}
	if res.Action != "resumed" || res.ResumedFromJobID != worker.JobID || res.JobID == "" || res.JobID == worker.JobID {
		t.Fatalf("result = %+v, want resumed new running delegate job from worker %s", res, worker.JobID)
	}

	waitForShellDone(t, coordinator.jobManager, res.JobID)
	output, _, _, err := coordinator.jobManager.readOutput(res.JobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("read resumed worker output: %v", err)
	}
	if !strings.Contains(output, "worker resumed complete") {
		t.Fatalf("resumed worker output = %q, want second run output", output)
	}
}

func TestSendDelegateMessageResumedJobCopiesCompleteDelegateDescriptor(t *testing.T) {
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("first complete")
			},
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("second complete")
			},
		},
	}
	c.Register(adapter)
	workDir := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(workDir)
	env.EnvPolicy = execenv.EnvPolicyCoreOnly
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{
		StateDir:         t.TempDir(),
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
			Skills:       []string{"review-skill"},
			Tasks:        []taskpkg.TaskTemplate{{Title: "Review", Prompt: "Check the patch carefully."}},
			SystemPrompt: "Review carefully.",
			PluginName:   "test-plugin",
		},
	}
	skillDir := filepath.Join(workDir, "skills", "review-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	const reviewSkillBody = "Prefer concise review findings."
	skillFile := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte("---\nname: review-skill\ndescription: Review guidance\n---\n"+reviewSkillBody+"\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	sess.skills["review-skill"] = skill.SkillMeta{
		Name:        "review-skill",
		Description: "Review guidance",
		Dir:         skillDir,
		SkillFile:   skillFile,
	}
	resultSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message": map[string]any{"type": "string"},
		},
		"required": []string{"message"},
	}

	jobID := jobstore.NewJobID()
	ctx := context.WithValue(context.Background(), ctxParentJobID, jobID)
	ctx = context.WithValue(ctx, ctxToolCallID, "call_original_delegate")
	ctx = context.WithValue(ctx, ctxCommunicateOutputSchema, resultSchema)
	prepared, err := sess.prepareSubagentRun(ctx, "finish first", "gpt-5.3", "", 0, "reviewer", "high", nil, []string{"shell"})
	if err != nil {
		t.Fatalf("prepareSubagentRun: %v", err)
	}
	childID := prepared.sub.id
	run, err := sess.attachDelegateJobWithPrepared(sess.jobManager, childID, "finish first", prepared.sub, jobID, resultSchema, false, prepared)
	if err != nil {
		prepared.runCancel()
		prepared.sub.sess.Close()
		t.Fatalf("attachDelegateJobWithPrepared: %v", err)
	}
	if err := sess.trackAndLaunchPreparedSubagent(prepared); err != nil {
		prepared.runCancel()
		prepared.sub.sess.Close()
		t.Fatalf("trackAndLaunchPreparedSubagent: %v", err)
	}
	finalizeErr, _ := sess.bridgeDelegateFinalization(run.rec.JobID, childID, prepared.sub)
	first := waitForDelegateFinalization(context.Background(), sess.jobManager, run, finalizeErr)
	if first.Err != nil || first.Status != jobstore.StatusCompleted {
		t.Fatalf("first delegate result = %+v, want completed", first)
	}

	original := loadShellRecord(t, sess.jobManager, first.JobID)
	if original.DelegateRestore == nil {
		t.Fatalf("original record missing descriptor: %+v", original)
	}
	resultSchema["required"] = []string{"mutated"}

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  first.JobID,
		Message: "run again",
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Action != "resumed" || res.JobID == "" || res.JobID == first.JobID {
		t.Fatalf("resume result = %+v, want new resumed job", res)
	}
	waitForShellDone(t, sess.jobManager, res.JobID)
	rec := loadShellRecord(t, sess.jobManager, res.JobID)
	if rec.DelegateRestore == nil {
		t.Fatalf("resumed record missing descriptor: %+v", rec)
	}
	desc := rec.DelegateRestore
	if desc.ChildSessionID != childID || desc.TranscriptRef != first.TranscriptRef {
		t.Fatalf("resumed descriptor identity = child %q ref %q, want child %q ref %q", desc.ChildSessionID, desc.TranscriptRef, childID, first.TranscriptRef)
	}
	if desc.ParentSessionID != sess.ID() || desc.ParentJobID != res.JobID || desc.OwnerSessionID != sess.ID() || desc.VisibleSessionID != sess.ID() {
		t.Fatalf("resumed descriptor linkage = parent %q job %q owner %q visible %q", desc.ParentSessionID, desc.ParentJobID, desc.OwnerSessionID, desc.VisibleSessionID)
	}
	if desc.OriginToolCallID != "call_original_delegate" || desc.Task != "finish first" {
		t.Fatalf("resumed descriptor origin/task = %q/%q, want original launch values", desc.OriginToolCallID, desc.Task)
	}
	if desc.AgentType != "reviewer" || desc.RequestedModel != "gpt-5.3" || desc.ResolvedProfileID != "openai" || desc.ResolvedModel != "gpt-5.3" {
		t.Fatalf("resumed descriptor model fields = agent %q requested %q resolved %q/%q", desc.AgentType, desc.RequestedModel, desc.ResolvedProfileID, desc.ResolvedModel)
	}
	if desc.ReasoningEffort != "high" || desc.AgentName != "reviewer" || desc.FrozenRolePrompt != "Review carefully." || desc.FrozenTaskPrompt != "Check the patch carefully." {
		t.Fatalf("resumed descriptor shaping = reasoning %q agent %q role %q task %q", desc.ReasoningEffort, desc.AgentName, desc.FrozenRolePrompt, desc.FrozenTaskPrompt)
	}
	if !hasString(desc.FrozenToolNames, "read_file") || !hasString(desc.FrozenToolNames, "task_list") || !hasString(desc.FrozenToolNames, "shell") {
		t.Fatalf("resumed frozen tool names = %+v, want original read_file/task_list/shell", desc.FrozenToolNames)
	}
	if len(desc.FrozenSkillNames) != 1 || desc.FrozenSkillNames[0] != "review-skill" {
		t.Fatalf("resumed frozen skill names = %+v, want [review-skill]", desc.FrozenSkillNames)
	}
	if len(desc.FrozenSkillBodies) != 1 || !strings.Contains(desc.FrozenSkillBodies[0], reviewSkillBody) {
		t.Fatalf("resumed frozen skill bodies = %+v, want original review skill body", desc.FrozenSkillBodies)
	}
	if desc.WorkingDir != workDir || desc.LocalEnvPolicy != "core_only" {
		t.Fatalf("resumed env fields = %q/%q, want %q/core_only", desc.WorkingDir, desc.LocalEnvPolicy, workDir)
	}
	if len(desc.ExplicitToolGrants) != 1 || desc.ExplicitToolGrants[0] != "shell" {
		t.Fatalf("resumed explicit grants = %+v, want [shell]", desc.ExplicitToolGrants)
	}
	schema, ok := desc.ResultSchema.(map[string]any)
	if !ok {
		t.Fatalf("resumed result_schema = %#v, want object schema", desc.ResultSchema)
	}
	required, ok := schema["required"].([]any)
	if !ok || len(required) != 1 || required[0] != "message" {
		t.Fatalf("resumed result_schema.required = %#v, want original [message]", schema["required"])
	}
}

func TestSendDelegateMessageTerminalDelegateForegroundResumeTimeoutLeavesChildRunning(t *testing.T) {
	adapter := &resumeBlockingDelegateAdapter{name: "openai", secondStarted: make(chan struct{})}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "finish first",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	if first.Status != jobstore.StatusCompleted {
		t.Fatalf("first result = %+v, want completed", first)
	}

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         first.JobID,
		Message:        "run again",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 1000,
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Action != "resumed" ||
		res.JobID == "" ||
		res.JobID == first.JobID ||
		res.ResumedFromJobID != first.JobID ||
		res.Status != jobstore.StatusRunning ||
		res.Reason != "foreground_timeout" ||
		!res.RunningInBackground ||
		!res.TimedOut ||
		res.TranscriptRef != first.TranscriptRef {
		t.Fatalf("result = %+v, want resumed foreground timeout running in background", res)
	}
	select {
	case <-adapter.secondStarted:
	default:
		t.Fatal("resumed delegate did not start before foreground timeout")
	}
	_, childID, err := decodeRef(first.TranscriptRef)
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

	_, _ = sess.jobManager.stop(res.JobID)
	waitForShellDone(t, sess.jobManager, res.JobID)
}

func TestSendDelegateMessageTerminalDelegateFailOnFinished(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("terminal complete")
			},
		},
	})
	sess := newDelegateTestSession(t, c)

	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "finish once",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:     first.JobID,
		Message:    "must be live",
		OnFinished: "fail",
	})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "target_terminal") {
		t.Fatalf("error = %v, want target_terminal", res.Err)
	}
	if jobs := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate}); len(jobs) != 1 {
		t.Fatalf("delegate jobs = %+v, want no new job", jobs)
	}
}

func TestSendDelegateMessageObservedTerminalRunningRecordFailReturnsTargetTerminal(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := &subagent{
		id:      child.ID(),
		sess:    child,
		running: false,
		status:  SubagentCompleted,
		result:  "already done",
		done:    make(chan struct{}),
	}
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "already terminal", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}
	t.Cleanup(func() {
		if err := parent.finalizeDelegate(run.rec.JobID, child.ID(), sub); err != nil {
			t.Fatalf("cleanup finalizeDelegate: %v", err)
		}
		waitForShellDone(t, parent.jobManager, run.rec.JobID)
	})

	before := parent.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	res := parent.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:     run.rec.JobID,
		Message:    "must still be live",
		OnFinished: "fail",
	})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "target_terminal") {
		t.Fatalf("error = %v, want target_terminal", res.Err)
	}
	if strings.Contains(res.Err.Error(), "not_controllable") {
		t.Fatalf("error = %v, must not report not_controllable", res.Err)
	}
	after := parent.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(after) != len(before) {
		t.Fatalf("delegate jobs grew from %d to %d; jobs = %+v", len(before), len(after), after)
	}
}

func TestWatchOriginatedRunningSendDoesNotMarkNonLiveDelegateFromWatch(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := &subagent{
		id:      child.ID(),
		sess:    child,
		running: false,
		status:  SubagentCompleted,
		result:  "already done",
		done:    make(chan struct{}),
	}
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "already terminal", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}
	t.Cleanup(func() {
		if err := parent.finalizeDelegate(run.rec.JobID, child.ID(), sub); err != nil {
			t.Fatalf("cleanup finalizeDelegate: %v", err)
		}
		waitForShellDone(t, parent.jobManager, run.rec.JobID)
	})

	res := parent.sendRunningDelegateMessage(run.rec.JobID, "watch-originated steer", run.rec, true)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "not_controllable") {
		t.Fatalf("error = %v, want not_controllable", res.Err)
	}
	if run.fromWatch.Load() {
		t.Fatalf("non-live delegate was marked watch-originated after undelivered send")
	}
}

func TestWatchOriginatedRunningSendRejectedByClosingChildStaysBusy(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := &subagent{
		id:      child.ID(),
		sess:    child,
		running: true,
		status:  SubagentRunning,
		done:    make(chan struct{}),
	}
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "closing child", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}
	t.Cleanup(func() {
		sub.mu.Lock()
		sub.running = false
		sub.status = SubagentCompleted
		sub.result = "closed"
		sub.mu.Unlock()
		if err := parent.finalizeDelegate(run.rec.JobID, child.ID(), sub); err != nil {
			t.Fatalf("cleanup finalizeDelegate: %v", err)
		}
		waitForShellDone(t, parent.jobManager, run.rec.JobID)
	})
	child.mu.Lock()
	child.state = SessionClosed
	child.mu.Unlock()

	res := parent.sendRunningDelegateMessage(run.rec.JobID, "watch-originated steer", run.rec, true)
	if res.Err != nil {
		t.Fatalf("sendRunningDelegateMessage returned error: %v", res.Err)
	}
	if res.Action != "busy" || !res.WatchSendDeliveryClassSet || res.WatchSendDeliveryClass != watchSendBusy {
		t.Fatalf("result = %+v, want retryable watch busy", res)
	}
	if run.fromWatch.Load() || sub.runFromWatch {
		t.Fatalf("rejected watch send marked lifecycle fromWatch: run=%v sub=%v", run.fromWatch.Load(), sub.runFromWatch)
	}
	if queue := child.SteeringQueueSnapshot(); len(queue) != 0 {
		t.Fatalf("closing child steering queue = %+v, want no delivery", queue)
	}
}

func TestWatchOriginatedResumeMarksJobStartedFromWatch(t *testing.T) {
	adapter := &resumeBlockingDelegateAdapter{name: "openai", secondStarted: make(chan struct{})}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "finish first",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	if first.Status != jobstore.StatusCompleted {
		t.Fatalf("first result = %+v, want completed", first)
	}

drain:
	for {
		select {
		case <-sess.Events():
		default:
			break drain
		}
	}

	second := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:    first.JobID,
		Message:   "watch-originated resume",
		FromWatch: true,
	})
	if second.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", second.Err)
	}
	if second.Action != "resumed" || second.JobID == "" || second.JobID == first.JobID {
		t.Fatalf("second result = %+v, want resumed new running delegate job", second)
	}

	var start events.JobStartedData
	for deadline := time.After(2 * time.Second); ; {
		select {
		case ev := <-sess.Events():
			data, ok := ev.Data.(events.JobStartedData)
			if ok && data.JobID == second.JobID {
				start = data
				goto gotStart
			}
		case <-deadline:
			t.Fatal("timed out waiting for resumed job started event")
		}
	}

gotStart:
	if !start.FromWatch {
		t.Fatalf("watch-originated resumed JOB_STARTED FromWatch = false; event = %+v", start)
	}

	_, _ = sess.jobManager.stop(second.JobID)
	waitForShellDone(t, sess.jobManager, second.JobID)
}

func TestSendDelegateMessageTerminalDelegateResumeSteersActiveRun(t *testing.T) {
	adapter := &resumeBlockingDelegateAdapter{name: "openai", secondStarted: make(chan struct{})}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "finish first",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	if first.Status != jobstore.StatusCompleted {
		t.Fatalf("first result = %+v, want completed", first)
	}

	second := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  first.JobID,
		Message: "run again",
	})
	if second.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", second.Err)
	}
	if second.Action != "resumed" || second.JobID == "" || second.JobID == first.JobID {
		t.Fatalf("second result = %+v, want resumed new running delegate job", second)
	}
	select {
	case <-adapter.secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("resumed delegate did not start")
	}

	before := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  first.JobID,
		Message: "steer current run",
	})
	if res.Err != nil {
		if strings.Contains(res.Err.Error(), "delegate_session_busy") {
			t.Fatalf("sendDelegateMessage returned delegate_session_busy: %v", res.Err)
		}
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Action != "sent" ||
		res.JobID != second.JobID ||
		res.JobID == first.JobID ||
		res.Status != jobstore.StatusRunning ||
		!res.RunningInBackground ||
		res.TranscriptRef != first.TranscriptRef {
		t.Fatalf("result = %+v, want sent to active resumed delegate job %s", res, second.JobID)
	}
	after := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(after) != len(before) {
		t.Fatalf("delegate jobs grew from %d to %d; jobs = %+v", len(before), len(after), after)
	}
	_, childID, err := decodeRef(first.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := sess.subagents.get(childID)
	if sub == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	queue := sub.sess.SteeringQueueSnapshot()
	if len(queue) != 1 || queue[0].Text != "steer current run" {
		t.Fatalf("steering queue = %+v, want steered message", queue)
	}

	_, _ = sess.jobManager.stop(second.JobID)
	waitForShellDone(t, sess.jobManager, second.JobID)
}

func TestSendDelegateMessageTerminalResumeWaitsForDelegateJobAttachment(t *testing.T) {
	adapter := &resumeBlockingDelegateAdapter{name: "openai", secondStarted: make(chan struct{})}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "finish first",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	if first.Status != jobstore.StatusCompleted {
		t.Fatalf("first result = %+v, want completed", first)
	}

	attachStarted := make(chan struct{})
	releaseAttach := make(chan struct{})
	var attachOnce sync.Once
	origAppend := sess.jobManager.appendEvent
	defer func() { sess.jobManager.appendEvent = origAppend }()
	sess.jobManager.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobStarted && e.Type == jobstore.JobDelegate && e.Task == "run again" {
			attachOnce.Do(func() { close(attachStarted) })
			<-releaseAttach
		}
		return origAppend(e)
	}

	firstResumeDone := make(chan sendMessageResult, 1)
	go func() {
		firstResumeDone <- sess.sendDelegateMessage(context.Background(), sendMessageArgs{
			Target:  first.JobID,
			Message: "run again",
		})
	}()
	select {
	case <-attachStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("resumed delegate job attachment did not start")
	}

	secondResumeDone := make(chan sendMessageResult, 1)
	go func() {
		secondResumeDone <- sess.sendDelegateMessage(context.Background(), sendMessageArgs{
			Target:  first.JobID,
			Message: "steer while attaching",
		})
	}()
	select {
	case res := <-secondResumeDone:
		t.Fatalf("second terminal resume returned before delegate job attached: %+v", res)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseAttach)
	var firstResume sendMessageResult
	select {
	case firstResume = <-firstResumeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first terminal resume did not return")
	}
	if firstResume.Err != nil {
		t.Fatalf("first terminal resume returned error: %v", firstResume.Err)
	}
	if firstResume.Action != "resumed" || firstResume.JobID == "" || firstResume.JobID == first.JobID {
		t.Fatalf("first terminal resume = %+v, want resumed new delegate job", firstResume)
	}

	var secondResume sendMessageResult
	select {
	case secondResume = <-secondResumeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("second terminal resume did not return after attachment released")
	}
	if secondResume.Err != nil {
		t.Fatalf("second terminal resume returned error: %v", secondResume.Err)
	}
	if secondResume.Action != "sent" ||
		secondResume.JobID != firstResume.JobID ||
		secondResume.TranscriptRef != first.TranscriptRef {
		t.Fatalf("second terminal resume = %+v, want sent to active resumed job %s", secondResume, firstResume.JobID)
	}

	_, childID, err := decodeRef(first.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := sess.subagents.get(childID)
	if sub == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	queue := sub.sess.SteeringQueueSnapshot()
	if len(queue) != 1 || queue[0].Text != "steer while attaching" {
		t.Fatalf("steering queue = %+v, want concurrent terminal resume steered", queue)
	}
	jobs := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(jobs) != 2 {
		t.Fatalf("delegate jobs = %+v, want original plus one resumed job", jobs)
	}

	_, _ = sess.jobManager.stop(firstResume.JobID)
	waitForShellDone(t, sess.jobManager, firstResume.JobID)
}

func TestSendDelegateMessageTerminalTargetFailDoesNotSteerLaterRun(t *testing.T) {
	adapter := &resumeBlockingDelegateAdapter{name: "openai", secondStarted: make(chan struct{})}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:           "finish first",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	if first.Status != jobstore.StatusCompleted {
		t.Fatalf("first result = %+v, want completed", first)
	}

	second := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  first.JobID,
		Message: "run again",
	})
	if second.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", second.Err)
	}
	if second.Action != "resumed" || second.JobID == "" || second.JobID == first.JobID {
		t.Fatalf("second result = %+v, want resumed new running delegate job", second)
	}
	select {
	case <-adapter.secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("resumed delegate did not start")
	}

	before := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:     first.JobID,
		Message:    "must not steer running job",
		OnFinished: "fail",
	})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "target_terminal") {
		t.Fatalf("error = %v, want target_terminal", res.Err)
	}
	after := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(after) != len(before) {
		t.Fatalf("delegate jobs grew from %d to %d; jobs = %+v", len(before), len(after), after)
	}
	_, childID, err := decodeRef(first.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := sess.subagents.get(childID)
	if sub == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	for _, entry := range sub.sess.SteeringQueueSnapshot() {
		if entry.Text == "must not steer running job" {
			t.Fatalf("terminal target message was steered to running job; queue = %+v", sub.sess.SteeringQueueSnapshot())
		}
	}

	_, _ = sess.jobManager.stop(second.JobID)
	waitForShellDone(t, sess.jobManager, second.JobID)
}

func TestSendDelegateMessageStoppedDelegateRestorePreflightNotResumable(t *testing.T) {
	cases := []struct {
		name       string
		breakState func(*testing.T, *Session, *jobstore.JobRecord)
		want       string
	}{
		{
			name: "missing descriptor",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore = nil
				replaceStoredDelegateRecord(t, s, rec)
			},
			want: "target_not_resumable:missing_delegate_resume_metadata",
		},
		{
			name: "bad linkage",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.ParentJobID = "job_other"
				replaceStoredDelegateRecord(t, s, rec)
			},
			want: "target_not_resumable:parent_linkage_unavailable",
		},
		{
			name: "missing local env policy",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.LocalEnvPolicy = ""
				replaceStoredDelegateRecord(t, s, rec)
			},
			want: "target_not_resumable:parent_linkage_unavailable",
		},
		{
			name: "invalid local env policy",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.LocalEnvPolicy = "all-ish"
				replaceStoredDelegateRecord(t, s, rec)
			},
			want: "target_not_resumable:parent_linkage_unavailable",
		},
		{
			name: "missing working dir",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.WorkingDir = ""
				replaceStoredDelegateRecord(t, s, rec)
			},
			want: "target_not_resumable:parent_linkage_unavailable",
		},
		{
			name: "missing meta",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				removeChildSessionMeta(t, s, rec)
			},
			want: "target_not_resumable:missing_child_session_meta",
		},
		{
			name: "corrupt meta",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				writeChildSessionMeta(t, s, rec, []byte(`{`))
			},
			want: "target_not_resumable:corrupt_child_session_meta",
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
			want: "target_not_resumable:corrupt_child_session_meta",
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
			want: "target_not_resumable:corrupt_child_session_meta",
		},
		{
			name: "missing transcript",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				removeChildTranscript(t, s, rec)
			},
			want: "target_not_resumable:missing_child_transcript",
		},
		{
			name: "corrupt transcript",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				appendChildTranscript(t, s, rec, "\n{not-json}\n{\"kind\":\"entry\"}\n")
			},
			want: "target_not_resumable:corrupt_child_transcript",
		},
		{
			name: "corrupt transcript misleading kind",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				appendChildTranscript(t, s, rec, "\n{\"kind\":\"transcript_session_mismatch\"}\n")
			},
			want: "target_not_resumable:corrupt_child_transcript",
		},
		{
			name: "session mismatch",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				writeChildTranscript(t, s, rec, []byte(`{"kind":"header","format_version":1,"session_id":"other"}`+"\n"))
			},
			want: "target_not_resumable:transcript_session_mismatch",
		},
		{
			name: "corrupt transcript header shape",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				writeChildTranscript(t, s, rec, []byte(fmt.Sprintf(`{"session_id":%q}`+"\n", rec.DelegateRestore.ChildSessionID)))
			},
			want: "target_not_resumable:corrupt_child_transcript",
		},
		{
			name: "busy child",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				childID := rec.DelegateRestore.ChildSessionID
				s.subagents.track(&subagent{
					id:      childID,
					sess:    newTestSession(t),
					running: true,
					done:    make(chan struct{}),
				})
			},
			want: "target_not_resumable:child_session_busy",
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
			want: "target_not_resumable:profile_unavailable",
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
			want: "target_not_resumable:profile_unavailable",
		},
		{
			name: "descriptor profile id without model",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.ResolvedProfileID = "openai"
				rec.DelegateRestore.ResolvedModel = ""
				replaceStoredDelegateRecord(t, s, rec)
			},
			want: "target_not_resumable:profile_unavailable",
		},
		{
			name: "descriptor model without profile id",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.ResolvedProfileID = ""
				rec.DelegateRestore.ResolvedModel = "gpt-5.2"
				replaceStoredDelegateRecord(t, s, rec)
			},
			want: "target_not_resumable:profile_unavailable",
		},
		{
			name: "descriptor missing resolved profile fields",
			breakState: func(t *testing.T, s *Session, rec *jobstore.JobRecord) {
				rec.DelegateRestore.ResolvedProfileID = ""
				rec.DelegateRestore.ResolvedModel = ""
				replaceStoredDelegateRecord(t, s, rec)
			},
			want: "target_not_resumable:profile_unavailable",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := llm.NewClient()
			adapter := &fakeAdapter{name: "openai"}
			c.Register(adapter)
			s := newDelegateRestorePreflightSession(t, c)
			rec := seedStoppedDelegateRestoreRecord(t, s)
			tc.breakState(t, s, rec)
			beforeEvents := len(loadJobStoreEvents(t, s.jobManager))
			beforeJobs := len(s.jobManager.list(listFilter{Type: jobstore.JobDelegate}))

			res := s.sendDelegateMessage(context.Background(), sendMessageArgs{
				Target:  rec.JobID,
				Message: "resume",
			})

			if res.Err == nil || res.Err.Error() != tc.want {
				t.Fatalf("error = %v, want %s", res.Err, tc.want)
			}
			if got := len(loadJobStoreEvents(t, s.jobManager)); got != beforeEvents {
				t.Fatalf("jobstore event count = %d, want unchanged %d", got, beforeEvents)
			}
			if got := len(s.jobManager.list(listFilter{Type: jobstore.JobDelegate})); got != beforeJobs {
				t.Fatalf("delegate job count = %d, want unchanged %d", got, beforeJobs)
			}
			if requests := adapter.Requests(); len(requests) != 0 {
				t.Fatalf("adapter requests = %+v, want none", requests)
			}
		})
	}
}

func TestReconstructDelegateRuntimeCollisionDoesNotCleanupSharedEnv(t *testing.T) {
	adapter := &fakeAdapter{name: "openai"}
	c := llm.NewClient()
	c.Register(adapter)
	workDir := t.TempDir()
	env := &cleanupCountingEnv{ExecutionEnvironment: execenv.NewLocalExecutionEnvironment(workDir)}
	s, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { s.close(false) })
	rec := seedStoppedDelegateRestoreRecord(t, s)
	rec.DelegateRestore.WorkingDir = workDir
	replaceStoredDelegateRecord(t, s, rec)
	markStoredDelegateResumable(t, s, rec)
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	childID := rec.DelegateRestore.ChildSessionID
	preflight := requireDelegateRestorePreflight(t, s, rec)

	first, err := s.restoreTerminalDelegateChild(rec, childID, preflight)
	if err != nil {
		t.Fatalf("first restoreTerminalDelegateChild: %v", err)
	}
	second, err := s.restoreTerminalDelegateChild(rec, childID, preflight)
	if err != nil {
		t.Fatalf("second restoreTerminalDelegateChild: %v", err)
	}

	if second != first {
		t.Fatalf("second reconstruction returned %p, want existing tracked child %p", second, first)
	}
	if got := env.count(); got != 0 {
		t.Fatalf("shared env cleanup count = %d, want loser candidate close to skip env cleanup", got)
	}
	if requests := adapter.Requests(); len(requests) != 0 {
		t.Fatalf("adapter requests = %+v, want no model calls during reconstruction", requests)
	}
}

func TestSendDelegateMessageRuntimeLostRestoreUsesDescriptorPreflightProfile(t *testing.T) {
	openAIAdapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("wrong provider")
			},
		},
	}
	workAdapter := &fakeAdapter{
		name: "work",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("descriptor provider")
			},
		},
	}
	c := llm.NewClient()
	c.Register(openAIAdapter)
	c.Register(workAdapter)
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	markStoredDelegateResumable(t, s, rec)
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	rec.DelegateRestore.ResolvedProfileID = "work"
	rec.DelegateRestore.ResolvedModel = "descriptor-model"
	replaceStoredDelegateRecord(t, s, rec)
	meta, err := schema.LoadSessionMeta(s.stateDir, rec.DelegateRestore.ChildSessionID)
	if err != nil {
		t.Fatalf("load child meta: %v", err)
	}
	meta.Model = "gpt-5.2"
	if err := schema.SaveSessionMeta(s.stateDir, meta); err != nil {
		t.Fatalf("save child meta: %v", err)
	}
	s.resolveProfile = func(ref string) (*provider.Profile, error) {
		if ref != "work/descriptor-model" {
			return nil, fmt.Errorf("unexpected profile ref %s", ref)
		}
		return WithProviderID(NewOpenAIProfile("descriptor-model"), "work"), nil
	}

	res := s.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         rec.JobID,
		Message:        "resume using descriptor",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})

	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Status != jobstore.StatusCompleted || !strings.Contains(res.Output, "descriptor provider") {
		t.Fatalf("result = %+v, want descriptor provider completion", res)
	}
	workRequests := workAdapter.Requests()
	if len(workRequests) != 1 {
		t.Fatalf("work adapter requests = %+v, want one descriptor-profile request", workRequests)
	}
	if workRequests[0].Provider != "work" || workRequests[0].Model != "descriptor-model" {
		t.Fatalf("work request provider/model = %s/%s, want work/descriptor-model", workRequests[0].Provider, workRequests[0].Model)
	}
	if openAIRequests := openAIAdapter.Requests(); len(openAIRequests) != 0 {
		t.Fatalf("openai adapter requests = %+v, want none", openAIRequests)
	}
}

func TestRestoreRuntimeLostDelegateNoAutoResumeNoModel(t *testing.T) {
	adapter := &fakeAdapter{name: "openai"}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	childID := rec.DelegateRestore.ChildSessionID
	parentMeta := s.Meta()
	stateDir := s.stateDir
	beforeJobs := len(s.jobManager.list(listFilter{Type: jobstore.JobDelegate}))
	s.Close()

	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), parentMeta, RestoreSessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(func() { restored.Close() })

	if requests := adapter.Requests(); len(requests) != 0 {
		t.Fatalf("adapter requests = %+v, want no model calls during restore", requests)
	}
	if jobs := restored.jobManager.list(listFilter{Type: jobstore.JobDelegate}); len(jobs) != beforeJobs {
		t.Fatalf("delegate jobs after restore = %+v, want %d existing job only", jobs, beforeJobs)
	}
	if sub := restored.subagents.get(childID); sub != nil {
		t.Fatalf("restored child runtime = %+v, want none before job_send_message", sub)
	}
}

func TestJobSendMessageReconstructsRestoredDelegateRuntimeFromDescriptor(t *testing.T) {
	var request llm.Request
	workAdapter := &fakeAdapter{
		name: "work",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				request = req
				return communicateWithDefaultOutput("restored delegate complete")
			},
		},
	}
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	c.Register(workAdapter)
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	childID := rec.DelegateRestore.ChildSessionID
	childWorkDir := t.TempDir()
	parentRestoreWorkDir := t.TempDir()
	resultSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{"type": "string"},
		},
		"required": []string{"summary"},
	}
	rec.DelegateRestore.ResolvedProfileID = "work"
	rec.DelegateRestore.ResolvedModel = "descriptor-model"
	rec.DelegateRestore.ReasoningEffort = "high"
	rec.DelegateRestore.WorkingDir = childWorkDir
	rec.DelegateRestore.LocalEnvPolicy = "none"
	rec.DelegateRestore.ResultSchema = resultSchema
	rec.DelegateRestore.ExplicitToolGrants = []string{"shell"}
	rec.DelegateRestore.FrozenToolNames = []string{"shell"}
	replaceStoredDelegateRecord(t, s, rec)
	parentMeta := s.Meta()
	stateDir := s.stateDir
	s.Close()

	restoredParentEnv := execenv.NewLocalExecutionEnvironment(parentRestoreWorkDir)
	restoredParentEnv.EnvPolicy = execenv.EnvPolicyAll
	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), restoredParentEnv, parentMeta, RestoreSessionConfig{
		StateDir: stateDir,
		ResolveProfile: func(ref string) (*provider.Profile, error) {
			if ref != "work/descriptor-model" {
				return nil, fmt.Errorf("unexpected profile ref %s", ref)
			}
			return WithProviderID(NewOpenAIProfile("descriptor-model"), "work"), nil
		},
	})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(func() { restored.Close() })
	if sub := restored.subagents.get(childID); sub != nil {
		t.Fatalf("restored child runtime before send = %+v, want none", sub)
	}

	res := restored.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         rec.JobID,
		Message:        "new input after restore",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}

	sub := restored.subagents.get(childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("reconstructed child runtime = %+v, want retained child session", sub)
	}
	child := sub.sess
	profile := child.currentProfile()
	if profile.ID() != "work" || profile.Model() != "descriptor-model" {
		t.Fatalf("child profile = %s/%s, want work/descriptor-model", profile.ID(), profile.Model())
	}
	if child.cfg.ReasoningEffort != "high" {
		t.Fatalf("child reasoning effort = %q, want high", child.cfg.ReasoningEffort)
	}
	if child.env.WorkingDirectory() != childWorkDir {
		t.Fatalf("child working dir = %q, want %q", child.env.WorkingDirectory(), childWorkDir)
	}
	if got := localEnvPolicyName(child.env); got != "none" {
		t.Fatalf("child local env policy = %q, want none", got)
	}
	if child.cfg.spawn.parentSessionID != restored.ID() ||
		child.cfg.spawn.parentJobID != res.JobID ||
		child.cfg.spawn.subagentTask != rec.DelegateRestore.Task ||
		child.cfg.spawn.depth != restored.depth+1 ||
		child.cfg.spawn.parentSteer == nil {
		t.Fatalf("child spawn config = %+v, want descriptor parent linkage with resumed job %q", child.cfg.spawn, res.JobID)
	}
	if child.jobManager.parentJobID != res.JobID || child.jobManager.forward == nil {
		t.Fatalf("child job manager linkage = parentJobID %q forwardSet %t, want resumed job %q and forward hook", child.jobManager.parentJobID, child.jobManager.forward != nil, res.JobID)
	}
	if child.reg.Get("shell") == nil {
		t.Fatal("reconstructed child missing explicitly granted shell tool")
	}
	if !communicateOutputSchemaHasProperty(request, "summary") {
		t.Fatalf("first restored request did not inherit result schema: %+v", request.Tools)
	}
	if request.Provider != "work" || request.Model != "descriptor-model" {
		t.Fatalf("first restored request provider/model = %s/%s, want work/descriptor-model", request.Provider, request.Model)
	}
	if request.ReasoningEffort == nil || *request.ReasoningEffort != "high" {
		t.Fatalf("first restored request reasoning = %#v, want high", request.ReasoningEffort)
	}
	if requestMessagesContain(request, rec.DelegateRestore.Task) {
		t.Fatalf("old delegate task %q was submitted as a new restored turn: %+v", rec.DelegateRestore.Task, request.Messages)
	}
	if !requestMessagesContain(request, "new input after restore") {
		t.Fatalf("new resumed input missing from first restored request: %+v", request.Messages)
	}
}

func TestRuntimeLostDelegateResumeAfterRestoreCreatesNewJobFromRetainedState(t *testing.T) {
	const originalTask = "original runtime-lost delegate task"
	const firstOutput = "old retained delegate output"
	const resumedOutput = "new retained delegate output"
	const resumedInput = "new input after restart"

	var restoredRequest llm.Request
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithStructured(firstOutput, map[string]any{"summary": "old"})
			},
			func(req llm.Request) llm.Response {
				restoredRequest = req
				return communicateWithStructured(resumedOutput, map[string]any{"summary": "new"})
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateTestSession(t, c)
	resultSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{"type": "string"},
		},
		"required": []string{"summary"},
	}

	first := s.createDelegate(context.Background(), delegateArgs{
		Task:           originalTask,
		Background:     false,
		BlockTimeoutMS: 5000,
		ResultSchema:   resultSchema,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	if first.Status != jobstore.StatusCompleted || !strings.Contains(first.Output, firstOutput) {
		t.Fatalf("first result = %+v, want completed old output", first)
	}
	oldRec := loadShellRecord(t, s.jobManager, first.JobID)
	childID := oldRec.DelegateRestore.ChildSessionID
	setStoredDelegateTerminalStatus(t, s, oldRec, jobstore.StatusStopped, "runtime_lost")
	parentMeta := s.Meta()
	stateDir := s.stateDir
	s.Close()

	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), parentMeta, RestoreSessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(func() { restored.Close() })
	if sub := restored.subagents.get(childID); sub != nil {
		t.Fatalf("restored child runtime = %+v, want none before job_send_message", sub)
	}
	if requests := adapter.Requests(); len(requests) != 1 {
		t.Fatalf("adapter requests before send = %+v, want only initial delegate request", requests)
	}

	res := restored.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         first.JobID,
		Message:        resumedInput,
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.JobID == "" || res.JobID == first.JobID ||
		res.Action != "resumed" ||
		res.ResumedFromJobID != first.JobID ||
		res.Status != jobstore.StatusCompleted ||
		res.TranscriptRef != first.TranscriptRef ||
		!strings.Contains(res.Output, resumedOutput) ||
		!res.StructuredResultValidSet ||
		!res.StructuredResultValid {
		t.Fatalf("resume result = %+v, want new completed resumed job", res)
	}
	oldAfter := loadShellRecord(t, restored.jobManager, first.JobID)
	if oldAfter.Status != jobstore.StatusStopped || oldAfter.Reason != "runtime_lost" {
		t.Fatalf("old record = %+v, want stopped/runtime_lost", oldAfter)
	}
	oldOutput, _, _, err := restored.jobManager.readOutput(first.JobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("read old output: %v", err)
	}
	if !strings.Contains(oldOutput, firstOutput) || strings.Contains(oldOutput, resumedOutput) {
		t.Fatalf("old output = %q, want only old output", oldOutput)
	}
	newOutput, _, _, err := restored.jobManager.readOutput(res.JobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("read new output: %v", err)
	}
	if !strings.Contains(newOutput, resumedOutput) || strings.Contains(newOutput, firstOutput) {
		t.Fatalf("new output = %q, want only resumed output", newOutput)
	}
	newRec := loadShellRecord(t, restored.jobManager, res.JobID)
	if newRec.TranscriptRef != first.TranscriptRef {
		t.Fatalf("new transcript_ref = %q, want %q", newRec.TranscriptRef, first.TranscriptRef)
	}
	if newRec.DelegateRestore == nil || newRec.DelegateRestore.ParentJobID != res.JobID {
		t.Fatalf("new delegate restore = %+v, want parent job set to new job", newRec.DelegateRestore)
	}
	if !communicateOutputSchemaHasProperty(restoredRequest, "summary") {
		t.Fatalf("restored request did not inherit result schema: %+v", restoredRequest.Tools)
	}
	if !requestMessagesContain(restoredRequest, originalTask) {
		t.Fatalf("restored request missing retained original user turn %q: %+v", originalTask, restoredRequest.Messages)
	}
	if last := lastUserMessageText(restoredRequest); last != resumedInput {
		t.Fatalf("last user message = %q, want fresh resumed input %q; messages = %+v", last, resumedInput, restoredRequest.Messages)
	}
	if strings.Contains(lastUserMessageText(restoredRequest), originalTask) {
		t.Fatalf("original task was submitted as fresh input: %+v", restoredRequest.Messages)
	}
	if got := countRequestMessagesContaining(restoredRequest, resumedInput); got != 1 {
		t.Fatalf("fresh input message count = %d, want 1; messages = %+v", got, restoredRequest.Messages)
	}
	if requests := adapter.Requests(); len(requests) != 2 {
		t.Fatalf("adapter requests after send = %+v, want initial plus resumed request", requests)
	}
}

func TestRuntimeLostDelegateResumeRelinksNestedJobsToNewJob(t *testing.T) {
	const firstOutput = "old nested-link delegate output"
	const resumedOutput = "new nested-link delegate output"
	const nestedDescription = "runtime-lost resumed nested shell"

	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput(firstOutput)
			},
			func(req llm.Request) llm.Response {
				return toolCallResponse(llm.ToolCallData{
					ID:   "nested_shell",
					Name: "shell",
					Arguments: json.RawMessage(fmt.Sprintf(
						`{"command":"printf 'runtime-lost-nested-ready\n'; sleep 30","description":%q,"max_wait_ms":1000}`,
						nestedDescription,
					)),
					Type: "function",
				})
			},
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput(resumedOutput)
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateTestSession(t, c)

	first := s.createDelegate(context.Background(), delegateArgs{
		Task:           "start runtime-lost nested-link delegate",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	oldRec := loadShellRecord(t, s.jobManager, first.JobID)
	childID := oldRec.DelegateRestore.ChildSessionID
	setStoredDelegateTerminalStatus(t, s, oldRec, jobstore.StatusStopped, "runtime_lost")
	parentMeta := s.Meta()
	stateDir := s.stateDir
	s.Close()

	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), parentMeta, RestoreSessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(func() { restored.Close() })
	if sub := restored.subagents.get(childID); sub != nil {
		t.Fatalf("restored child runtime = %+v, want none before job_send_message", sub)
	}

	res := restored.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         first.JobID,
		Message:        "resume and start a nested shell",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.JobID == "" || res.JobID == first.JobID || res.Action != "resumed" || res.ResumedFromJobID != first.JobID {
		t.Fatalf("resume result = %+v, want new resumed job from old runtime-lost job", res)
	}
	oldAfter := loadShellRecord(t, restored.jobManager, first.JobID)
	if oldAfter.Status != jobstore.StatusStopped || oldAfter.Reason != "runtime_lost" {
		t.Fatalf("old record = %+v, want stopped/runtime_lost", oldAfter)
	}
	oldOutput, _, _, err := restored.jobManager.readOutput(first.JobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("read old output: %v", err)
	}
	if !strings.Contains(oldOutput, firstOutput) || strings.Contains(oldOutput, resumedOutput) {
		t.Fatalf("old output = %q, want only old output", oldOutput)
	}

	sub := restored.subagents.get(childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("reconstructed child runtime = %+v, want retained child session", sub)
	}
	child := sub.sess
	nested := findJobByDescription(t, child.jobManager, nestedDescription)
	t.Cleanup(func() {
		if rec, err := findJobRecord(child.jobManager, nested.JobID); err == nil && rec.Status == jobstore.StatusRunning {
			_, _ = restored.stopNestedOrLocal(nested.JobID)
			waitForShellDone(t, child.jobManager, nested.JobID)
		}
	})
	if nested.ParentJobID != res.JobID {
		t.Fatalf("child nested ParentJobID = %q, want new resumed job %q", nested.ParentJobID, res.JobID)
	}
	parentNested := loadShellRecord(t, restored.jobManager, nested.JobID)
	if parentNested.ParentJobID != res.JobID {
		t.Fatalf("forwarded nested ParentJobID = %q, want new resumed job %q", parentNested.ParentJobID, res.JobID)
	}
	if parentNested.ParentJobID == first.JobID {
		t.Fatalf("forwarded nested job remained linked to old runtime-lost job %q", first.JobID)
	}

	stopOut, err := jobStopTool(context.Background(), restored, map[string]any{
		"job_id":           res.JobID,
		"include_children": true,
	}, 20000)
	if err != nil {
		t.Fatalf("job_stop include_children on resumed job: %v", err)
	}
	var stop jobStopResult
	if err := json.Unmarshal([]byte(stopOut), &stop); err != nil {
		t.Fatalf("unmarshal job_stop: %v (output: %s)", err, stopOut)
	}
	if stop.JobID != res.JobID {
		t.Fatalf("job_stop result = %+v, want resumed job %q", stop, res.JobID)
	}
	waitForShellDone(t, child.jobManager, nested.JobID)
	stoppedNested := loadShellRecord(t, child.jobManager, nested.JobID)
	if stoppedNested.Status != jobstore.StatusCancelled || stoppedNested.Reason != "stopped_by_parent" {
		t.Fatalf("nested record after include_children = %+v, want cancelled/stopped_by_parent", stoppedNested)
	}
}

func TestReconstructDelegateRuntimeCollisionReusesTrackedChild(t *testing.T) {
	adapter := &fakeAdapter{name: "openai"}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	markStoredDelegateResumable(t, s, rec)
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	childID := rec.DelegateRestore.ChildSessionID
	preflight := requireDelegateRestorePreflight(t, s, rec)

	first, err := s.restoreTerminalDelegateChild(rec, childID, preflight)
	if err != nil {
		t.Fatalf("first restoreTerminalDelegateChild: %v", err)
	}
	second, err := s.restoreTerminalDelegateChild(rec, childID, preflight)
	if err != nil {
		t.Fatalf("second restoreTerminalDelegateChild: %v", err)
	}

	if second != first {
		t.Fatalf("second reconstruction returned %p, want existing tracked child %p", second, first)
	}
	if got := s.subagents.get(childID); got != first {
		t.Fatalf("tracked child = %p, want original reconstructed child %p", got, first)
	}
	if requests := adapter.Requests(); len(requests) != 0 {
		t.Fatalf("adapter requests = %+v, want no model calls during reconstruction", requests)
	}
}

func TestJobSendMessageReconstructsDelegateFrozenSkills(t *testing.T) {
	var request llm.Request
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				request = req
				return communicateWithDefaultOutput("restored skill complete")
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	childID := rec.DelegateRestore.ChildSessionID
	childWorkDir := t.TempDir()
	skillDir := filepath.Join(childWorkDir, "skills", "review-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	const skillBody = "Use the frozen reviewer checklist before responding."
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: review-skill\ndescription: Review guidance\n---\n"+skillBody+"\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	rec.DelegateRestore.WorkingDir = childWorkDir
	rec.DelegateRestore.FrozenSkillNames = []string{"review-skill"}
	rec.DelegateRestore.FrozenSkillBodies = []string{skillBody}
	replaceStoredDelegateRecord(t, s, rec)
	parentMeta := s.Meta()
	stateDir := s.stateDir
	s.Close()

	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), parentMeta, RestoreSessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(func() { restored.Close() })

	res := restored.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         rec.JobID,
		Message:        "continue with the checklist",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}

	sub := restored.subagents.get(childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("reconstructed child runtime = %+v, want retained child session", sub)
	}
	if bodies := sub.sess.cfg.spawn.activatedSkillBodies; len(bodies) != 1 || !strings.Contains(bodies[0], skillBody) {
		t.Fatalf("activated skill bodies = %#v, want frozen review-skill body", bodies)
	}
	if !requestMessagesContain(request, skillBody) {
		t.Fatalf("first restored request missing frozen skill body %q: %+v", skillBody, request.Messages)
	}
}

func TestJobSendMessageReconstructsDelegateFrozenSkillBodiesFromDescriptor(t *testing.T) {
	var request llm.Request
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				request = req
				return communicateWithDefaultOutput("restored descriptor skill complete")
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	childID := rec.DelegateRestore.ChildSessionID
	childWorkDir := t.TempDir()
	skillDir := filepath.Join(childWorkDir, "skills", "review-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	const frozenBody = "Use the descriptor-frozen reviewer checklist before responding."
	const currentBody = "Use the changed current reviewer checklist instead."
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: review-skill\ndescription: Review guidance\n---\n"+currentBody+"\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	rec.DelegateRestore.WorkingDir = childWorkDir
	rec.DelegateRestore.FrozenSkillNames = []string{"review-skill"}
	rec.DelegateRestore.FrozenSkillBodies = []string{frozenBody}
	replaceStoredDelegateRecord(t, s, rec)
	parentMeta := s.Meta()
	stateDir := s.stateDir
	s.Close()

	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), parentMeta, RestoreSessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(func() { restored.Close() })

	res := restored.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         rec.JobID,
		Message:        "continue with the descriptor checklist",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}

	sub := restored.subagents.get(childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("reconstructed child runtime = %+v, want retained child session", sub)
	}
	bodies := sub.sess.cfg.spawn.activatedSkillBodies
	if len(bodies) != 1 || !strings.Contains(bodies[0], frozenBody) {
		t.Fatalf("activated skill bodies = %#v, want descriptor frozen body", bodies)
	}
	if strings.Contains(bodies[0], currentBody) {
		t.Fatalf("activated skill body used current skill file content: %#v", bodies)
	}
	if !requestMessagesContain(request, frozenBody) {
		t.Fatalf("first restored request missing descriptor frozen skill body %q: %+v", frozenBody, request.Messages)
	}
	if requestMessagesContain(request, currentBody) {
		t.Fatalf("first restored request used current skill file body %q: %+v", currentBody, request.Messages)
	}
}

func TestFailedPreflightDoesNotReconstructDelegateRuntime(t *testing.T) {
	adapter := &fakeAdapter{name: "openai"}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	childID := rec.DelegateRestore.ChildSessionID
	removeChildSessionMeta(t, s, rec)
	beforeEvents := len(loadJobStoreEvents(t, s.jobManager))
	beforeJobs := len(s.jobManager.list(listFilter{Type: jobstore.JobDelegate}))

	res := s.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  rec.JobID,
		Message: "resume",
	})

	if res.Err == nil || res.Err.Error() != "target_not_resumable:missing_child_session_meta" {
		t.Fatalf("error = %v, want missing_child_session_meta", res.Err)
	}
	if got := len(loadJobStoreEvents(t, s.jobManager)); got != beforeEvents {
		t.Fatalf("jobstore event count = %d, want unchanged %d", got, beforeEvents)
	}
	if got := len(s.jobManager.list(listFilter{Type: jobstore.JobDelegate})); got != beforeJobs {
		t.Fatalf("delegate job count = %d, want unchanged %d", got, beforeJobs)
	}
	if sub := s.subagents.get(childID); sub != nil {
		t.Fatalf("retained child runtime = %+v, want none after failed preflight", sub)
	}
	if requests := adapter.Requests(); len(requests) != 0 {
		t.Fatalf("adapter requests = %+v, want none", requests)
	}
}

func TestReconstructDelegateRuntimeMissingRequiredToolsFailsBeforeTracking(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*jobstore.DelegateRestoreDescriptor)
	}{
		{
			name: "frozen tool missing",
			mutate: func(desc *jobstore.DelegateRestoreDescriptor) {
				desc.FrozenToolNames = []string{"missing_frozen_tool"}
			},
		},
		{
			name: "explicit grant missing",
			mutate: func(desc *jobstore.DelegateRestoreDescriptor) {
				desc.ExplicitToolGrants = []string{"missing_explicit_tool"}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter := &fakeAdapter{name: "openai"}
			c := llm.NewClient()
			c.Register(adapter)
			s := newDelegateRestorePreflightSession(t, c)
			rec := seedStoppedDelegateRestoreRecord(t, s)
			childID := rec.DelegateRestore.ChildSessionID
			tc.mutate(rec.DelegateRestore)
			replaceStoredDelegateRecord(t, s, rec)
			rec = loadShellRecord(t, s.jobManager, rec.JobID)
			beforeEvents := len(loadJobStoreEvents(t, s.jobManager))
			beforeJobs := len(s.jobManager.list(listFilter{Type: jobstore.JobDelegate}))

			res := s.sendDelegateMessage(context.Background(), sendMessageArgs{
				Target:  rec.JobID,
				Message: "resume",
			})

			if res.Err == nil {
				t.Fatal("sendDelegateMessage succeeded, want missing tool failure")
			}
			errText := res.Err.Error()
			if !strings.Contains(errText, "target_not_resumable") || !strings.Contains(errText, "missing_") {
				t.Fatalf("error = %v, want target_not_resumable with missing tool name", res.Err)
			}
			if got := len(loadJobStoreEvents(t, s.jobManager)); got != beforeEvents {
				t.Fatalf("jobstore event count = %d, want unchanged %d", got, beforeEvents)
			}
			if got := len(s.jobManager.list(listFilter{Type: jobstore.JobDelegate})); got != beforeJobs {
				t.Fatalf("delegate job count = %d, want unchanged %d", got, beforeJobs)
			}
			if sub := s.subagents.get(childID); sub != nil {
				t.Fatalf("retained child runtime = %+v, want none after missing tool failure", sub)
			}
			if requests := adapter.Requests(); len(requests) != 0 {
				t.Fatalf("adapter requests = %+v, want none", requests)
			}
		})
	}
}

func TestReconstructDelegateMissingToolsDoesNotRunChildRestoreWatchRetry(t *testing.T) {
	adapter := &fakeAdapter{name: "openai"}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	markStoredDelegateResumable(t, s, rec)
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	childID := rec.DelegateRestore.ChildSessionID
	rec.DelegateRestore.FrozenToolNames = []string{"missing_frozen_tool"}
	replaceStoredDelegateRecord(t, s, rec)
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	now := time.Unix(3200, 0).UTC()
	appendSessionJobEvents(t, s.stateDir, childID, restoredWatchSendPendingEvents(childID, "job_child_observed", "caller", now)...)
	beforeParentEvents := len(loadJobStoreEvents(t, s.jobManager))
	beforeChildEvents := len(loadSessionJobStoreEvents(t, s.stateDir, childID))
	if pending := loadSessionWatchSendRecord(t, s.stateDir, childID).Pending; len(pending) != 1 {
		t.Fatalf("child pending before reconstruction = %+v, want 1", pending)
	}

	res := s.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  rec.JobID,
		Message: "resume",
	})

	if res.Err == nil || !strings.Contains(res.Err.Error(), "missing_frozen_tool") {
		t.Fatalf("error = %v, want missing frozen tool failure", res.Err)
	}
	if got := len(loadJobStoreEvents(t, s.jobManager)); got != beforeParentEvents {
		t.Fatalf("parent jobstore event count = %d, want unchanged %d", got, beforeParentEvents)
	}
	if got := len(loadSessionJobStoreEvents(t, s.stateDir, childID)); got != beforeChildEvents {
		t.Fatalf("child jobstore event count = %d, want unchanged %d", got, beforeChildEvents)
	}
	if pending := loadSessionWatchSendRecord(t, s.stateDir, childID).Pending; len(pending) != 1 {
		t.Fatalf("child pending after failed reconstruction = %+v, want unchanged pending watch send", pending)
	}
	for _, entry := range s.SteeringQueueSnapshot() {
		if strings.Contains(entry.Text, "delivery_restore_pending") || strings.Contains(entry.Text, "restored observe") {
			t.Fatalf("failed reconstruction delivered child watch frame to parent: queue = %+v", s.SteeringQueueSnapshot())
		}
	}
	if sub := s.subagents.get(childID); sub != nil {
		t.Fatalf("retained child runtime = %+v, want none after missing tool failure", sub)
	}
	if requests := adapter.Requests(); len(requests) != 0 {
		t.Fatalf("adapter requests = %+v, want none", requests)
	}
}

func TestReconstructDelegateChildRegistryMismatchDoesNotRunRestoreSideEffects(t *testing.T) {
	adapter := &fakeAdapter{name: "openai"}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateRestorePreflightSession(t, c)
	if err := s.reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name:        "parent_only_tool",
			Description: "registered only on the parent test session",
		}},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("register parent-only tool: %v", err)
	}
	rec := seedStoppedDelegateRestoreRecord(t, s)
	markStoredDelegateResumable(t, s, rec)
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	childID := rec.DelegateRestore.ChildSessionID
	rec.DelegateRestore.FrozenToolNames = []string{"parent_only_tool"}
	replaceStoredDelegateRecord(t, s, rec)
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	now := time.Unix(3300, 0).UTC()
	appendSessionJobEvents(t, s.stateDir, childID, restoredWatchSendPendingEvents(childID, "job_child_observed", "caller", now)...)
	beforeParentEvents := len(loadJobStoreEvents(t, s.jobManager))
	beforeChildEvents := len(loadSessionJobStoreEvents(t, s.stateDir, childID))

	res := s.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  rec.JobID,
		Message: "resume",
	})

	if res.Err == nil || !strings.Contains(res.Err.Error(), "parent_only_tool") {
		t.Fatalf("error = %v, want parent_only_tool failure", res.Err)
	}
	if got := len(loadJobStoreEvents(t, s.jobManager)); got != beforeParentEvents {
		t.Fatalf("parent jobstore event count = %d, want unchanged %d", got, beforeParentEvents)
	}
	if got := len(loadSessionJobStoreEvents(t, s.stateDir, childID)); got != beforeChildEvents {
		t.Fatalf("child jobstore event count = %d, want unchanged %d", got, beforeChildEvents)
	}
	if pending := loadSessionWatchSendRecord(t, s.stateDir, childID).Pending; len(pending) != 1 {
		t.Fatalf("child pending after failed reconstruction = %+v, want unchanged pending watch send", pending)
	}
	for _, entry := range s.SteeringQueueSnapshot() {
		if strings.Contains(entry.Text, "delivery_restore_pending") || strings.Contains(entry.Text, "restored observe") {
			t.Fatalf("failed post-restore validation delivered child watch frame to parent: queue = %+v", s.SteeringQueueSnapshot())
		}
	}
	if sub := s.subagents.get(childID); sub != nil {
		t.Fatalf("retained child runtime = %+v, want none after child registry validation failure", sub)
	}
	if requests := adapter.Requests(); len(requests) != 0 {
		t.Fatalf("adapter requests = %+v, want none", requests)
	}
}

func TestReconstructDelegateChildRegistryMismatchDoesNotReconcileChildJobs(t *testing.T) {
	adapter := &fakeAdapter{name: "openai"}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateRestorePreflightSession(t, c)
	if err := s.reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name:        "parent_only_tool",
			Description: "registered only on the parent test session",
		}},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("register parent-only tool: %v", err)
	}
	rec := seedStoppedDelegateRestoreRecord(t, s)
	markStoredDelegateResumable(t, s, rec)
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	childID := rec.DelegateRestore.ChildSessionID
	rec.DelegateRestore.FrozenToolNames = []string{"parent_only_tool"}
	replaceStoredDelegateRecord(t, s, rec)
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	startedAt := time.Unix(3600, 0).UTC()
	appendSessionJobEvents(t, s.stateDir, childID, jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               startedAt,
		JobID:            "job_child_nested_running",
		Type:             jobstore.JobShell,
		Command:          "sleep 999",
		Description:      "nested child job that must not reconcile on failed reconstruction",
		OwnerSessionID:   childID,
		VisibleToSession: childID,
		StartedAt:        &startedAt,
	})
	beforeChildEvents := loadSessionJobStoreEvents(t, s.stateDir, childID)

	res := s.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  rec.JobID,
		Message: "resume",
	})

	if res.Err == nil || !strings.Contains(res.Err.Error(), "parent_only_tool") {
		t.Fatalf("error = %v, want parent_only_tool failure", res.Err)
	}
	afterChildEvents := loadSessionJobStoreEvents(t, s.stateDir, childID)
	if len(afterChildEvents) != len(beforeChildEvents) {
		t.Fatalf("child jobstore event count = %d, want unchanged %d; events = %+v", len(afterChildEvents), len(beforeChildEvents), afterChildEvents)
	}
	for _, event := range afterChildEvents {
		if event.JobID == "job_child_nested_running" && (event.Kind == jobstore.EventJobFinished || event.Kind == jobstore.EventJobNotificationPending) {
			t.Fatalf("failed reconstruction reconciled nested child job: %+v", event)
		}
	}
	if sub := s.subagents.get(childID); sub != nil {
		t.Fatalf("retained child runtime = %+v, want none after child registry validation failure", sub)
	}
	if requests := adapter.Requests(); len(requests) != 0 {
		t.Fatalf("adapter requests = %+v, want none", requests)
	}
}

func TestConcurrentDelegateReconstructionRunsRestoreSideEffectsOnce(t *testing.T) {
	adapter := &fakeAdapter{name: "openai"}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	markStoredDelegateResumable(t, s, rec)
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	childID := rec.DelegateRestore.ChildSessionID
	hookMarker := filepath.Join(t.TempDir(), "session-start-hook")
	pluginDir := sessionStartHookPlugin(t, "sh -c "+shellQuote("echo hook >> "+hookMarker+"; sleep 0.2"))
	meta, err := schema.LoadSessionMeta(s.stateDir, childID)
	if err != nil {
		t.Fatalf("load child meta: %v", err)
	}
	meta.Config.PluginDirs = []string{pluginDir}
	if err := schema.SaveSessionMeta(s.stateDir, meta); err != nil {
		t.Fatalf("save child meta: %v", err)
	}
	now := time.Unix(3400, 0).UTC()
	appendSessionJobEvents(t, s.stateDir, childID, restoredWatchSendPendingEvents(childID, "job_child_observed", "caller", now)...)
	preflight := requireDelegateRestorePreflight(t, s, rec)
	// Caller sends restore as wake tokens, not synchronous steering frames; count
	// how many caller tokens the winning reconstruction enqueues. Restore side
	// effects run exactly once, so exactly one token is surfaced. The hook runs on
	// the winning child only; install the counter on its jobManager there.
	var callerTokens int32
	s.delegateRestoreBeforeSideEffects = func(child *Session) {
		origEnqueue := child.jobManager.enqueue
		child.jobManager.enqueue = func(n jobNotification) {
			if n.WatchSend != nil && n.WatchSend.Key.ResolvedSendTo == runtimeMessageAliasCaller {
				atomic.AddInt32(&callerTokens, 1)
			}
			if origEnqueue != nil {
				origEnqueue(n)
			}
		}
	}
	start := make(chan struct{})
	type restoreResult struct {
		sub *subagent
		err error
	}
	results := make(chan restoreResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			sub, err := s.restoreTerminalDelegateChild(rec, childID, preflight)
			results <- restoreResult{sub: sub, err: err}
		}()
	}
	close(start)

	first := <-results
	second := <-results

	if first.err != nil {
		t.Fatalf("first restore error: %v", first.err)
	}
	if second.err != nil {
		t.Fatalf("second restore error: %v", second.err)
	}
	if first.sub == nil || second.sub == nil || first.sub != second.sub {
		t.Fatalf("reconstruction results = %p and %p, want same retained child", first.sub, second.sub)
	}
	if got := atomic.LoadInt32(&callerTokens); got != 1 {
		t.Fatalf("caller watch-send tokens enqueued = %d, want exactly one (side effects run once)", got)
	}
	if got := countFileLines(t, hookMarker); got != 1 {
		t.Fatalf("SessionStart hook executions = %d, want 1", got)
	}
	if got := countSteeringEntriesContaining(s, "delivery_restore_pending"); got != 0 {
		t.Fatalf("parent watch-frame steering deliveries = %d, want 0 (caller sends never steer); queue = %+v", got, s.SteeringQueueSnapshot())
	}
	if pending := loadSessionWatchSendRecord(t, s.stateDir, childID).Pending; len(pending) != 1 {
		t.Fatalf("child pending after winning reconstruction = %+v, want still pending (token surfaced, not settled)", pending)
	}
}

func TestDelegateReconstructionRacingParentCloseDoesNotTrackOrRunSideEffects(t *testing.T) {
	adapter := &fakeAdapter{name: "openai"}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	markStoredDelegateResumable(t, s, rec)
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	childID := rec.DelegateRestore.ChildSessionID
	hookMarker := filepath.Join(t.TempDir(), "session-start-hook")
	pluginDir := sessionStartHookPlugin(t, "sh -c "+shellQuote("echo hook >> "+hookMarker))
	meta, err := schema.LoadSessionMeta(s.stateDir, childID)
	if err != nil {
		t.Fatalf("load child meta: %v", err)
	}
	meta.Config.PluginDirs = []string{pluginDir}
	if err := schema.SaveSessionMeta(s.stateDir, meta); err != nil {
		t.Fatalf("save child meta: %v", err)
	}
	now := time.Unix(3500, 0).UTC()
	appendSessionJobEvents(t, s.stateDir, childID, restoredWatchSendPendingEvents(childID, "job_child_observed", "caller", now)...)
	beforeParentEvents := len(loadJobStoreEvents(t, s.jobManager))
	beforeChildEvents := len(loadSessionJobStoreEvents(t, s.stateDir, childID))
	paused := make(chan struct{})
	release := make(chan struct{})
	var pauseOnce sync.Once
	s.delegateRestoreBeforeTrack = func() {
		pauseOnce.Do(func() { close(paused) })
		<-release
	}

	done := make(chan sendMessageResult, 1)
	go func() {
		done <- s.sendDelegateMessage(context.Background(), sendMessageArgs{
			Target:  rec.JobID,
			Message: "resume while parent closes",
		})
	}()
	select {
	case <-paused:
	case <-time.After(2 * time.Second):
		t.Fatal("reconstruction did not pause before parent tracking")
	}
	closeDone := make(chan struct{})
	go func() {
		s.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
		t.Fatal("parent Close returned before paused reconstruction reached tracking")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("parent Close did not return after releasing reconstruction")
	}

	res := <-done
	if res.Err == nil || !strings.Contains(res.Err.Error(), "session is closed") {
		t.Fatalf("sendDelegateMessage error = %v, want session is closed", res.Err)
	}
	if sub := s.subagents.get(childID); sub != nil {
		t.Fatalf("retained child runtime after parent close race = %+v, want none", sub)
	}
	if got := countSteeringEntriesContaining(s, "delivery_restore_pending"); got != 0 {
		t.Fatalf("parent watch-frame steering deliveries = %d, want 0; queue = %+v", got, s.SteeringQueueSnapshot())
	}
	if got := countFileLines(t, hookMarker); got != 0 {
		t.Fatalf("SessionStart hook executions = %d, want 0", got)
	}
	if got := len(loadSessionJobStoreEvents(t, s.stateDir, childID)); got != beforeChildEvents {
		t.Fatalf("child jobstore event count = %d, want unchanged %d", got, beforeChildEvents)
	}
	if pending := loadSessionWatchSendRecord(t, s.stateDir, childID).Pending; len(pending) != 1 {
		t.Fatalf("child pending after parent close race = %+v, want unchanged pending watch send", pending)
	}
	if got := len(loadJobStoreEvents(t, s.jobManager)); got != beforeParentEvents {
		t.Fatalf("parent jobstore event count = %d, want unchanged %d", got, beforeParentEvents)
	}
	if requests := adapter.Requests(); len(requests) != 0 {
		t.Fatalf("adapter requests = %+v, want none", requests)
	}
}

func TestParentCloseWaitsForInFlightDelegateReconstructionClaim(t *testing.T) {
	adapter := &fakeAdapter{name: "openai"}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	markStoredDelegateResumable(t, s, rec)
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	childID := rec.DelegateRestore.ChildSessionID
	now := time.Unix(3800, 0).UTC()
	appendSessionJobEvents(t, s.stateDir, childID, restoredWatchSendPendingEvents(childID, "job_child_observed", "caller", now)...)
	beforeParentEvents := len(loadJobStoreEvents(t, s.jobManager))
	beforeChildEvents := len(loadSessionJobStoreEvents(t, s.stateDir, childID))
	paused := make(chan struct{})
	release := make(chan struct{})
	var pauseOnce sync.Once
	s.delegateRestoreAfterClaim = func() {
		pauseOnce.Do(func() { close(paused) })
		<-release
	}

	sendDone := make(chan sendMessageResult, 1)
	go func() {
		sendDone <- s.sendDelegateMessage(context.Background(), sendMessageArgs{
			Target:  rec.JobID,
			Message: "resume while close waits for reconstruction claim",
		})
	}()
	select {
	case <-paused:
	case <-time.After(2 * time.Second):
		t.Fatal("reconstruction did not pause after claim")
	}
	closeDone := make(chan struct{})
	go func() {
		s.Close()
		close(closeDone)
	}()
	closeReturnedBeforeRelease := false
	select {
	case <-closeDone:
		closeReturnedBeforeRelease = true
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("parent Close did not return after releasing reconstruction")
	}
	res := <-sendDone
	if closeReturnedBeforeRelease {
		t.Fatal("parent Close returned before in-flight reconstruction claim finished")
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "session is closed") {
		t.Fatalf("sendDelegateMessage error = %v, want session is closed", res.Err)
	}
	if sub := s.subagents.get(childID); sub != nil {
		t.Fatalf("retained child runtime after parent close race = %+v, want none", sub)
	}
	if got := len(loadJobStoreEvents(t, s.jobManager)); got != beforeParentEvents {
		t.Fatalf("parent jobstore event count = %d, want unchanged %d", got, beforeParentEvents)
	}
	if got := len(loadSessionJobStoreEvents(t, s.stateDir, childID)); got != beforeChildEvents {
		t.Fatalf("child jobstore event count = %d, want unchanged %d", got, beforeChildEvents)
	}
	if got := countSteeringEntriesContaining(s, "delivery_restore_pending"); got != 0 {
		t.Fatalf("parent watch-frame steering deliveries = %d, want 0; queue = %+v", got, s.SteeringQueueSnapshot())
	}
	if requests := adapter.Requests(); len(requests) != 0 {
		t.Fatalf("adapter requests = %+v, want none", requests)
	}
}

func TestDelegateReconstructionParentCloseBeforeDeferredSideEffectsDoesNotRunThem(t *testing.T) {
	adapter := &fakeAdapter{name: "openai"}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	markStoredDelegateResumable(t, s, rec)
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	childID := rec.DelegateRestore.ChildSessionID
	now := time.Unix(3700, 0).UTC()
	appendSessionJobEvents(t, s.stateDir, childID, restoredWatchSendPendingEvents(childID, "job_child_observed", "caller", now)...)
	beforeChildEvents := len(loadSessionJobStoreEvents(t, s.stateDir, childID))
	beforeParentEvents := len(loadJobStoreEvents(t, s.jobManager))
	pauseReached := false
	s.delegateRestoreBeforeSideEffects = func(child *Session) {
		pauseReached = true
		if drained := s.subagents.drainForClose(); len(drained) != 1 || drained[0].sess != child {
			t.Fatalf("drained subagents = %+v, want reconstructed child", drained)
		}
	}

	res := s.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  rec.JobID,
		Message: "resume while parent closes before side effects",
	})

	if res.Err == nil || !strings.Contains(res.Err.Error(), "session is closed") {
		t.Fatalf("sendDelegateMessage error = %v, want session is closed", res.Err)
	}
	if !pauseReached {
		t.Fatal("test did not reach deferred side-effect pause")
	}
	if got := len(loadSessionJobStoreEvents(t, s.stateDir, childID)); got != beforeChildEvents {
		t.Fatalf("child jobstore event count = %d, want unchanged %d", got, beforeChildEvents)
	}
	if got := countSteeringEntriesContaining(s, "delivery_restore_pending"); got != 0 {
		t.Fatalf("parent watch-frame steering deliveries = %d, want 0; queue = %+v", got, s.SteeringQueueSnapshot())
	}
	if got := len(loadJobStoreEvents(t, s.jobManager)); got != beforeParentEvents {
		t.Fatalf("parent jobstore event count = %d, want unchanged %d", got, beforeParentEvents)
	}
	if sub := s.subagents.get(childID); sub != nil {
		t.Fatalf("retained child runtime after parent close race = %+v, want none", sub)
	}
	if requests := adapter.Requests(); len(requests) != 0 {
		t.Fatalf("adapter requests = %+v, want none", requests)
	}
}

// TestDelegateReconstructionSideEffectFailureAfterWatchSendKeepsRuntime
// re-anchors TestDelegateReconstructionSideEffectFailureAfterWatchDeliveryKeepsRuntime.
// The old test asserted the restored caller watch send was DELIVERED synchronously
// (a watch_send_delivered event + a steering frame) before a later notification-arm
// failure, and that the failure kept the reconstructed runtime. Restore no longer
// delivers caller sends synchronously: retryRestoredPendingWatchSends enqueues a
// caller wake token and the durable watch_send_pending survives until a later accept
// settles it (spec §4.3). The preserved invariant: the watch-send restore side effect
// runs and durably survives (now: token enqueued + pending intact) BEFORE the injected
// notification-arm failure, and that failure keeps the runtime retained. Assertions
// mapped to the replacement mechanism: delivered→token-enqueued, delivered-event→
// pending-survives; the runtime-retained / no-orphan-notification / no-model-call
// assertions are preserved verbatim.
func TestDelegateReconstructionSideEffectFailureAfterWatchSendKeepsRuntime(t *testing.T) {
	adapter := &fakeAdapter{name: "openai"}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	markStoredDelegateResumable(t, s, rec)
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	childID := rec.DelegateRestore.ChildSessionID
	now := time.Unix(3900, 0).UTC()
	appendSessionJobEvents(t, s.stateDir, childID, restoredWatchSendPendingEvents(childID, "job_child_observed", "caller", now)...)
	appendChildCompletedJobNeedingNotification(t, s.stateDir, childID, "job_child_completed_needs_notify", now.Add(time.Second))
	preflight := requireDelegateRestorePreflight(t, s, rec)
	sawCallerToken := false
	s.delegateRestoreBeforeSideEffects = func(child *Session) {
		origEnqueue := child.jobManager.enqueue
		child.jobManager.enqueue = func(n jobNotification) {
			if n.WatchSend != nil && n.WatchSend.Key.ResolvedSendTo == runtimeMessageAliasCaller {
				sawCallerToken = true
			}
			if origEnqueue != nil {
				origEnqueue(n)
			}
		}
		origAppend := child.jobManager.appendEvent
		child.jobManager.appendEvent = func(event jobstore.Event) error {
			// The caller watch-send side effect runs (token enqueued) before the
			// notification arm; fail the arm to exercise the keep-runtime path.
			if sawCallerToken && event.Kind == jobstore.EventJobNotificationPending {
				return errors.New("injected notification arm failure after watch send")
			}
			return origAppend(event)
		}
	}

	sub, err := s.restoreTerminalDelegateChild(rec, childID, preflight)

	if err != nil {
		t.Fatalf("restoreTerminalDelegateChild error = %v, want retained runtime after committed watch send", err)
	}
	if sub == nil || sub.sess == nil {
		t.Fatalf("reconstructed child = %+v, want retained runtime", sub)
	}
	if got := s.subagents.get(childID); got != sub {
		t.Fatalf("tracked child = %+v, want reconstructed child %p", got, sub)
	}
	if !sawCallerToken {
		t.Fatal("restore did not enqueue the caller watch-send token before the injected failure")
	}
	events := loadSessionJobStoreEvents(t, s.stateDir, childID)
	if !hasWatchSendEvent(events, jobstore.EventWatchSendPending, "delivery_restore_pending") {
		t.Fatalf("child jobstore missing durable pending watch send after notification-arm failure: %+v", events)
	}
	if hasJobEvent(events, jobstore.EventJobNotificationPending, "job_child_completed_needs_notify") {
		t.Fatalf("injected notification failure still appended notification pending: %+v", events)
	}
	if requests := adapter.Requests(); len(requests) != 0 {
		t.Fatalf("adapter requests = %+v, want no model calls during reconstruction", requests)
	}
}

// TestDelegateReconstructionWatchSendToCallerDuringParentCloseStaysPending
// proves that a restored child caller pending is NOT delivered when the parent
// is closing. The old test interposed parentWatchSteerDelivered to pause the
// synchronous caller delivery mid-restore and confirm it stayed pending. Caller
// sends are now notification tokens (spec §4.3): restore enqueues a token but
// only the parent's acceptNotificationInput renders and settles it. Marking the
// parent closing before the child's deferred side effects run means no accept
// ever runs, so the durable pending survives unsettled — the same outcome the old
// test asserted, via the new mechanism. Every original assertion is preserved
// (no watch_send_delivered, pending unchanged, nothing on parent steering, parent
// jobstore unchanged, no model call).
func TestDelegateReconstructionWatchSendToCallerDuringParentCloseStaysPending(t *testing.T) {
	adapter := &fakeAdapter{name: "openai"}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	markStoredDelegateResumable(t, s, rec)
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	childID := rec.DelegateRestore.ChildSessionID
	now := time.Unix(4000, 0).UTC()
	appendSessionJobEvents(t, s.stateDir, childID, restoredWatchSendPendingEvents(childID, "job_child_observed", "caller", now)...)
	beforeParentEvents := len(loadJobStoreEvents(t, s.jobManager))
	preflight := requireDelegateRestorePreflight(t, s, rec)
	s.delegateRestoreBeforeSideEffects = func(_ *Session) {
		// The parent closes mid-restore, before the child's deferred watch-send
		// side effects run: no accept boundary can ever render/settle the token.
		s.mu.Lock()
		s.closing = true
		s.state = SessionClosed
		s.mu.Unlock()
	}

	if _, err := s.restoreTerminalDelegateChild(rec, childID, preflight); err != nil {
		t.Fatalf("restoreTerminalDelegateChild error = %v, want nil", err)
	}

	events := loadSessionJobStoreEvents(t, s.stateDir, childID)
	if hasWatchSendEvent(events, jobstore.EventWatchSendDelivered, "delivery_restore_pending") {
		t.Fatalf("watch send was marked delivered while parent was closing: %+v", events)
	}
	if pending := loadSessionWatchSendRecord(t, s.stateDir, childID).Pending; len(pending) != 1 {
		t.Fatalf("child pending after parent-close caller delivery = %+v, want unchanged pending watch send", pending)
	}
	if got := countSteeringEntriesContaining(s, "delivery_restore_pending"); got != 0 {
		t.Fatalf("parent watch-frame steering deliveries = %d, want 0; queue = %+v", got, s.SteeringQueueSnapshot())
	}
	if got := len(loadJobStoreEvents(t, s.jobManager)); got != beforeParentEvents {
		t.Fatalf("parent jobstore event count = %d, want unchanged %d", got, beforeParentEvents)
	}
	if requests := adapter.Requests(); len(requests) != 0 {
		t.Fatalf("adapter requests = %+v, want no model calls during reconstruction", requests)
	}
}

// TestWatchCallerDeliverySuppressedDuringProcessingDeliversAtAcceptBoundary
// re-anchors the old TestWatchCallerDeliveryWaitsForToolResultsBoundary onto the
// notification rail. The old test asserted caller delivery was SUPPRESSED during
// active processing / before tool results and SUCCEEDED at idle. Caller sends are
// now notification tokens: observation/drains enqueue a token but NEVER append to
// the steering queue, and the token renders only at the loop-owned accept
// boundary. The boundary-safety that the old "waits for tool results" check
// provided is now structural — acceptNotificationInput runs repairOrphanedToolResults
// itself, so it is safe regardless of tool-results state; the preserved analog is
// "nothing lands on steering mid-processing; the frame renders at the accept turn".
// Both original assertions survive: suppressed-during-processing AND delivered-when-idle.
func TestWatchCallerDeliverySuppressedDuringProcessingDeliversAtAcceptBoundary(t *testing.T) {
	s := newPersistentTestSession(t)
	now := time.Unix(4300, 0).UTC()
	if err := s.jobManager.appendWatchSendEvents(restoredWatchSendPendingEvents(s.ID(), "job_child_observed", "caller", now)); err != nil {
		t.Fatalf("append pending watch send: %v", err)
	}
	if err := s.jobManager.restoreWatchSendPending(); err != nil {
		t.Fatalf("restore watch sends: %v", err)
	}

	// Active processing with an orphaned tool call (the old "before tool results"
	// shape): a drain enqueues a token but must not append to the steering queue
	// or settle the pending mid-processing.
	s.mu.Lock()
	s.state = SessionProcessing
	s.mu.Unlock()
	s.appendTurn(schema.TurnAssistant, llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        "call_1",
				Name:      "shell",
				Arguments: json.RawMessage(`{"command":"printf ready"}`),
			},
		}},
	})
	if err := s.drainPendingWatchSends(context.Background()); err != nil {
		t.Fatalf("drain while processing: %v", err)
	}
	if queue := s.SteeringQueueSnapshot(); len(queue) != 0 {
		t.Fatalf("steering queue while processing = %+v, want empty (caller sends never steer)", queue)
	}
	if got := countSteeringEntriesContaining(s, "delivery_restore_pending"); got != 0 {
		t.Fatalf("watch caller deliveries = %d, want 0 while processing", got)
	}
	if pending := loadWatchSendRecord(t, s.jobManager).Pending; len(pending) != 1 {
		t.Fatalf("pending while processing = %+v, want still pending (no mid-processing delivery)", pending)
	}

	// At idle, the loop-owned drain+accept renders the frame into the notification
	// turn and settles the pending.
	s.mu.Lock()
	s.state = SessionIdle
	s.mu.Unlock()
	drainAndAccept(t, s)
	if got := countSteeringEntriesContaining(s, "delivery_restore_pending"); got != 1 {
		t.Fatalf("watch caller deliveries = %d, want 1 after idle accept", got)
	}
	if pending := loadWatchSendRecord(t, s.jobManager).Pending; len(pending) != 0 {
		t.Fatalf("pending after idle accept = %+v, want settled", pending)
	}
}

// TestWatchCallerDeliverySurfacesAsTokenForNonPersistentSession re-anchors the
// old TestWatchCallerDeliveryFallsBackForNonPersistentSession. The old test
// asserted a non-persistent (no-transcript) session delivered the caller frame
// to the steering queue. Caller sends are now notification tokens: a
// non-persistent session still has a jobManager + enqueue, so the drain surfaces
// the caller frame as a wake token on the notification queue (it never renders a
// turn here — appendTurnDurably needs a transcript — but the token carries the
// durable pending's identity, which is how the frame reaches the owner). The
// preserved intent: a non-persistent session still surfaces the caller frame.
func TestWatchCallerDeliverySurfacesAsTokenForNonPersistentSession(t *testing.T) {
	s := newTestSession(t)
	if s.transcript != nil {
		t.Fatal("newTestSession unexpectedly persistent; test needs a no-transcript session")
	}
	now := time.Unix(4400, 0).UTC()
	if err := s.jobManager.appendWatchSendEvents(restoredWatchSendPendingEvents(s.ID(), "job_child_observed", "caller", now)); err != nil {
		t.Fatalf("append pending watch send: %v", err)
	}
	if err := s.jobManager.restoreWatchSendPending(); err != nil {
		t.Fatalf("restore watch sends: %v", err)
	}

	if err := s.drainPendingWatchSends(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if queue := s.SteeringQueueSnapshot(); len(queue) != 0 {
		t.Fatalf("steering queue = %+v, want empty (caller sends never steer)", queue)
	}

	tokens := s.drainJobNotifications()
	if len(tokens) != 1 || tokens[0].WatchSend == nil {
		t.Fatalf("notification queue = %+v, want one caller wake token", tokens)
	}
	_, _, state, ok := s.resolveWatchSendToken(tokens[0].WatchSend)
	if !ok {
		t.Fatalf("enqueued token did not resolve to a current pending: %+v", tokens[0].WatchSend)
	}
	if !strings.Contains(state.Frame, "delivery_restore_pending") {
		t.Fatalf("token frame = %q, want the non-persistent caller watch frame", state.Frame)
	}
}

// TestWatchCallerDeliveryRidesJobNotificationWake re-anchors the old
// TestWatchCallerDeliveryDoesNotUseJobNotificationWake, whose premise is now
// inverted by design: the old test asserted caller delivery did NOT use the
// notify wake (it was a synchronous steering-turn append). The mailbox design
// (spec §4.3) makes caller sends ride the notification rail, so enqueueing a
// caller wake token DOES trigger notify. This asserts the new truth: a drained
// caller send enqueues a token and fires the wake callback.
func TestWatchCallerDeliveryRidesJobNotificationWake(t *testing.T) {
	s := newPersistentTestSession(t)
	var notifyCalled bool
	s.SetNotifyFunc(func() { notifyCalled = true })
	now := time.Unix(4500, 0).UTC()
	if err := s.jobManager.appendWatchSendEvents(restoredWatchSendPendingEvents(s.ID(), "job_child_observed", "caller", now)); err != nil {
		t.Fatalf("append pending watch send: %v", err)
	}
	if err := s.jobManager.restoreWatchSendPending(); err != nil {
		t.Fatalf("restore watch sends: %v", err)
	}

	if err := s.drainPendingWatchSends(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if !notifyCalled {
		t.Fatal("caller watch send must ride the job notification wake")
	}
	if got := s.peekNotifications(); got != 1 {
		t.Fatalf("pending notifications = %d, want one caller wake token", got)
	}
}

func TestOrphanToolRepairRetriesPendingCallerWatchSends(t *testing.T) {
	s := newPersistentTestSession(t)
	s.appendTurn(schema.TurnAssistant, llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        "call_orphan",
				Name:      "shell",
				Arguments: json.RawMessage(`{"command":"printf ready"}`),
			},
		}},
	})
	now := time.Unix(4100, 0).UTC()
	if err := s.jobManager.appendWatchSendEvents(restoredWatchSendPendingEvents(s.ID(), "job_child_observed", "caller", now)); err != nil {
		t.Fatalf("append pending watch send: %v", err)
	}
	if err := s.jobManager.restoreWatchSendPending(); err != nil {
		t.Fatalf("restore watch sends: %v", err)
	}

	// Orphan repair drains pending watch sends, enqueuing the caller frame as a
	// notification token; the accept boundary renders it into the notification turn
	// (a TurnSteering reminder in history) and settles the pending.
	if repairs := s.repairOrphanedToolResults("test"); repairs != 1 {
		t.Fatalf("repairOrphanedToolResults repairs = %d, want 1", repairs)
	}
	s.acceptNotificationInput(context.Background())
	if got := countSteeringEntriesContaining(s, "delivery_restore_pending"); got != 1 {
		t.Fatalf("watch caller deliveries = %d, want 1 after orphan repair", got)
	}
	if pending := loadWatchSendRecord(t, s.jobManager).Pending; len(pending) != 0 {
		t.Fatalf("pending watch sends after orphan repair = %+v, want none", pending)
	}
}

func TestProcessingExitRetriesPendingCallerWatchSends(t *testing.T) {
	s := newPersistentTestSession(t)
	s.mu.Lock()
	s.state = SessionProcessing
	s.mu.Unlock()
	now := time.Unix(4200, 0).UTC()
	if err := s.jobManager.appendWatchSendEvents(restoredWatchSendPendingEvents(s.ID(), "job_late_watch", "caller", now)); err != nil {
		t.Fatalf("append pending watch send: %v", err)
	}
	if err := s.jobManager.restoreWatchSendPending(); err != nil {
		t.Fatalf("restore watch sends: %v", err)
	}

	// The processing-exit boundary drains pending watch sends (enqueuing the caller
	// frame as a notification token); the accept boundary renders it into the
	// notification turn and settles the pending.
	s.finishProcessingAtBoundary(context.Background(), SessionIdle)
	s.acceptNotificationInput(context.Background())

	if got := s.State(); got != SessionIdle {
		t.Fatalf("state = %q, want idle", got)
	}
	if got := countSteeringEntriesContaining(s, "delivery_restore_pending"); got != 1 {
		t.Fatalf("watch caller deliveries = %d, want 1 after processing exit", got)
	}
	if pending := loadWatchSendRecord(t, s.jobManager).Pending; len(pending) != 0 {
		t.Fatalf("pending watch sends after processing exit = %+v, want none", pending)
	}
}

func TestTerminalDelegateRestoreRequiresStrictPreflightBeforeReconstruction(t *testing.T) {
	cases := []struct {
		status jobstore.Status
		reason string
	}{
		{status: jobstore.StatusCompleted, reason: "exit_zero"},
		{status: jobstore.StatusCancelled, reason: "cancelled"},
		{status: jobstore.StatusFailed, reason: "failed"},
	}
	for _, tc := range cases {
		t.Run(string(tc.status), func(t *testing.T) {
			adapter := &fakeAdapter{name: "openai"}
			c := llm.NewClient()
			c.Register(adapter)
			s := newDelegateRestorePreflightSession(t, c)
			rec := seedStoppedDelegateRestoreRecord(t, s)
			setStoredDelegateTerminalStatus(t, s, rec, tc.status, tc.reason)
			markStoredDelegateResumable(t, s, rec)
			rec = loadShellRecord(t, s.jobManager, rec.JobID)
			childID := rec.DelegateRestore.ChildSessionID
			appendChildTranscript(t, s, rec, "\n{not-json}\n")
			beforeEvents := len(loadJobStoreEvents(t, s.jobManager))
			beforeJobs := len(s.jobManager.list(listFilter{Type: jobstore.JobDelegate}))

			res := s.sendDelegateMessage(context.Background(), sendMessageArgs{
				Target:  rec.JobID,
				Message: "resume",
			})

			if res.Err == nil || res.Err.Error() != "target_not_resumable:corrupt_child_transcript" {
				t.Fatalf("error = %v, want corrupt_child_transcript", res.Err)
			}
			if got := len(loadJobStoreEvents(t, s.jobManager)); got != beforeEvents {
				t.Fatalf("jobstore event count = %d, want unchanged %d", got, beforeEvents)
			}
			if got := len(s.jobManager.list(listFilter{Type: jobstore.JobDelegate})); got != beforeJobs {
				t.Fatalf("delegate job count = %d, want unchanged %d", got, beforeJobs)
			}
			if sub := s.subagents.get(childID); sub != nil {
				t.Fatalf("retained child runtime = %+v, want none after failed strict preflight", sub)
			}
			if requests := adapter.Requests(); len(requests) != 0 {
				t.Fatalf("adapter requests = %+v, want none", requests)
			}
		})
	}
}

func TestTerminalDelegateRestoreUsesStrictPreflightHistory(t *testing.T) {
	var request llm.Request
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				request = req
				return communicateWithDefaultOutput("resumed from strict history")
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	setStoredDelegateTerminalStatus(t, s, rec, jobstore.StatusCompleted, "exit_zero")
	markStoredDelegateResumable(t, s, rec)
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	childID := rec.DelegateRestore.ChildSessionID
	appendChildTranscriptTurn(t, s, rec, schema.NewTurn(schema.TurnUserInput, llm.User("retained transcript source")))
	original := delegateRestoreResumeHistory
	delegateRestoreResumeHistory = func(entries []transcript.Entry) []schema.Turn {
		if len(entries) != 1 {
			t.Fatalf("strict preflight entries = %d, want 1", len(entries))
		}
		return []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("strict preflight history marker"))}
	}
	defer func() { delegateRestoreResumeHistory = original }()

	res := s.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         rec.JobID,
		Message:        "resume valid terminal delegate",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})

	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	sub := s.subagents.get(childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("reconstructed child runtime = %+v, want retained child session", sub)
	}
	if !requestMessagesContain(request, "strict preflight history marker") {
		t.Fatalf("first restored request missing strict preflight history marker: %+v", request.Messages)
	}
	if !requestMessagesContain(request, "resume valid terminal delegate") {
		t.Fatalf("first restored request missing resumed input: %+v", request.Messages)
	}
}

func TestSendDelegateMessageRunningDelegateTargetSteersWithoutNewJob(t *testing.T) {
	adapter := &cancelAwareDelegateAdapter{name: "openai", started: make(chan struct{})}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "stay running",
		Background: true,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	select {
	case <-adapter.started:
	case <-time.After(2 * time.Second):
		t.Fatal("delegate child did not start")
	}
	before := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  first.JobID,
		Message: "please adjust course",
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Action != "sent" ||
		res.JobID != first.JobID ||
		res.Status != jobstore.StatusRunning ||
		!res.RunningInBackground ||
		res.TranscriptRef != first.TranscriptRef {
		t.Fatalf("result = %+v, want sent to running delegate job", res)
	}
	after := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(after) != len(before) {
		t.Fatalf("delegate jobs grew from %d to %d; jobs = %+v", len(before), len(after), after)
	}
	_, childID, err := decodeRef(first.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := sess.subagents.get(childID)
	if sub == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	queue := sub.sess.SteeringQueueSnapshot()
	if len(queue) != 1 || queue[0].Text != "please adjust course" {
		t.Fatalf("steering queue = %+v, want sent message", queue)
	}

	_, _ = sess.jobManager.stop(first.JobID)
	waitForShellDone(t, sess.jobManager, first.JobID)
}

func TestWatchOriginatedSendToRunningDelegateSteersAndMarksLifecycleFromWatch(t *testing.T) {
	adapter := &cancelAwareDelegateAdapter{name: "openai", started: make(chan struct{})}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "stay running",
		Background: true,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	select {
	case <-adapter.started:
	case <-time.After(2 * time.Second):
		t.Fatal("delegate child did not start")
	}
	_, _, err := decodeRef(first.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}

	seedCommonWatchSendTargets(t, sess.jobManager)
	if _, err := sess.jobManager.configureWatch(watchArgs{
		Target: "caller",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure watch: %v", err)
	}

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:    first.JobID,
		Message:   "watch-originated steer",
		FromWatch: true,
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Action != "sent" || res.JobID != first.JobID || res.Status != jobstore.StatusRunning || !res.RunningInBackground {
		t.Fatalf("result = %+v, want sent to running delegate job %s", res, first.JobID)
	}
	_, childID, err := decodeRef(first.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript ref: %v", err)
	}
	sub := sess.subagents.get(childID)
	if sub == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	queue := sub.sess.SteeringQueueSnapshot()
	if len(queue) != 1 || queue[0].Text != "watch-originated steer" {
		t.Fatalf("steering queue = %+v, want watch-originated steer", queue)
	}

	_, _ = sess.jobManager.stop(first.JobID)
	waitForShellDone(t, sess.jobManager, first.JobID)

	var end events.JobFinishedData
	for deadline := time.After(2 * time.Second); ; {
		select {
		case ev := <-sess.Events():
			data, ok := ev.Data.(events.JobFinishedData)
			if ok && data.JobID == first.JobID {
				end = data
				goto gotEnd
			}
		case <-deadline:
			t.Fatal("timed out waiting for job finished event")
		}
	}

gotEnd:
	if !end.FromWatch {
		t.Fatalf("watch-originated send did not mark existing job finished FromWatch; event = %+v", end)
	}
	if pending := loadWatchSendRecord(t, sess.jobManager).Pending; len(pending) != 0 {
		t.Fatalf("watch-originated running delegate completion recorded watch sends = %d, want 0: %+v", len(pending), pending)
	}
}

func TestSendDelegateMessageRunningTargetHoldsRunLockThroughSteer(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := &subagent{
		id:      child.ID(),
		sess:    child,
		running: true,
		status:  SubagentRunning,
		done:    make(chan struct{}),
	}
	parent.subagents.track(sub)
	rec := &jobstore.JobRecord{
		JobID:         "job_atomic_delegate",
		Type:          jobstore.JobDelegate,
		Status:        jobstore.StatusRunning,
		TranscriptRef: encodeRef("", child.ID()),
	}

	child.mu.Lock()
	childLocked := true
	t.Cleanup(func() {
		if childLocked {
			child.mu.Unlock()
		}
	})

	done := make(chan sendMessageResult, 1)
	go func() {
		done <- parent.sendRunningDelegateMessage(rec.JobID, "atomic steer", rec, false)
	}()

	for deadline := time.Now().Add(time.Second); sub.mu.TryLock(); {
		sub.mu.Unlock()
		select {
		case res := <-done:
			t.Fatalf("sendRunningDelegateMessage returned before Steer could append: %+v", res)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("subagent run lock was not held while steering was blocked")
		}
		time.Sleep(10 * time.Millisecond)
	}

	child.mu.Unlock()
	childLocked = false
	res := <-done
	if res.Err != nil {
		t.Fatalf("sendRunningDelegateMessage returned error: %v", res.Err)
	}
	queue := child.SteeringQueueSnapshot()
	if len(queue) != 1 || queue[0].Text != "atomic steer" {
		t.Fatalf("steering queue = %+v, want atomic steer", queue)
	}
}

func TestFindRunningDelegateByTranscriptRefRejectsAmbiguousMatches(t *testing.T) {
	adapter := &cancelAwareDelegateAdapter{name: "openai", started: make(chan struct{})}
	c := llm.NewClient()
	c.Register(adapter)
	sess := newDelegateTestSession(t, c)

	first := sess.createDelegate(context.Background(), delegateArgs{
		Task:       "stay running",
		Background: true,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	select {
	case <-adapter.started:
	case <-time.After(2 * time.Second):
		t.Fatal("delegate child did not start")
	}

	sess.jobManager.mu.Lock()
	sess.jobManager.running["job_duplicate_delegate"] = &runningJob{
		rec: &jobstore.JobRecord{
			JobID:         "job_duplicate_delegate",
			Type:          jobstore.JobDelegate,
			Status:        jobstore.StatusRunning,
			TranscriptRef: first.TranscriptRef,
		},
		durableStarted: true,
	}
	sess.jobManager.mu.Unlock()
	t.Cleanup(func() {
		sess.jobManager.mu.Lock()
		delete(sess.jobManager.running, "job_duplicate_delegate")
		sess.jobManager.mu.Unlock()
	})

	_, err := findRunningDelegateByTranscriptRef(sess.jobManager, first.TranscriptRef)
	if err == nil || !strings.Contains(err.Error(), "active_delegate_ambiguous") {
		t.Fatalf("findRunningDelegateByTranscriptRef error = %v, want active_delegate_ambiguous", err)
	}

	_, _ = sess.jobManager.stop(first.JobID)
	waitForShellDone(t, sess.jobManager, first.JobID)
}

func TestSendDelegateMessageAliasTargetDeliversRuntimeMessage(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess := newDelegateTestSession(t, c)

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  "caller",
		Message: "runtime advisory",
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Target != "caller" ||
		!res.Delivered ||
		res.Action != "sent" ||
		res.MessageType != "runtime" {
		t.Fatalf("result = %+v, want runtime delivered shape", res)
	}
	queue := sess.SteeringQueueSnapshot()
	if len(queue) != 1 || queue[0].Text != "runtime advisory" {
		t.Fatalf("steering queue = %+v, want runtime advisory", queue)
	}
}

func TestJobSendMessageMainAliasFailsTargetNotFound(t *testing.T) {
	sess := newTestSession(t)
	called := false
	sess.cfg.spawn.parentSteer = func(string) { called = true }

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  "main",
		Message: "hello",
	})

	if res.Err == nil || !strings.Contains(res.Err.Error(), "target_not_found") {
		t.Fatalf("error = %v, want target_not_found", res.Err)
	}
	if called {
		t.Fatal("main alias called parentSteer")
	}
	if queue := sess.SteeringQueueSnapshot(); len(queue) != 0 {
		t.Fatalf("steering queue = %+v, want no side effects", queue)
	}
	if jobs := sess.jobManager.list(listFilter{}); len(jobs) != 0 {
		t.Fatalf("jobs = %+v, want no jobs created", jobs)
	}
	sess.jobManager.mu.Lock()
	runningJobs := len(sess.jobManager.running)
	sess.jobManager.mu.Unlock()
	if runningJobs != 0 {
		t.Fatalf("running jobs = %d, want no runs created", runningJobs)
	}
}

func TestJobSendMessageWatchedWithoutWatchContextFails(t *testing.T) {
	sess := newTestSession(t)

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  "watched",
		Message: "hello",
	})

	if res.Err == nil || !strings.Contains(res.Err.Error(), "target_not_found") {
		t.Fatalf("error = %v, want target_not_found", res.Err)
	}
	if queue := sess.SteeringQueueSnapshot(); len(queue) != 0 {
		t.Fatalf("steering queue = %+v, want no side effects", queue)
	}
}

func TestSendDelegateMessageAliasFromSubagentSteersCaller(t *testing.T) {
	parent := newTestSession(t)
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	subCfg := SessionConfig{MaxSubagentDepth: 2}
	subCfg.spawn.depth = 1
	subCfg.spawn.parentSessionID = parent.ID()
	subCfg.spawn.parentSteer = parent.Steer
	child, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), subCfg)
	if err != nil {
		t.Fatalf("NewSession child: %v", err)
	}
	t.Cleanup(func() { child.Close() })

	res := child.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  "caller",
		Message: "child advisory",
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Target != "caller" || !res.Delivered || res.Action != "sent" || res.MessageType != "runtime" {
		t.Fatalf("result = %+v, want runtime alias delivery", res)
	}
	if queue := child.SteeringQueueSnapshot(); len(queue) != 0 {
		t.Fatalf("child steering queue = %+v, want no alias message", queue)
	}
	queue := parent.SteeringQueueSnapshot()
	if len(queue) != 1 || queue[0].Text != "child advisory" {
		t.Fatalf("parent steering queue = %+v, want child advisory", queue)
	}
}

func TestSendDelegateMessageUnsupportedAliasesFromSubagentFailTargetNotFound(t *testing.T) {
	for _, target := range []string{"main", "watched"} {
		t.Run(target, func(t *testing.T) {
			parent := newTestSession(t)
			dir := t.TempDir()
			c := llm.NewClient()
			c.Register(&fakeAdapter{name: "openai"})
			subCfg := SessionConfig{MaxSubagentDepth: 2}
			subCfg.spawn.depth = 1
			subCfg.spawn.parentSessionID = parent.ID()
			subCfg.spawn.parentSteer = parent.Steer
			child, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), subCfg)
			if err != nil {
				t.Fatalf("NewSession child: %v", err)
			}
			t.Cleanup(func() { child.Close() })

			res := child.sendDelegateMessage(context.Background(), sendMessageArgs{
				Target:  target,
				Message: "child advisory",
			})

			if res.Err == nil || !strings.Contains(res.Err.Error(), "target_not_found") {
				t.Fatalf("error = %v, want target_not_found", res.Err)
			}
			if strings.Contains(res.Err.Error(), "not_controllable") {
				t.Fatalf("error = %v, must not report not_controllable", res.Err)
			}
			if queue := parent.SteeringQueueSnapshot(); len(queue) != 0 {
				t.Fatalf("parent steering queue = %+v, want no side effects", queue)
			}
			if queue := child.SteeringQueueSnapshot(); len(queue) != 0 {
				t.Fatalf("child steering queue = %+v, want no side effects", queue)
			}
		})
	}
}

func newDelegateTestSession(t *testing.T, c *llm.Client) *Session {
	t.Helper()
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func newDelegateRestorePreflightSession(t *testing.T, c *llm.Client) *Session {
	t.Helper()
	workDir := t.TempDir()
	stateDir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func seedStoppedDelegateRestoreRecord(t *testing.T, s *Session) *jobstore.JobRecord {
	t.Helper()
	childID, childWorkDir := seedRetainedChildSessionWithWorkingDir(t, s)
	jobID := jobstore.NewJobID()
	now := time.Now().UTC()
	ref := encodeRef("", childID)
	desc := &jobstore.DelegateRestoreDescriptor{
		Version:           1,
		ChildSessionID:    childID,
		TranscriptRef:     ref,
		ParentSessionID:   s.ID(),
		ParentJobID:       jobID,
		OwnerSessionID:    s.ID(),
		VisibleSessionID:  s.ID(),
		Task:              "retained delegate",
		ResolvedProfileID: "openai",
		ResolvedModel:     "gpt-5.2",
		WorkingDir:        childWorkDir,
		LocalEnvPolicy:    "default",
	}
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               now,
		JobID:            jobID,
		Type:             jobstore.JobDelegate,
		Task:             desc.Task,
		OwnerSessionID:   s.ID(),
		VisibleToSession: s.ID(),
		StartedAt:        &now,
		TranscriptRef:    ref,
		DelegateRestore:  desc,
	}); err != nil {
		t.Fatalf("append delegate start: %v", err)
	}
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          now,
		JobID:       jobID,
		Status:      jobstore.StatusStopped,
		Reason:      "runtime_lost",
		EndedAt:     &now,
		TerminalGen: jobstore.NewWatchGeneration(),
	}); err != nil {
		t.Fatalf("append delegate stopped: %v", err)
	}
	return loadShellRecord(t, s.jobManager, jobID)
}

func markStoredDelegateResumable(t *testing.T, s *Session, rec *jobstore.JobRecord) {
	t.Helper()
	resumable := true
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:          jobstore.EventJobSessionAssigned,
		TS:            time.Now().UTC(),
		JobID:         rec.JobID,
		TranscriptRef: rec.TranscriptRef,
		Resumable:     &resumable,
	}); err != nil {
		t.Fatalf("append delegate resumable assignment: %v", err)
	}
}

func setStoredDelegateTerminalStatus(t *testing.T, s *Session, rec *jobstore.JobRecord, status jobstore.Status, reason string) {
	t.Helper()
	events := loadJobStoreEvents(t, s.jobManager)
	for i := range events {
		if events[i].JobID == rec.JobID && events[i].Kind == jobstore.EventJobFinished {
			events[i].Status = status
			events[i].Reason = reason
			rewriteJobStoreEvents(t, s.jobManager, events)
			return
		}
	}
	t.Fatalf("terminal event for delegate job %s not found", rec.JobID)
}

func requireDelegateRestorePreflight(t *testing.T, s *Session, rec *jobstore.JobRecord) *delegateRestorePreflight {
	t.Helper()
	assessment := s.assessDelegateResumability(rec, delegateResumabilityPreflight)
	if !assessment.Resumable || assessment.Preflight == nil {
		t.Fatalf("delegate restore preflight = %+v, want resumable with preflight", assessment)
	}
	return assessment.Preflight
}

func seedRetainedChildSessionWithWorkingDir(t *testing.T, parent *Session) (string, string) {
	t.Helper()
	workDir := t.TempDir()
	cfg := SessionConfig{
		StateDir:         parent.stateDir,
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
	}
	cfg.spawn.depth = parent.depth + 1
	cfg.spawn.parentSessionID = parent.ID()
	child, err := NewSession(parent.client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), cfg)
	if err != nil {
		t.Fatalf("NewSession child: %v", err)
	}
	if child.transcript != nil {
		if err := child.transcript.Close(); err != nil {
			t.Fatalf("close child transcript: %v", err)
		}
		child.transcript = nil
	}
	t.Cleanup(func() { child.Close() })
	return child.ID(), workDir
}

func replaceStoredDelegateRecord(t *testing.T, s *Session, rec *jobstore.JobRecord) {
	t.Helper()
	events := loadJobStoreEvents(t, s.jobManager)
	for i := range events {
		if events[i].JobID == rec.JobID && events[i].Kind == jobstore.EventJobStarted {
			events[i].DelegateRestore = rec.DelegateRestore
			events[i].TranscriptRef = rec.TranscriptRef
			events[i].OwnerSessionID = rec.OwnerSessionID
			events[i].VisibleToSession = rec.VisibleToSession
			break
		}
	}
	rewriteJobStoreEvents(t, s.jobManager, events)
}

func rewriteJobStoreEvents(t *testing.T, jm *jobManager, events []jobstore.Event) {
	t.Helper()
	path := filepath.Join(jm.dir, "jobs.jsonl")
	var lines []string
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal job event: %v", err)
		}
		lines = append(lines, string(data))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("rewrite jobs.jsonl: %v", err)
	}
}

func childSessionMetaPath(s *Session, rec *jobstore.JobRecord) string {
	return filepath.Join(s.stateDir, sessionsSubdir, rec.DelegateRestore.ChildSessionID+".meta.json")
}

func childTranscriptPath(s *Session, rec *jobstore.JobRecord) string {
	return filepath.Join(s.stateDir, sessionsSubdir, rec.DelegateRestore.ChildSessionID+".transcript.jsonl")
}

func removeChildSessionMeta(t *testing.T, s *Session, rec *jobstore.JobRecord) {
	t.Helper()
	if err := os.Remove(childSessionMetaPath(s, rec)); err != nil {
		t.Fatalf("remove child meta: %v", err)
	}
}

func writeChildSessionMeta(t *testing.T, s *Session, rec *jobstore.JobRecord, data []byte) {
	t.Helper()
	if err := os.WriteFile(childSessionMetaPath(s, rec), data, 0o644); err != nil {
		t.Fatalf("write child meta: %v", err)
	}
}

func removeChildTranscript(t *testing.T, s *Session, rec *jobstore.JobRecord) {
	t.Helper()
	if err := os.Remove(childTranscriptPath(s, rec)); err != nil {
		t.Fatalf("remove child transcript: %v", err)
	}
}

func writeChildTranscript(t *testing.T, s *Session, rec *jobstore.JobRecord, data []byte) {
	t.Helper()
	if err := os.WriteFile(childTranscriptPath(s, rec), data, 0o644); err != nil {
		t.Fatalf("write child transcript: %v", err)
	}
}

func appendChildTranscript(t *testing.T, s *Session, rec *jobstore.JobRecord, data string) {
	t.Helper()
	f, err := os.OpenFile(childTranscriptPath(s, rec), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open child transcript: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(data); err != nil {
		t.Fatalf("append child transcript: %v", err)
	}
}

func appendChildTranscriptTurn(t *testing.T, s *Session, rec *jobstore.JobRecord, turn schema.Turn) {
	t.Helper()
	entry := transcript.Entry{
		Kind: "entry",
		Seq:  1,
		Turn: turn,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal child transcript entry: %v", err)
	}
	appendChildTranscript(t, s, rec, string(data)+"\n")
}

func appendSessionJobEvents(t *testing.T, stateDir, sessionID string, events ...jobstore.Event) {
	t.Helper()
	store, err := jobstore.Open(filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl"))
	if err != nil {
		t.Fatalf("open session job store: %v", err)
	}
	defer store.Close()
	for _, event := range events {
		if err := store.Append(event); err != nil {
			t.Fatalf("append session job event %s: %v", event.Kind, err)
		}
	}
}

func appendChildCompletedJobNeedingNotification(t *testing.T, stateDir, childID, jobID string, ts time.Time) {
	t.Helper()
	terminalGen := jobstore.NewTerminalGeneration()
	endedAt := ts.Add(time.Millisecond)
	appendSessionJobEvents(t, stateDir, childID,
		jobstore.Event{
			Kind:             jobstore.EventJobStarted,
			TS:               ts,
			JobID:            jobID,
			Type:             jobstore.JobShell,
			Command:          "true",
			Description:      "completed child job needing notification arm",
			OwnerSessionID:   childID,
			VisibleToSession: childID,
			StartedAt:        &ts,
		},
		jobstore.Event{
			Kind:        jobstore.EventJobFinished,
			TS:          ts.Add(time.Millisecond),
			JobID:       jobID,
			Status:      jobstore.StatusCompleted,
			Reason:      "exit_zero",
			EndedAt:     &endedAt,
			TerminalGen: terminalGen,
		},
	)
}

func loadSessionJobStoreEvents(t *testing.T, stateDir, sessionID string) []jobstore.Event {
	t.Helper()
	path := filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session jobs.jsonl: %v", err)
	}
	var events []jobstore.Event
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event jobstore.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("parse session job event %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func loadSessionWatchSendRecord(t *testing.T, stateDir, sessionID string) jobstore.WatchSendRecord {
	t.Helper()
	return jobstore.FoldWatchSends(loadSessionJobStoreEvents(t, stateDir, sessionID))
}

func hasWatchSendEvent(events []jobstore.Event, kind jobstore.EventKind, deliveryID string) bool {
	for _, event := range events {
		if event.Kind == kind && event.WatchSend != nil && event.WatchSend.DeliveryID == deliveryID {
			return true
		}
	}
	return false
}

func hasJobEvent(events []jobstore.Event, kind jobstore.EventKind, jobID string) bool {
	for _, event := range events {
		if event.Kind == kind && event.JobID == jobID {
			return true
		}
	}
	return false
}

func sessionStartHookPlugin(t *testing.T, command string) string {
	t.Helper()
	dir := makePluginDir(t, "delegate-restore-hook")
	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	raw := map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{
					"matcher": "resume",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": command,
						},
					},
				},
			},
		},
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal hooks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), data, 0o644); err != nil {
		t.Fatalf("write hooks: %v", err)
	}
	return dir
}

func countSteeringEntriesContaining(s *Session, text string) int {
	return len(steeringEntriesContaining(s, text))
}

func newPersistentTestSession(t *testing.T) *Session {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		StateDir:         dir,
	})
	if err != nil {
		t.Fatalf("new persistent test session: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func waitForSteeringEntryContaining(t *testing.T, s *Session, text string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if entries := steeringEntriesContaining(s, text); len(entries) > 0 {
			return entries[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no steering entry containing %q; queue = %+v", text, s.SteeringQueueSnapshot())
	return ""
}

// waitForJobNotification blocks until at least one job notification (e.g. a
// caller watch-send wake token enqueued by asynchronous observation) is pending,
// so a test can then accept it the way the live loop would on wake.
func waitForJobNotification(t *testing.T, s *Session) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s.peekNotifications() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no job notification became pending within deadline")
}

func steeringEntriesContaining(s *Session, text string) []string {
	var entries []string
	for _, entry := range s.SteeringQueueSnapshot() {
		if strings.Contains(entry.Text, text) {
			entries = append(entries, entry.Text)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, turn := range s.history {
		if turn.Kind == schema.TurnSteering && strings.Contains(turn.Message.Text(), text) {
			entries = append(entries, turn.Message.Text())
		}
	}
	return entries
}

func countFileLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func completedDelegateSubagent(child *Session, result string) *subagent {
	return &subagent{
		id:      child.ID(),
		sess:    child,
		running: false,
		status:  SubagentCompleted,
		result:  result,
		done:    make(chan struct{}),
	}
}

func runningDelegateJob(t *testing.T, jm *jobManager, jobID string) *runningJob {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	run := jm.running[jobID]
	if run == nil {
		t.Fatalf("delegate job %s is not running", jobID)
	}
	return run
}

func waitForRunningDelegateJob(t *testing.T, jm *jobManager) *runningJob {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		jm.mu.Lock()
		for _, run := range jm.running {
			if run.rec.Type == jobstore.JobDelegate {
				jm.mu.Unlock()
				return run
			}
		}
		jm.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for running delegate job")
	return nil
}

type cancelAwareDelegateAdapter struct {
	name    string
	started chan struct{}
	once    sync.Once
}

func (a *cancelAwareDelegateAdapter) Name() string { return a.name }

func (a *cancelAwareDelegateAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.once.Do(func() { close(a.started) })
	<-ctx.Done()
	return llm.Response{Provider: a.name, Model: req.Model}, ctx.Err()
}

func (a *cancelAwareDelegateAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

type resumeBlockingDelegateAdapter struct {
	name          string
	secondStarted chan struct{}
	once          sync.Once
	mu            sync.Mutex
	calls         int
}

func (a *resumeBlockingDelegateAdapter) Name() string { return a.name }

func (a *resumeBlockingDelegateAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	a.calls++
	call := a.calls
	a.mu.Unlock()

	if call == 1 {
		resp := communicateWithDefaultOutput("first complete")
		resp.Provider = a.name
		if resp.Model == "" {
			resp.Model = req.Model
		}
		return resp, nil
	}

	a.once.Do(func() { close(a.secondStarted) })
	<-ctx.Done()
	return llm.Response{Provider: a.name, Model: req.Model}, ctx.Err()
}

func (a *resumeBlockingDelegateAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

func communicateWithStructured(message string, output map[string]any) llm.Response {
	args, _ := json.Marshal(map[string]any{
		"message":     message,
		"await_reply": false,
		"output":      output,
	})
	return toolCallResponse(llm.ToolCallData{
		ID:        "delegate_communicate",
		Name:      "communicate",
		Arguments: args,
		Type:      "function",
	})
}

func communicateWithoutStructured(message string) llm.Response {
	args, _ := json.Marshal(map[string]any{
		"message":     message,
		"await_reply": false,
	})
	return toolCallResponse(llm.ToolCallData{
		ID:        "delegate_communicate",
		Name:      "communicate",
		Arguments: args,
		Type:      "function",
	})
}

func communicateWithDefaultOutput(message string) llm.Response {
	return communicateWithStructured(message, map[string]any{
		"message":   message,
		"data":      map[string]any{},
		"artifacts": []string{},
	})
}

func communicateOutputSchemaHasProperty(req llm.Request, property string) bool {
	for _, td := range req.Tools {
		if td.Name != "communicate" {
			continue
		}
		params, ok := td.Parameters["properties"].(map[string]any)
		if !ok {
			return false
		}
		output, ok := params["output"].(map[string]any)
		if !ok {
			return false
		}
		props, ok := output["properties"].(map[string]any)
		if !ok {
			return false
		}
		_, ok = props[property]
		return ok
	}
	return false
}

func requestMessagesContain(req llm.Request, text string) bool {
	for _, msg := range req.Messages {
		if strings.Contains(msg.Text(), text) {
			return true
		}
	}
	return false
}

func countRequestMessagesContaining(req llm.Request, text string) int {
	var count int
	for _, msg := range req.Messages {
		if strings.Contains(msg.Text(), text) {
			count++
		}
	}
	return count
}

func lastUserMessageText(req llm.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == llm.RoleUser {
			return strings.TrimSpace(req.Messages[i].Text())
		}
	}
	return ""
}

func findJobByDescription(t *testing.T, jm *jobManager, description string) *jobstore.JobRecord {
	t.Helper()
	jobs := jm.list(listFilter{IncludeNested: true})
	for _, rec := range jobs {
		if rec.Description == description {
			return rec
		}
	}
	t.Fatalf("jobs = %+v, want job with description %q", jobs, description)
	return nil
}

// TestCoordinatorTypeDelegateResumes verifies seam 5 (spec §1): when a restored
// delegate descriptor carries DelegationAllowance > 0 and its FrozenToolNames
// include "delegate", validateRestoredDelegateRequiredTools must not strip
// "delegate" from the validation set (today's bug: it always strips root-only
// tools regardless of allowance, so the frozen requirement fails and the delegate
// cannot resume).
func TestCoordinatorTypeDelegateResumes(t *testing.T) {
	// Part A-positive: allowance > 0, FrozenToolNames includes "delegate" → must pass.
	t.Run("allowance>0 coordinator resumes with delegate in frozen tools", func(t *testing.T) {
		c := llm.NewClient()
		c.Register(&fakeAdapter{name: "openai"})
		s := newDelegateRestorePreflightSession(t, c)

		// The parent session (s) is a root session — its registry has "delegate".
		// Confirm the registry has it (seam 3 Task 6 confirms this for root sessions).
		if s.reg.Get("delegate") == nil {
			t.Fatal("precondition: parent session registry must have 'delegate' registered")
		}

		desc := &jobstore.DelegateRestoreDescriptor{
			Version:             1,
			DelegationAllowance: 1, // coordinator — can delegate further
			FrozenToolNames:     []string{"delegate", "task_list"},
		}

		err := s.validateRestoredDelegateRequiredTools(desc)
		if err != nil {
			t.Fatalf("validateRestoredDelegateRequiredTools allowance>0: got error %v, want nil", err)
		}
	})

	// Part A-negative: allowance == 0, FrozenToolNames includes "delegate" → must
	// fail (preserves today's leaf semantics: a leaf cannot have delegate in its
	// frozen tool requirements, because the parent's validation set has it stripped).
	t.Run("allowance==0 leaf with delegate in frozen tools fails validation", func(t *testing.T) {
		c := llm.NewClient()
		c.Register(&fakeAdapter{name: "openai"})
		s := newDelegateRestorePreflightSession(t, c)

		desc := &jobstore.DelegateRestoreDescriptor{
			Version:             1,
			DelegationAllowance: 0, // leaf — no delegation
			FrozenToolNames:     []string{"delegate", "task_list"},
		}

		err := s.validateRestoredDelegateRequiredTools(desc)
		if err == nil {
			t.Fatal("validateRestoredDelegateRequiredTools allowance==0 with delegate in frozen tools: want error, got nil")
		}
		if !strings.Contains(err.Error(), "delegate") {
			t.Fatalf("error = %q, want message naming 'delegate'", err.Error())
		}
	})

	// Part A-zero-frozen: allowance > 0 but no frozen tool requirements → must pass.
	t.Run("allowance>0 no frozen tool requirements passes", func(t *testing.T) {
		c := llm.NewClient()
		c.Register(&fakeAdapter{name: "openai"})
		s := newDelegateRestorePreflightSession(t, c)

		desc := &jobstore.DelegateRestoreDescriptor{
			Version:             1,
			DelegationAllowance: 1,
			FrozenToolNames:     nil, // wildcard or empty — no required-tools check
		}

		err := s.validateRestoredDelegateRequiredTools(desc)
		if err != nil {
			t.Fatalf("validateRestoredDelegateRequiredTools no frozen tools: got error %v, want nil", err)
		}
	})
}

func TestDelegateGrantErrorEnumeratesValidRange(t *testing.T) {
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

