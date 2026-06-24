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
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/skill"
	taskpkg "primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

func TestSendDelegateMessageTerminalDelegateResumeCreatesNewJob(t *testing.T) {
	t.Parallel()
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
		Target:  first.DelegateID,
		Message: "run again",
		OnIdle:  "start",
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Action != "started" ||
		res.JobID == "" ||
		res.JobID == first.JobID ||
		res.ResumedFromJobID != first.JobID ||
		res.TranscriptRef != first.TranscriptRef ||
		res.Status != jobstore.StatusRunning ||
		!res.RunningInBackground {
		t.Fatalf("result = %+v, want started new running delegate job from %s", res, first.JobID)
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

func TestDelegateResumeKeepsDelegateIDAndUpdatesLatestJob(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("first") },
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("second") },
	}})
	s := newDelegateTestSession(t, c)
	first := s.createDelegate(context.Background(), delegateArgs{Task: "first", Background: false, BlockTimeoutMS: 5000})
	if first.Err != nil {
		t.Fatalf("first delegate: %v", first.Err)
	}
	res := s.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         first.DelegateID,
		Message:        "second",
		OnIdle:         "start",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage: %v", res.Err)
	}
	if res.DelegateID != first.DelegateID || res.StartedJobID == first.JobID || res.LatestJobID != res.StartedJobID {
		t.Fatalf("resume result = %+v, want same delegate and new latest job", res)
	}
	rec := loadShellRecord(t, s.jobManager, res.StartedJobID)
	if rec.DelegateID != first.DelegateID {
		t.Fatalf("resumed job DelegateID = %q, want %q", rec.DelegateID, first.DelegateID)
	}
}

func TestDelegateIDResumeFinalizesObservedTerminalRunningJob(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("resumed") },
	}})
	parent := newDelegateTestSession(t, c)
	child := newDelegateTestSession(t, c)
	sub := completedDelegateSubagent(child, "already done")
	parent.subagents.track(sub)

	delegateID := jobstore.NewDelegateID()
	jobID := jobstore.NewJobID()
	run, err := parent.attachDelegateJobWithRestoreAndDelegate(parent.jobManager, child.ID(), "already terminal", sub, jobID, nil, false, nil, nil, delegateJobLink{
		delegateID: delegateID,
		generation: jobstore.NewDelegateGeneration(),
		create:     true,
	}, nil)
	if err != nil {
		t.Fatalf("attachDelegateJobWithRestoreAndDelegate: %v", err)
	}
	if run.rec.Status != jobstore.StatusRunning {
		t.Fatalf("seed job status = %s, want running", run.rec.Status)
	}

	res := parent.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         delegateID,
		Message:        "resume after observed terminal",
		OnIdle:         "start",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage: %v", res.Err)
	}
	if res.DelegateID != delegateID || res.StartedJobID == "" || res.StartedJobID == jobID {
		t.Fatalf("resume result = %+v, want same delegate and new concrete job", res)
	}
	rec := loadShellRecord(t, parent.jobManager, res.StartedJobID)
	if rec.DelegateID != delegateID {
		t.Fatalf("resumed job DelegateID = %q, want %q", rec.DelegateID, delegateID)
	}
}

func TestDelegateSendJobIDDoesNotRevealDescendantDelegateID(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	started := time.Unix(400, 0).UTC()
	if err := parent.jobManager.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               started,
		JobID:            "job_child_delegate",
		Type:             jobstore.JobDelegate,
		DelegateID:       "dlg_child_hidden",
		OwnerSessionID:   "CHILD",
		VisibleToSession: parent.id,
		TranscriptRef:    encodeRef("", "CHILD"),
		StartedAt:        &started,
	}); err != nil {
		t.Fatalf("append child-owned delegate job: %v", err)
	}

	listOut, err := jobListTool(parent, decodeJobListArgs(t, `{"include_nested":true}`), 1<<20)
	if err != nil {
		t.Fatalf("jobListTool(include_nested): %v", err)
	}
	var listed jobListResult
	if err := json.Unmarshal(handlerJSON(t, listOut), &listed); err != nil {
		t.Fatalf("unmarshal job_list: %v", err)
	}
	var row *jobListEntry
	for i := range listed.Jobs {
		if listed.Jobs[i].JobID == "job_child_delegate" {
			row = &listed.Jobs[i]
			break
		}
	}
	if row == nil {
		t.Fatalf("job_list rows = %+v, want child delegate job", listed.Jobs)
	}
	if row.DelegateID != "" || strings.Contains(listOut.(tool.StateResult).Output, "dlg_child_hidden") {
		t.Fatalf("job_list exposed descendant delegate handle: row=%+v output=%s", row, listOut.(tool.StateResult).Output)
	}

	res := parent.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  "job_child_delegate",
		Message: "hello",
	})
	if res.Err == nil {
		t.Fatal("sendDelegateMessage succeeded, want rejection")
	}
	if strings.Contains(res.Err.Error(), "dlg_child_hidden") {
		t.Fatalf("delegate_send error exposed descendant delegate_id: %v", res.Err)
	}
	if !strings.Contains(res.Err.Error(), "not_controllable") {
		t.Fatalf("delegate_send error = %v, want not_controllable without delegate_id", res.Err)
	}
}

// TestSendDelegateMessageOwnDirectDelegatesAtDepth: a depth-1 coordinator may
// message its OWN direct worker delegate by job_id (spec §3:
// "own direct delegates at every level"). Today the depth>0 guard rejects every
// concrete delegate target as "root-only"; the coordinator's own worker delegate
// must instead resume.
func TestSendDelegateMessageOwnDirectDelegatesAtDepth(t *testing.T) {
	t.Parallel()
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
		Target:  worker.DelegateID,
		Message: "worker, run again",
		OnIdle:  "start",
	})
	if res.Err != nil {
		t.Fatalf("depth-1 coordinator messaging its own direct worker delegate: %v", res.Err)
	}
	if res.Action != "started" || res.ResumedFromJobID != worker.JobID || res.JobID == "" || res.JobID == worker.JobID {
		t.Fatalf("result = %+v, want started new running delegate job from worker %s", res, worker.JobID)
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
	t.Parallel()
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
	finalizeErr, _ := sess.bridgeDelegateFinalization(run.rec.JobID, childID, prepared.sub, true)
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
		Target:  first.DelegateID,
		Message: "run again",
		OnIdle:  "start",
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Action != "started" || res.JobID == "" || res.JobID == first.JobID {
		t.Fatalf("resume result = %+v, want new started job", res)
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
	t.Parallel()
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
		Target:         first.DelegateID,
		Message:        "run again",
		OnIdle:         "start",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 1000,
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Action != "started" ||
		res.JobID == "" ||
		res.JobID == first.JobID ||
		res.ResumedFromJobID != first.JobID ||
		res.Status != jobstore.StatusRunning ||
		res.Reason != "foreground_timeout" ||
		!res.RunningInBackground ||
		!res.TimedOut ||
		res.TranscriptRef != first.TranscriptRef {
		t.Fatalf("result = %+v, want started foreground timeout running in background", res)
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

func TestSendDelegateMessageTerminalDelegateDefaultIdleFails(t *testing.T) {
	t.Parallel()
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
		Target:  first.DelegateID,
		Message: "must be live",
	})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "target_idle") {
		t.Fatalf("error = %v, want target_idle", res.Err)
	}
	if jobs := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate}); len(jobs) != 1 {
		t.Fatalf("delegate jobs = %+v, want no new job", jobs)
	}
}

func TestSendDelegateMessageObservedTerminalRunningRecordDefaultIdleFails(t *testing.T) {
	t.Parallel()
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
		Target:  run.rec.DelegateID,
		Message: "must still be live",
	})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "target_idle") {
		t.Fatalf("error = %v, want target_idle", res.Err)
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
	t.Parallel()
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

	res := parent.sendRunningDelegateMessage(run.rec.JobID, "watch-originated steer", run.rec, true, nil)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "not_controllable") {
		t.Fatalf("error = %v, want not_controllable", res.Err)
	}
	if run.fromWatch.Load() {
		t.Fatalf("non-live delegate was marked watch-originated after undelivered send")
	}
}

func TestWatchOriginatedRunningSendRejectedByClosingChildStaysBusy(t *testing.T) {
	t.Parallel()
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

	res := parent.sendRunningDelegateMessage(run.rec.JobID, "watch-originated steer", run.rec, true, nil)
	if res.Err != nil {
		t.Fatalf("sendRunningDelegateMessage returned error: %v", res.Err)
	}
	if res.Action != "steered" || !res.WatchSendDeliveryClassSet || res.WatchSendDeliveryClass != watchSendBusy {
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
	t.Parallel()
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
		Target:    first.DelegateID,
		Message:   "watch-originated resume",
		OnIdle:    "start",
		FromWatch: true,
	})
	if second.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", second.Err)
	}
	if second.Action != "started" || second.JobID == "" || second.JobID == first.JobID {
		t.Fatalf("second result = %+v, want started new running delegate job", second)
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
	t.Parallel()
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
		Target:  first.DelegateID,
		Message: "run again",
		OnIdle:  "start",
	})
	if second.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", second.Err)
	}
	if second.Action != "started" || second.JobID == "" || second.JobID == first.JobID {
		t.Fatalf("second result = %+v, want started new running delegate job", second)
	}
	select {
	case <-adapter.secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("resumed delegate did not start")
	}

	before := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  first.DelegateID,
		Message: "steer current run",
	})
	if res.Err != nil {
		if strings.Contains(res.Err.Error(), "delegate_session_busy") {
			t.Fatalf("sendDelegateMessage returned delegate_session_busy: %v", res.Err)
		}
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Action != "steered" ||
		res.JobID != second.JobID ||
		res.JobID == first.JobID ||
		res.Status != jobstore.StatusRunning ||
		!res.RunningInBackground ||
		res.TranscriptRef != first.TranscriptRef {
		t.Fatalf("result = %+v, want steered to active started delegate job %s", res, second.JobID)
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
	t.Parallel()
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
	origAppendEvents := sess.jobManager.appendEvents
	defer func() { sess.jobManager.appendEvents = origAppendEvents }()
	sess.jobManager.appendEvents = func(events []jobstore.Event) error {
		for _, event := range events {
			if event.Kind == jobstore.EventJobStarted && event.Type == jobstore.JobDelegate && event.Task == "run again" {
				attachOnce.Do(func() { close(attachStarted) })
				<-releaseAttach
				break
			}
		}
		return origAppendEvents(events)
	}

	firstResumeDone := make(chan sendMessageResult, 1)
	go func() {
		firstResumeDone <- sess.sendDelegateMessage(context.Background(), sendMessageArgs{
			Target:  first.DelegateID,
			Message: "run again",
			OnIdle:  "start",
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
			Target:  first.DelegateID,
			Message: "steer while attaching",
			OnIdle:  "start",
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
	if firstResume.Action != "started" || firstResume.JobID == "" || firstResume.JobID == first.JobID {
		t.Fatalf("first terminal resume = %+v, want started new delegate job", firstResume)
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
	if secondResume.Action != "steered" ||
		secondResume.JobID != firstResume.JobID ||
		secondResume.TranscriptRef != first.TranscriptRef {
		t.Fatalf("second terminal resume = %+v, want steered to active started job %s", secondResume, firstResume.JobID)
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
	if len(queue) > 0 && queue[0].Text != "steer while attaching" {
		t.Fatalf("steering queue = %+v, want only concurrent terminal resume steer if still queued", queue)
	}
	jobs := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(jobs) != 2 {
		t.Fatalf("delegate jobs = %+v, want original plus one resumed job", jobs)
	}

	_, _ = sess.jobManager.stop(firstResume.JobID)
	waitForShellDone(t, sess.jobManager, firstResume.JobID)
}

func TestSendDelegateMessageTerminalTargetFailDoesNotSteerLaterRun(t *testing.T) {
	t.Parallel()
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
		Target:  first.DelegateID,
		Message: "run again",
		OnIdle:  "start",
	})
	if second.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", second.Err)
	}
	if second.Action != "started" || second.JobID == "" || second.JobID == first.JobID {
		t.Fatalf("second result = %+v, want started new running delegate job", second)
	}
	select {
	case <-adapter.secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("resumed delegate did not start")
	}

	before := sess.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  first.JobID,
		Message: "must not steer running job",
	})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "job_id is a job/turn handle") {
		t.Fatalf("error = %v, want job_id handle rejection", res.Err)
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
	t.Parallel()
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
			t.Parallel()
			c := llm.NewClient()
			adapter := &fakeAdapter{name: "openai"}
			c.Register(adapter)
			s := newDelegateRestorePreflightSession(t, c)
			rec := seedStoppedDelegateRestoreRecord(t, s)
			tc.breakState(t, s, rec)
			beforeEvents := len(loadJobStoreEvents(t, s.jobManager))
			beforeJobs := len(s.jobManager.list(listFilter{Type: jobstore.JobDelegate}))

			res := s.sendDelegateMessage(context.Background(), sendMessageArgs{
				Target:  rec.DelegateID,
				Message: "resume",
				OnIdle:  "start",
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
	t.Parallel()
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
	t.Parallel()
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
		Target:         rec.DelegateID,
		Message:        "resume using descriptor",
		OnIdle:         "start",
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
	t.Parallel()
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
		t.Fatalf("restored child runtime = %+v, want none before delegate_send", sub)
	}
}

func TestJobSendMessageReconstructsRestoredDelegateRuntimeFromDescriptor(t *testing.T) {
	t.Parallel()
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
		Target:         rec.DelegateID,
		Message:        "new input after restore",
		OnIdle:         "start",
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

func TestJobSendMessageRestoresWatchParentLeafObserverJobWatch(t *testing.T) {
	t.Parallel()
	var request llm.Request
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				request = req
				return communicateWithDefaultOutput("restored observer complete")
			},
		},
	})
	s := newDelegateRestorePreflightSession(t, c)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	childID := rec.DelegateRestore.ChildSessionID
	rec.DelegateRestore.DelegationAllowance = 0
	rec.DelegateRestore.ParentWatchGranted = true
	rec.DelegateRestore.FrozenToolNames = []string{"job_watch"}
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
		Target:         rec.DelegateID,
		Message:        "resume observer after restart",
		OnIdle:         "start",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}

	sub := restored.subagents.get(childID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("reconstructed child runtime = %+v, want retained observer child", sub)
	}
	child := sub.sess
	if !child.cfg.spawn.parentWatchGranted {
		t.Fatal("restored observer lost parentWatchGranted")
	}
	if child.cfg.spawn.parentInstallWatch == nil {
		t.Fatal("restored observer lost parentInstallWatch")
	}
	if child.reg.Get("job_watch") == nil {
		t.Fatal("restored observer missing job_watch")
	}
	if !hasCachedCallableToolDefinition(child, "job_watch") {
		t.Fatal("restored observer must advertise job_watch")
	}
	if child.reg.Get("delegate") != nil {
		t.Fatal("restored leaf observer must not get delegate")
	}
	if hasCachedCallableToolDefinition(child, "delegate") {
		t.Fatal("restored leaf observer must not advertise delegate")
	}
	if !requestHasTool(request, "job_watch") {
		t.Fatalf("restored observer request missing job_watch tool: %+v", request.Tools)
	}
	if requestHasTool(request, "delegate") {
		t.Fatalf("restored leaf observer request advertised delegate: %+v", request.Tools)
	}

	out, err := jobWatchTool(child, map[string]any{
		"operation": "create",
		"source":    "parent",
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("restored observer jobWatchTool(source parent): %v", err)
	}
	state := out.(tool.StateResult).State.(jobWatchToolResult)
	if state.Source != "parent" || !state.Watching {
		t.Fatalf("restored observer watch state = %+v, want source parent watching", state)
	}
	cfg := onlyWatchConfigForTest(t, restored.jobManager)
	if cfg.receiverSessionID != child.ID() {
		t.Fatalf("restored receiverSessionID = %q, want child %q", cfg.receiverSessionID, child.ID())
	}
	if cfg.receiverDelegateID != rec.DelegateID {
		t.Fatalf("restored receiverDelegateID = %q, want delegate %q", cfg.receiverDelegateID, rec.DelegateID)
	}

	clearOut, err := jobWatchTool(child, map[string]any{
		"operation": "clear",
		"watch_id":  state.WatchID,
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("restored observer public clear: %v", err)
	}
	clearState := clearOut.(tool.StateResult).State.(jobWatchToolResult)
	if clearState.WatchID != state.WatchID || clearState.Watching {
		t.Fatalf("restored observer clear state = %+v, want watch %q cleared", clearState, state.WatchID)
	}
	restored.jobManager.mu.Lock()
	remaining := len(restored.jobManager.watches)
	restored.jobManager.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("restored parent watch count after clear = %d, want 0", remaining)
	}
}

func TestRuntimeLostDelegateResumeAfterRestoreCreatesNewJobFromRetainedState(t *testing.T) {
	t.Parallel()
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
		t.Fatalf("restored child runtime = %+v, want none before delegate_send", sub)
	}
	if requests := adapter.Requests(); len(requests) != 1 {
		t.Fatalf("adapter requests before send = %+v, want only initial delegate request", requests)
	}

	res := restored.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         first.DelegateID,
		Message:        resumedInput,
		OnIdle:         "start",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.JobID == "" || res.JobID == first.JobID ||
		res.Action != "started" ||
		res.ResumedFromJobID != first.JobID ||
		res.Status != jobstore.StatusCompleted ||
		res.TranscriptRef != first.TranscriptRef ||
		!strings.Contains(res.Output, resumedOutput) ||
		!res.StructuredResultValidSet ||
		!res.StructuredResultValid {
		t.Fatalf("resume result = %+v, want new completed started job", res)
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
	t.Parallel()
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
						`{"command":"printf 'runtime-lost-nested-ready\n'; sleep 30","description":%q,"background":true}`,
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
		t.Fatalf("restored child runtime = %+v, want none before delegate_send", sub)
	}

	res := restored.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         first.DelegateID,
		Message:        "resume and start a nested shell",
		OnIdle:         "start",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.JobID == "" || res.JobID == first.JobID || res.Action != "started" || res.ResumedFromJobID != first.JobID {
		t.Fatalf("resume result = %+v, want new started job from old runtime-lost job", res)
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
	if err := json.Unmarshal(handlerJSON(t, stopOut), &stop); err != nil {
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
	t.Parallel()
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
	t.Parallel()
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
		Target:         rec.DelegateID,
		Message:        "continue with the checklist",
		OnIdle:         "start",
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
	t.Parallel()
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
		Target:         rec.DelegateID,
		Message:        "continue with the descriptor checklist",
		OnIdle:         "start",
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
	t.Parallel()
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
		Target:  rec.DelegateID,
		Message: "resume",
		OnIdle:  "start",
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
	t.Parallel()
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
				Target:  rec.DelegateID,
				Message: "resume",
				OnIdle:  "start",
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
	t.Parallel()
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
		Target:  rec.DelegateID,
		Message: "resume",
		OnIdle:  "start",
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
	t.Parallel()
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
		Target:  rec.DelegateID,
		Message: "resume",
		OnIdle:  "start",
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
	t.Parallel()
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
		Target:  rec.DelegateID,
		Message: "resume",
		OnIdle:  "start",
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
	t.Parallel()
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
	t.Parallel()
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
			Target:  rec.DelegateID,
			Message: "resume while parent closes",
			OnIdle:  "start",
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
	t.Parallel()
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
			Target:  rec.DelegateID,
			Message: "resume while close waits for reconstruction claim",
			OnIdle:  "start",
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
	t.Parallel()
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
		Target:  rec.DelegateID,
		Message: "resume while parent closes before side effects",
		OnIdle:  "start",
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
				Target:  rec.DelegateID,
				Message: "resume",
				OnIdle:  "start",
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
	t.Parallel()
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
	s.delegateRestoreResumeHistory = func(entries []transcript.Entry) []schema.Turn {
		if len(entries) != 1 {
			t.Errorf("strict preflight entries = %d, want 1", len(entries))
		}
		return []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("strict preflight history marker"))}
	}

	res := s.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         rec.DelegateID,
		Message:        "resume valid terminal delegate",
		OnIdle:         "start",
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
	t.Parallel()
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
		Target:  first.DelegateID,
		Message: "please adjust course",
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Action != "steered" ||
		res.JobID != first.JobID ||
		res.Status != jobstore.StatusRunning ||
		!res.RunningInBackground ||
		res.TranscriptRef != first.TranscriptRef {
		t.Fatalf("result = %+v, want steered to running delegate job", res)
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
	t.Parallel()
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
		Send:   &watchSendArgs{To: "dlg_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure watch: %v", err)
	}

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:    first.DelegateID,
		Message:   "watch-originated steer",
		FromWatch: true,
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Action != "steered" || res.JobID != first.JobID || res.Status != jobstore.StatusRunning || !res.RunningInBackground {
		t.Fatalf("result = %+v, want steered to running delegate job %s", res, first.JobID)
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
	// FromWatch on the payload no longer suppresses watch fires; suppression is now
	// per-watch causal provenance. This JobFinished does not carry THIS watch's
	// (watch_id, generation) key — the completion was caused by stop(), not by a
	// delivery of this watch — so the unrelated caller watch correctly fires once.
	if pending := loadWatchSendRecord(t, sess.jobManager).Pending; len(pending) != 1 {
		t.Fatalf("unrelated caller watch should fire on watch-originated completion; pending = %d: %+v", len(pending), pending)
	}
}

func TestSendDelegateMessageRunningTargetHoldsRunLockThroughSteer(t *testing.T) {
	t.Parallel()
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
		done <- parent.sendRunningDelegateMessage(rec.JobID, "atomic steer", rec, false, nil)
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
	t.Parallel()
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

func TestSendDelegateMessageRootCallerTargetFails(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess := newDelegateTestSession(t, c)

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  "caller",
		Message: "runtime advisory",
	})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "caller is only available") {
		t.Fatalf("error = %v, want caller route rejection", res.Err)
	}
	queue := sess.SteeringQueueSnapshot()
	if len(queue) != 0 {
		t.Fatalf("steering queue = %+v, want no runtime advisory", queue)
	}
}

func TestDelegateSendToolRejectsCallerAliasPublicly(t *testing.T) {
	t.Parallel()
	sess := newTestSession(t)

	_, err := delegateSendTool(context.Background(), sess, map[string]any{
		"to":      "caller",
		"message": "old observer callback",
	}, jobToolResultDefaultMaxChar)
	if err == nil {
		t.Fatal("delegate_send(to=caller) succeeded, want invalid_request")
	}
	if !strings.Contains(err.Error(), "delegate_id") || !strings.Contains(err.Error(), "communicate(end_turn=true)") {
		t.Fatalf("error = %v, want child delegate_id and communicate guidance", err)
	}
}

func TestDelegateSendMainAliasFailsInvalidRequest(t *testing.T) {
	t.Parallel()
	sess := newTestSession(t)
	called := false
	sess.cfg.spawn.parentSteer = func(string, *provenance.Causal) { called = true }

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  "main",
		Message: "hello",
	})

	if res.Err == nil || !strings.Contains(res.Err.Error(), "invalid_request") {
		t.Fatalf("error = %v, want invalid_request", res.Err)
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

func TestDelegateSendWatchedWithoutWatchContextFails(t *testing.T) {
	t.Parallel()
	sess := newTestSession(t)

	res := sess.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  "watched",
		Message: "hello",
	})

	if res.Err == nil || !strings.Contains(res.Err.Error(), "invalid_request") {
		t.Fatalf("error = %v, want invalid_request", res.Err)
	}
	if queue := sess.SteeringQueueSnapshot(); len(queue) != 0 {
		t.Fatalf("steering queue = %+v, want no side effects", queue)
	}
}

func TestSendDelegateMessageAliasFromSubagentSteersCaller(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	subCfg := SessionConfig{MaxSubagentDepth: 2}
	subCfg.spawn.depth = 1
	subCfg.spawn.parentSessionID = parent.ID()
	subCfg.spawn.parentSteer = parent.SteerWithProvenance
	child := newSession(t, withClient(c), withDir(dir), withConfig(subCfg))

	res := child.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  "caller",
		Message: "child advisory",
	})
	if res.Err != nil {
		t.Fatalf("sendDelegateMessage returned error: %v", res.Err)
	}
	if res.Target != "caller" || !res.Delivered || res.Action != "delivered" || res.MessageType != "runtime" {
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
	t.Parallel()
	for _, target := range []string{"main", "watched"} {
		t.Run(target, func(t *testing.T) {
			parent := newTestSession(t)
			dir := t.TempDir()
			c := llm.NewClient()
			c.Register(&fakeAdapter{name: "openai"})
			subCfg := SessionConfig{MaxSubagentDepth: 2}
			subCfg.spawn.depth = 1
			subCfg.spawn.parentSessionID = parent.ID()
			subCfg.spawn.parentSteer = parent.SteerWithProvenance
			child := newSession(t, withClient(c), withDir(dir), withConfig(subCfg))

			res := child.sendDelegateMessage(context.Background(), sendMessageArgs{
				Target:  target,
				Message: "child advisory",
			})

			if res.Err == nil || !strings.Contains(res.Err.Error(), "invalid_request") {
				t.Fatalf("error = %v, want invalid_request", res.Err)
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

// TestCoordinatorTypeDelegateResumes verifies seam 5 (spec §1): when a restored
// delegate descriptor carries DelegationAllowance > 0 and its FrozenToolNames
// include "delegate", validateRestoredDelegateRequiredTools must not strip
// "delegate" from the validation set (today's bug: it always strips root-only
// tools regardless of allowance, so the frozen requirement fails and the delegate
// cannot resume).
func TestCoordinatorTypeDelegateResumes(t *testing.T) {
	t.Parallel()
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

func TestDelegateSendCallerCarriesActiveProvenanceToParentSteering(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	p := provenance.WithWatch(nil, "watch_A", "wg_1", "wd_1", parent.ID(), "caller")
	child.replaceActiveProvenance(p)
	child.cfg.spawn.parentSteerDelivered = parent.trySteerWithProvenance

	res := child.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:  runtimeMessageAliasCaller,
		Message: "PYTHON_QUOTE delivery=wd_1 quote=Ni!",
	})
	if res.Err != nil || !res.Delivered {
		t.Fatalf("sendDelegateMessage = %+v, want delivered", res)
	}

	parent.mu.Lock()
	defer parent.mu.Unlock()
	if len(parent.steeringQueue) != 1 {
		t.Fatalf("parent steering queue = %d, want 1", len(parent.steeringQueue))
	}
	if !provenance.ContainsWatch(parent.steeringQueue[0].Provenance, "watch_A", "wg_1") {
		t.Fatalf("steering provenance = %+v, want watch_A/wg_1", parent.steeringQueue[0].Provenance)
	}
}

func TestRunningDelegateWatchSendCarriesProvenanceToObserverSteering(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	childID := child.ID()
	sub := &subagent{id: childID, sess: child, running: true}
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, childID, "observer", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}
	p := provenance.WithWatch(nil, "watch_A", "wg_1", "wd_1", parent.ID(), "caller")

	res := parent.sendRunningDelegateMessage(run.rec.DelegateID, "Watch frame", run.rec, true, p)
	if res.Err != nil {
		t.Fatalf("sendRunningDelegateMessage: %+v", res)
	}
	child.mu.Lock()
	defer child.mu.Unlock()
	if len(child.steeringQueue) != 1 {
		t.Fatalf("child steering queue = %d, want 1", len(child.steeringQueue))
	}
	if !provenance.ContainsWatch(child.steeringQueue[0].Provenance, "watch_A", "wg_1") {
		t.Fatalf("child steering provenance = %+v, want watch_A/wg_1", child.steeringQueue[0].Provenance)
	}
}

// TestCrossWatchObserverResumeAdoptsDrivingWatchProvenance: an observer whose
// prior run was driven by watch_X is resumed by a watch send delivering watch_Y.
// The new run must be attributed to the CURRENT driving watch (watch_Y), not the
// watch that first drove the observer (watch_X) — otherwise the current watch's
// terminal notification is mis-attributed and not suppressed (the loop reopens).
func TestCrossWatchObserverResumeAdoptsDrivingWatchProvenance(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	parent := newDelegateTestSession(t, c)
	child := newDelegateTestSession(t, c)
	sub := completedDelegateSubagent(child, "already done")
	parent.subagents.track(sub)

	previousRestore := &jobstore.DelegateRestoreDescriptor{
		Version:        1,
		ChildSessionID: child.ID(),
		Provenance:     provenance.WithWatch(nil, "watch_X", "wg_x", "wd_x", parent.ID(), "caller"),
	}
	drivingWatch := provenance.WithWatch(nil, "watch_Y", "wg_y", "wd_y", parent.ID(), "caller")

	run, err := parent.attachDelegateJobWithRestoreAndDelegate(parent.jobManager, child.ID(), "observer", sub, jobstore.NewJobID(), nil, true, nil, previousRestore, delegateJobLink{
		delegateID: jobstore.NewDelegateID(),
		generation: jobstore.NewDelegateGeneration(),
		create:     true,
	}, drivingWatch)
	if err != nil {
		t.Fatalf("attachDelegateJobWithRestoreAndDelegate: %v", err)
	}

	if !provenance.ContainsWatch(run.rec.Provenance, "watch_Y", "wg_y") {
		t.Fatalf("run provenance = %+v, want driving watch_Y/wg_y", run.rec.Provenance)
	}
	if provenance.ContainsWatch(run.rec.Provenance, "watch_X", "wg_x") {
		t.Fatalf("run provenance = %+v, must not re-pin to prior watch_X/wg_x", run.rec.Provenance)
	}
	if !provenance.ContainsWatch(run.rec.DelegateRestore.Provenance, "watch_Y", "wg_y") {
		t.Fatalf("restore provenance = %+v, want driving watch_Y/wg_y", run.rec.DelegateRestore.Provenance)
	}
}
