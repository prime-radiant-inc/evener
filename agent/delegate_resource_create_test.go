package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/skill"
	taskpkg "primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
)

func TestDelegateResourceCreate_IsolationFailurePublishesNothing(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	wantErr := errors.New("injected sandbox isolation failure")
	root.cfg.testOnly.subagentPrepareFault = func(point string) error {
		if point == "sandbox_resolve" {
			return wantErr
		}
		return nil
	}

	result := root.createDelegate(context.Background(), delegateArgs{
		Task:                "remain unpublished",
		Sandbox:             "workspace-write",
		DelegationAllowance: 0,
	})
	if !errors.Is(result.Err, wantErr) {
		t.Fatalf("createDelegate error = %v, want isolation failure", result.Err)
	}
	root.delegateController.mu.Lock()
	defer root.delegateController.mu.Unlock()
	if len(root.delegateController.durable) != 0 || len(root.delegateController.live) != 0 || len(root.delegateController.reservations) != 0 {
		t.Fatalf("isolation failure published controller state: durable=%#v live=%#v reservations=%#v", root.delegateController.durable, root.delegateController.live, root.delegateController.reservations)
	}
	if root.delegateController.turnsInUse != 0 {
		t.Fatalf("isolation failure retained turn capacity = %d", root.delegateController.turnsInUse)
	}
}

func TestDelegateResourceCreate_StableRouteSkipsLegacyRetentionReservation(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	legacyReservationCalled := false
	root.cfg.testOnly.subagentReserveSlot = func(*Session) ([]*subagent, error) {
		legacyReservationCalled = true
		return nil, errors.New("legacy retained-session reservation invoked")
	}

	result := root.createDelegate(context.Background(), delegateArgs{
		Task:                "do not reclaim legacy sessions",
		DelegationAllowance: 0,
	})
	if legacyReservationCalled {
		t.Fatal("stable create invoked the legacy retained-session reservation seam")
	}
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
}

func TestDelegateResourceCreate_StableIdentityCommitsBeforeRuntimeLaunch(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	wantErr := errors.New("injected child construction failure")
	var committedID string
	root.cfg.testOnly.subagentPrepareFault = func(point string) error {
		if point != "new_session" {
			return nil
		}
		root.delegateController.mu.Lock()
		defer root.delegateController.mu.Unlock()
		for id, aggregate := range root.delegateController.durable {
			if aggregate != nil && aggregate.CurrentRunOpen && aggregate.Generation == 1 {
				committedID = id
			}
		}
		return wantErr
	}

	result := root.createDelegate(context.Background(), delegateArgs{
		Task:                "commit before construction",
		DelegationAllowance: 0,
	})
	if !errors.Is(result.Err, wantErr) {
		t.Fatalf("createDelegate error = %v, want construction failure", result.Err)
	}
	if committedID == "" {
		t.Fatal("child construction began before a stable delegate generation was durable")
	}
	if result.DelegateID != committedID {
		t.Fatalf("returned delegate ID = %q, want committed stable ID %q", result.DelegateID, committedID)
	}
}

func TestDelegateResourceCreate_CommittedUpdatePrecedesConstruction(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	constructorEntered := make(chan struct{})
	releaseConstructor := make(chan struct{})
	createDone := make(chan delegateResult, 1)
	wantErr := errors.New("injected constructor barrier failure")
	var updatesMu sync.Mutex
	var updates []delegateUpdatePlan
	root.delegateController.mu.Lock()
	root.delegateController.emitUpdate = func(plan delegateUpdatePlan) {
		updatesMu.Lock()
		updates = append(updates, plan)
		updatesMu.Unlock()
	}
	root.delegateController.mu.Unlock()
	root.cfg.testOnly.subagentPrepareFault = func(point string) error {
		if point != "new_session" {
			return nil
		}
		close(constructorEntered)
		<-releaseConstructor
		return wantErr
	}

	go func() {
		createDone <- root.createDelegate(context.Background(), delegateArgs{
			Task:                "publish before construction",
			DelegationAllowance: 0,
		})
	}()
	<-constructorEntered
	updatesMu.Lock()
	var committed delegateUpdatePlan
	if len(updates) != 0 {
		committed = updates[0]
	}
	updatesMu.Unlock()
	close(releaseConstructor)
	result := <-createDone
	if !errors.Is(result.Err, wantErr) {
		t.Fatalf("createDelegate error = %v, want constructor failure", result.Err)
	}
	if len(committed.rows) != 1 {
		t.Fatalf("stable updates before construction = %#v, want one committed row", committed.rows)
	}
	row := committed.rows[0]
	if row.id != result.DelegateID || row.phase != delegatestore.PhaseRunning || row.lifecycle != delegateLifecycleRunning || !row.resumable {
		t.Fatalf("committed update = %#v, want running resumable delegate %q", row, result.DelegateID)
	}
	aggregate := delegateAggregateSnapshot(t, root.delegateController, result.DelegateID)
	if aggregate.Phase != delegatestore.PhaseClosed || aggregate.Resumable {
		t.Fatalf("post-construction-failure aggregate = %#v, want closed and not resumable", aggregate)
	}
	if committed.rows[0].phase != delegatestore.PhaseRunning || !committed.rows[0].resumable {
		t.Fatalf("captured committed update changed after later failure: %#v", committed.rows[0])
	}
}

func TestDelegateResourceCreate_PostCommitConstructionFailureClosesResumability(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	wantErr := errors.New("injected permanent child construction failure")
	root.cfg.testOnly.subagentPrepareFault = func(point string) error {
		if point == "new_session" {
			return wantErr
		}
		return nil
	}

	result := root.createDelegate(context.Background(), delegateArgs{
		Task:                "fail after commit",
		DelegationAllowance: 0,
	})
	if !errors.Is(result.Err, wantErr) {
		t.Fatalf("createDelegate error = %v, want construction failure", result.Err)
	}
	aggregate := delegateAggregateSnapshot(t, root.delegateController, result.DelegateID)
	if aggregate.Phase != delegatestore.PhaseClosed || aggregate.CurrentRunOpen || aggregate.Resumable {
		t.Fatalf("post-commit failure aggregate = %#v, want closed and not resumable", aggregate)
	}
	if aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeFailed || aggregate.LatestOutcome.Reason != "construction_failed" {
		t.Fatalf("post-commit failure outcome = %#v, want failed/construction_failed", aggregate.LatestOutcome)
	}
	if aggregate.NotResumableReason != "construction_failed" {
		t.Fatalf("not-resumable reason = %q, want construction_failed", aggregate.NotResumableReason)
	}
}

func TestDelegateResourceCreate_RegisteredPostCommitFailureRetainsStableIdentityWithinMinimumLimit(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	root.reg.OverrideLimits(map[string]schema.ToolOutputLimit{
		"delegate": {MaxChars: 1, Strategy: schema.TruncTail},
	})
	enforceJobToolJSONLimits(root.reg)
	registered := root.reg.Get("delegate")
	if registered == nil || registered.Limit.MaxChars != jobToolResultMinJSONChars {
		t.Fatalf("registered delegate output limit = %#v, want enforced minimum %d", registered, jobToolResultMinJSONChars)
	}

	wantErr := errors.New(strings.Repeat("oversized post-commit construction diagnostic ", 200))
	root.cfg.testOnly.subagentPrepareFault = func(point string) error {
		if point == "new_session" {
			return wantErr
		}
		return nil
	}
	raw, err := json.Marshal(map[string]any{"task": "retain stable identity after oversized construction failure"})
	if err != nil {
		t.Fatal(err)
	}
	call := root.reg.ExecuteCall(context.Background(), root.currentEnv(), llm.ToolCallData{
		ID:        "task6-bounded-postcommit-failure",
		Name:      "delegate",
		Arguments: raw,
	})
	if call.IsError {
		t.Fatalf("registered post-commit failure returned transport error: %s", call.Output)
	}
	resultJSON := toolResultJSON(call)
	if got := jsonCharLen(resultJSON); got > jobToolResultMinJSONChars {
		t.Fatalf("registered post-commit result length = %d, want <= %d", got, jobToolResultMinJSONChars)
	}
	var result stableDelegateCreateResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatalf("decode registered post-commit result %q: %v", resultJSON, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(resultJSON, &fields); err != nil {
		t.Fatalf("decode registered post-commit result fields: %v", err)
	}
	if _, exists := fields["error"]; exists {
		t.Fatalf("bounded post-commit result retained oversized error diagnostic: %#v", fields)
	}

	aggregate := delegateAggregateSnapshot(t, root.delegateController, result.DelegateID)
	if aggregate.Phase != delegatestore.PhaseClosed || aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeFailed || aggregate.LatestOutcome.Reason != "construction_failed" || aggregate.Resumable {
		t.Fatalf("durable post-commit failure = %#v, want closed failed/construction_failed/not resumable", aggregate)
	}
	if result.DelegateID == "" || result.DelegateID != aggregate.DelegateID {
		t.Fatalf("registered delegate_id = %q, durable aggregate = %q", result.DelegateID, aggregate.DelegateID)
	}
	if result.ChildSessionID != aggregate.Descriptor.ChildSessionID {
		t.Fatalf("registered child_session_id = %q, durable descriptor = %q", result.ChildSessionID, aggregate.Descriptor.ChildSessionID)
	}
	if result.Type != delegateResourceType {
		t.Fatalf("registered type = %q, want delegate", result.Type)
	}
	if result.Status != string(aggregate.LatestOutcome.Status) || result.Reason != aggregate.LatestOutcome.Reason {
		t.Fatalf("registered outcome = %q/%q, durable outcome = %#v", result.Status, result.Reason, aggregate.LatestOutcome)
	}
	if result.Resumable == nil || *result.Resumable != aggregate.Resumable {
		t.Fatalf("registered resumability = %#v, durable resumability = %t", result.Resumable, aggregate.Resumable)
	}
	if result.TranscriptRef == "" || result.TranscriptRef != aggregate.Descriptor.TranscriptRef {
		t.Fatalf("registered transcript_ref = %q, durable descriptor = %q", result.TranscriptRef, aggregate.Descriptor.TranscriptRef)
	}
	wantModel := aggregate.Descriptor.ResolvedProfileID + "/" + aggregate.Descriptor.ResolvedModel
	if result.Model != wantModel {
		t.Fatalf("registered bounded model = %q, want retained diagnostic %q", result.Model, wantModel)
	}
}

func TestDelegateResourceCreate_ResultMatchesCommittedSnapshot(t *testing.T) {
	t.Run("stop wins before attach", func(t *testing.T) {
		root, _, _ := newDelegateResourceBootstrapSession(t)
		constructionErr := errors.New("construction cancelled after committed stop")
		root.cfg.testOnly.subagentPrepareFault = func(point string) error {
			if point != "new_session" {
				return nil
			}
			plan := root.delegateController.Snapshot()
			if len(plan.rows) != 1 {
				t.Fatalf("committed snapshot rows = %#v, want one delegate", plan.rows)
			}
			_, cancelPlan, _, err := root.delegateController.StopSubtree(rootDelegateActor(root.ID()), plan.rows[0].id)
			if err != nil {
				t.Fatalf("StopSubtree: %v", err)
			}
			executeDelegateCancelPlan(cancelPlan)
			return constructionErr
		}

		result := root.createDelegate(context.Background(), delegateArgs{Task: "stop before attach"})
		if !errors.Is(result.Err, constructionErr) {
			t.Fatalf("create error = %v, want construction diagnostic", result.Err)
		}
		if result.Status != jobstore.StatusStopped || result.Reason != "stopped_by_parent" || result.Resumable == nil || !*result.Resumable {
			t.Fatalf("create result = %#v, want stopped/stopped_by_parent/resumable", result)
		}
	})

	t.Run("permanent postcommit closure", func(t *testing.T) {
		root, _, _ := newDelegateResourceBootstrapSession(t)
		constructionErr := errors.New("permanent committed construction failure")
		root.cfg.testOnly.subagentPrepareFault = func(point string) error {
			if point == "new_session" {
				return constructionErr
			}
			return nil
		}

		result := root.createDelegate(context.Background(), delegateArgs{Task: "close failed start"})
		if !errors.Is(result.Err, constructionErr) {
			t.Fatalf("create error = %v, want construction diagnostic", result.Err)
		}
		if result.Status != jobstore.StatusFailed || result.Reason != "construction_failed" || result.Resumable == nil || *result.Resumable {
			t.Fatalf("create result = %#v, want failed/construction_failed/not resumable", result)
		}
	})

	t.Run("compensating append failure", func(t *testing.T) {
		root, _, _ := newDelegateResourceBootstrapSession(t)
		constructionErr := errors.New("construction failure before failed compensation")
		root.cfg.testOnly.subagentPrepareFault = func(point string) error {
			if point != "new_session" {
				return nil
			}
			if err := root.delegateController.store.Close(); err != nil {
				t.Fatalf("close delegate store: %v", err)
			}
			return constructionErr
		}

		result := root.createDelegate(context.Background(), delegateArgs{Task: "retain fenced start"})
		if !errors.Is(result.Err, constructionErr) || !strings.Contains(result.Err.Error(), "store is closed") {
			t.Fatalf("create error = %v, want construction and compensating append diagnostics", result.Err)
		}
		if result.Status != jobstore.StatusRunning || result.Reason != "" || result.Resumable == nil || !*result.Resumable {
			t.Fatalf("create result = %#v, want exact running/resumable committed snapshot", result)
		}
		root.delegateController.mu.Lock()
		live := root.delegateController.live[result.DelegateID]
		root.delegateController.mu.Unlock()
		if live == nil || !live.recoveryRequired {
			t.Fatalf("append-failed delegate live state = %#v, want recovery fence", live)
		}
	})
}

func TestDelegateResourceCreate_UsesFrozenDescriptorAfterCommit(t *testing.T) {
	root, client, profile := newDelegateResourceBootstrapSession(t)
	adapter := newTask6FrozenDescriptorAdapter()
	client.Register(adapter)
	t.Cleanup(adapter.releaseRun)

	loopDetection := false
	root.mu.Lock()
	root.cfg.ReasoningEffort = "low"
	root.cfg.UserInstructionOverride = "FROZEN USER INSTRUCTION"
	root.cfg.ShareTasksWithChildren = true
	root.cfg.ToolOutputLimits = map[string]schema.ToolOutputLimit{
		"read_file": {MaxChars: 111, MaxLines: 7, Strategy: schema.TruncHeadTail},
	}
	root.cfg.ModelFallbacks = []string{"openai/frozen-fallback"}
	root.cfg.EnableLoopDetection = &loopDetection
	root.cfg.MCPConfigFiles = []string{"parent-only.mcp.json"}
	root.cfg.MCPInline = []string{"parent-only:inline"}
	root.cfg.testOnly.minimalSystemPrompt = false
	wantConfig := root.cfg.toSnapshot()
	wantConfig.ToolOutputLimits = map[string]schema.ToolOutputLimit{
		"read_file": wantConfig.ToolOutputLimits["read_file"],
	}
	wantConfig.ModelFallbacks = append([]string(nil), wantConfig.ModelFallbacks...)
	wantLoopDetection := *wantConfig.EnableLoopDetection
	wantConfig.EnableLoopDetection = &wantLoopDetection
	root.mu.Unlock()
	rootTaskStore := root.getOrCreateTaskStore()
	rootTasks, err := rootTaskStore.Append([]taskpkg.TaskInput{{
		Type:        taskpkg.TaskTypeVerify,
		Description: "keep the committed root task store",
		Prompt:      "FROZEN TASK PROMPT",
	}})
	if err != nil {
		t.Fatalf("append root task: %v", err)
	}
	if err := rootTaskStore.Update([]taskpkg.TaskUpdate{{ID: rootTasks[0].ID, Status: taskpkg.TaskInProgress}}); err != nil {
		t.Fatalf("start root task: %v", err)
	}

	const (
		agentType       = "task6-frozen-reviewer"
		frozenRole      = "FROZEN ROLE PROMPT"
		frozenTask      = "FROZEN TASK PROMPT"
		frozenSkillBody = "FROZEN SKILL BODY"
		frozenUser      = "FROZEN USER INSTRUCTION"
		mutatedTask     = "MUTATED TASK PROMPT"
		mutatedSkill    = "MUTATED SKILL BODY"
		mutatedUser     = "MUTATED USER INSTRUCTION"
	)
	tools := []string{"read_file"}
	skills := []string{"task6-frozen-skill"}
	tasks := []taskpkg.TaskTemplate{{Title: "Frozen workflow", Prompt: frozenTask}}
	root.pluginAgents[agentType] = plugin.Agent{
		Name:         "frozen-reviewer",
		Description:  "Exercises committed delegate descriptors",
		Model:        "inherit",
		Tools:        tools,
		Skills:       skills,
		Tasks:        tasks,
		SystemPrompt: frozenRole,
		PluginName:   "task6-test",
	}
	frozenSkillFile := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(frozenSkillFile, []byte(frozenSkillBody), 0o600); err != nil {
		t.Fatal(err)
	}
	mutatedSkillFile := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(mutatedSkillFile, []byte(mutatedSkill), 0o600); err != nil {
		t.Fatal(err)
	}
	root.skills["task6-frozen-skill"] = skill.SkillMeta{Name: "task6-frozen-skill", SkillFile: frozenSkillFile}
	root.skills["task6-mutated-skill"] = skill.SkillMeta{Name: "task6-mutated-skill", SkillFile: mutatedSkillFile}
	wantConfig.MaxTurns = 500
	wantConfig.AgentName = "frozen-reviewer"
	wantConfig.ReasoningEffort = "low"
	wantConfig.MCPConfigFiles = nil
	wantConfig.MCPInline = nil
	wantConfig.Sandbox = ""
	wantConfig.SandboxNet = nil

	resultSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"frozen": map[string]any{"type": "string"},
		},
		"required": []string{"frozen"},
	}
	originalWorkDir := root.currentEnv().WorkingDirectory()
	originalProvenance := testProvenance("task6_frozen", "wg_original")
	root.replaceActiveProvenance(originalProvenance)
	mutatedWorkDir := t.TempDir()
	mutatedProvenance := testProvenance("task6_mutated", "wg_mutated")

	var mutateOnce sync.Once
	root.delegateController.mu.Lock()
	root.delegateController.emitUpdate = func(delegateUpdatePlan) {
		mutateOnce.Do(func() {
			mutated := root.pluginAgents[agentType]
			mutated.Tools[0] = "shell"
			mutated.Skills[0] = "task6-mutated-skill"
			mutated.Tasks[0].Prompt = mutatedTask
			root.pluginAgents[agentType] = mutated
			root.skills["task6-frozen-skill"] = skill.SkillMeta{Name: "task6-frozen-skill", SkillFile: mutatedSkillFile}
			resultSchema["properties"] = map[string]any{"mutated": map[string]any{"type": "boolean"}}
			root.mu.Lock()
			root.cfg.ReasoningEffort = "high"
			root.cfg.UserInstructionOverride = mutatedUser
			root.cfg.ShareTasksWithChildren = false
			root.cfg.ToolOutputLimits["read_file"] = schema.ToolOutputLimit{MaxChars: 999, MaxLines: 99, Strategy: schema.TruncTail}
			root.cfg.ModelFallbacks[0] = "openai/mutated-fallback"
			*root.cfg.EnableLoopDetection = true
			root.mu.Unlock()
			root.swapEnvAndRefresh(execenv.NewLocalExecutionEnvironment(mutatedWorkDir))
			root.replaceActiveProvenance(mutatedProvenance)
		})
	}
	root.delegateController.mu.Unlock()

	result := root.createDelegate(context.Background(), delegateArgs{
		Task:         "use the committed descriptor",
		AgentType:    agentType,
		Model:        "gpt-5.2",
		WatchParent:  true,
		ResultSchema: resultSchema,
	})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}

	var request llm.Request
	select {
	case request = <-adapter.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not receive the frozen delegate request")
	}
	adapter.releaseRun()

	aggregate := delegateAggregateSnapshot(t, root.delegateController, result.DelegateID)
	descriptor := aggregate.Descriptor
	childRecord := root.subagents.get(descriptor.ChildSessionID)
	if childRecord == nil || childRecord.sess == nil {
		t.Fatalf("committed child %q is not retained", descriptor.ChildSessionID)
	}
	child := childRecord.sess

	if descriptor.AgentType != agentType || descriptor.Config.AgentName != "frozen-reviewer" || descriptor.RequestedModel != "gpt-5.2" || descriptor.ResolvedProfileID != profile.ID() || descriptor.ResolvedModel != profile.Model() {
		t.Fatalf("committed identity/model descriptor = %#v", descriptor)
	}
	if request.SessionID != descriptor.ChildSessionID || request.Model != descriptor.ResolvedModel || child.currentProfile().ID() != descriptor.ResolvedProfileID || child.currentProfile().Model() != descriptor.ResolvedModel {
		t.Fatalf("provider/child identity = session %q model %q profile %s/%s, want descriptor %#v", request.SessionID, request.Model, child.currentProfile().ID(), child.currentProfile().Model(), descriptor)
	}
	if request.ReasoningEffort == nil || *request.ReasoningEffort != "low" || child.cfg.ReasoningEffort != descriptor.Config.ReasoningEffort {
		t.Fatalf("provider/child reasoning = %#v/%q, want frozen %q", request.ReasoningEffort, child.cfg.ReasoningEffort, descriptor.Config.ReasoningEffort)
	}
	if child.cfg.AgentName != descriptor.Config.AgentName || childRecord.agentType != descriptor.AgentType {
		t.Fatalf("child agent identity = %q/%q, want %q/%q", child.cfg.AgentName, childRecord.agentType, descriptor.Config.AgentName, descriptor.AgentType)
	}

	requestText := task6RequestText(request)
	for _, want := range []string{frozenRole, frozenTask, frozenSkillBody, frozenUser} {
		if !strings.Contains(requestText, want) {
			t.Fatalf("provider request omitted frozen prompt %q: %s", want, requestText)
		}
	}
	for _, forbidden := range []string{mutatedTask, mutatedSkill, mutatedUser} {
		if strings.Contains(requestText, forbidden) {
			t.Fatalf("provider request used post-commit prompt %q: %s", forbidden, requestText)
		}
	}
	if got := child.cfg.spawn.activatedSkillBodies; !reflect.DeepEqual(got, descriptor.FrozenSkillBodies) {
		t.Fatalf("child skill bodies = %#v, want descriptor %#v", got, descriptor.FrozenSkillBodies)
	}

	wantTools := append([]string(nil), descriptor.ToolNameCeiling...)
	gotTools := make([]string, 0, len(request.Tools))
	for _, definition := range request.Tools {
		gotTools = append(gotTools, definition.Name)
	}
	sort.Strings(wantTools)
	sort.Strings(gotTools)
	if !reflect.DeepEqual(gotTools, wantTools) {
		t.Fatalf("provider tools = %v, want exact frozen tools %v", gotTools, wantTools)
	}
	if !child.reg.RegisteredNames()["read_file"] || child.reg.RegisteredNames()["shell"] || !child.reg.RegisteredNames()["job_watch"] {
		t.Fatalf("child registry = %v, want frozen read_file + watch_parent grant without mutated shell", child.reg.Names())
	}

	var frozenResultSchema map[string]any
	if err := json.Unmarshal(descriptor.ResultSchema, &frozenResultSchema); err != nil {
		t.Fatalf("decode committed result schema: %v", err)
	}
	if got := task6CommunicateOutputSchema(t, request); !reflect.DeepEqual(got, frozenResultSchema) {
		t.Fatalf("provider result schema = %#v, want frozen %#v", got, frozenResultSchema)
	}
	if !reflect.DeepEqual(child.cfg.spawn.communicateOutputSchema, frozenResultSchema) || child.delegationAllowance != descriptor.DelegationAllowance {
		t.Fatalf("child result/allowance = %#v/%d, want %#v/%d", child.cfg.spawn.communicateOutputSchema, child.delegationAllowance, frozenResultSchema, descriptor.DelegationAllowance)
	}
	if child.currentEnv().WorkingDirectory() != descriptor.WorkingDir || descriptor.WorkingDir != originalWorkDir || localEnvPolicyName(child.currentEnv()) != descriptor.LocalEnvPolicy || child.cfg.spawn.isolation != descriptor.Isolation || sandboxSnapshotFromEnv(child.currentEnv()) != nil || descriptor.Sandbox != nil {
		t.Fatalf("child environment diverged from descriptor: cwd=%q policy=%q isolation=%q sandbox=%#v descriptor=%#v", child.currentEnv().WorkingDirectory(), localEnvPolicyName(child.currentEnv()), child.cfg.spawn.isolation, sandboxSnapshotFromEnv(child.currentEnv()), descriptor)
	}
	if !reflect.DeepEqual(descriptor.Provenance, originalProvenance) || !reflect.DeepEqual(child.activeCausalProvenance(), originalProvenance) {
		t.Fatalf("child provenance = %#v descriptor=%#v, want frozen %#v", child.activeCausalProvenance(), descriptor.Provenance, originalProvenance)
	}
	if !child.cfg.spawn.parentWatchGranted {
		t.Fatal("watch_parent was not passed through to the committed child")
	}
	if got := child.getOrCreateTaskStore(); got != rootTaskStore {
		t.Errorf("child task store = %p, want exact committed root store %p", got, rootTaskStore)
	}
	if got := child.Meta().Config; !reflect.DeepEqual(got, descriptor.Config) {
		t.Errorf("child meta config = %#v, want committed descriptor config %#v", got, descriptor.Config)
	}
	if !reflect.DeepEqual(descriptor.Config, wantConfig) {
		t.Errorf("committed descriptor config = %#v, want effective child config %#v", descriptor.Config, wantConfig)
	}
	if got := descriptor.SharedTaskStoreOwnerSessionID; got != root.ID() {
		t.Errorf("shared task store owner = %q, want root session %q", got, root.ID())
	}
}

func TestDelegateResourceCreate_PreservesCompleteNamedAgentWorkflowAfterCommit(t *testing.T) {
	root, client, _ := newDelegateResourceBootstrapSession(t)
	adapter := newTask6FrozenDescriptorAdapter()
	client.Register(adapter)
	t.Cleanup(adapter.releaseRun)

	const agentType = "task6-complete-workflow"
	templates := []taskpkg.TaskTemplate{
		{
			Title:           "Investigate the failure",
			Prompt:          "Trace the failing behavior to its source.",
			ReasoningEffort: "high",
			Type:            string(taskpkg.TaskTypeResearch),
		},
		{
			Title:  "Preserve the insertion step",
			Prompt: "Keep this literal step when no parent task templates are supplied.",
			Type:   string(taskpkg.TaskTypeVerify),
			Insert: "parent_tasks",
		},
		{
			Title:           "Implement the correction",
			Prompt:          "Make the smallest source-level correction.",
			ReasoningEffort: "low",
			Type:            string(taskpkg.TaskTypeFix),
		},
	}
	wantTemplates := append([]taskpkg.TaskTemplate(nil), templates...)
	root.pluginAgents[agentType] = plugin.Agent{
		Name:         "complete-workflow",
		Description:  "Exercises durable named-agent workflows",
		Model:        "inherit",
		Tools:        []string{"read_file"},
		Tasks:        templates,
		SystemPrompt: "Follow the complete workflow.",
		PluginName:   "task6-test",
	}

	var mutateOnce sync.Once
	root.delegateController.mu.Lock()
	root.delegateController.emitUpdate = func(delegateUpdatePlan) {
		mutateOnce.Do(func() {
			mutated := root.pluginAgents[agentType]
			for i := range mutated.Tasks {
				mutated.Tasks[i] = taskpkg.TaskTemplate{
					Title:           "MUTATED TITLE",
					Prompt:          "MUTATED PROMPT",
					ReasoningEffort: "medium",
					Type:            string(taskpkg.TaskTypeImplement),
					Insert:          "mutated_insert",
				}
			}
			root.pluginAgents[agentType] = mutated
		})
	}
	root.delegateController.mu.Unlock()

	result := root.createDelegate(context.Background(), delegateArgs{
		Task:      "run the complete committed workflow",
		AgentType: agentType,
	})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	select {
	case <-adapter.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not receive the named-agent workflow request")
	}
	adapter.releaseRun()

	descriptor := delegateAggregateSnapshot(t, root.delegateController, result.DelegateID).Descriptor
	if !reflect.DeepEqual(descriptor.TaskTemplates, wantTemplates) {
		t.Fatalf("committed task templates = %#v, want %#v", descriptor.TaskTemplates, wantTemplates)
	}
	childRecord := root.subagents.get(descriptor.ChildSessionID)
	if childRecord == nil || childRecord.sess == nil {
		t.Fatalf("committed child %q is not retained", descriptor.ChildSessionID)
	}
	got := childRecord.sess.getOrCreateTaskStore().View()
	if len(got) != len(wantTemplates) {
		t.Fatalf("child workflow task count = %d, want %d: %#v", len(got), len(wantTemplates), got)
	}
	for i, want := range wantTemplates {
		wantStatus := taskpkg.TaskOpen
		if i == 0 {
			wantStatus = taskpkg.TaskInProgress
		}
		if got[i].ID != i+1 || got[i].Description != want.Title || got[i].Prompt != want.Prompt || got[i].ReasoningEffort != want.ReasoningEffort || got[i].Type != taskpkg.TaskType(want.Type) || got[i].Insert != want.Insert || got[i].Status != wantStatus {
			t.Errorf("child workflow task %d = %#v, want id=%d title=%q prompt=%q reasoning=%q type=%q insert=%q status=%q", i, got[i], i+1, want.Title, want.Prompt, want.ReasoningEffort, want.Type, want.Insert, wantStatus)
		}
	}
}

func TestDelegateResourceCreate_ToolCapabilityCeiling(t *testing.T) {
	requestToolNames := func(request llm.Request) map[string]bool {
		names := make(map[string]bool, len(request.Tools))
		for _, definition := range request.Tools {
			names[definition.Name] = true
		}
		return names
	}

	t.Run("parent runtime-only tool is not required from child", func(t *testing.T) {
		root, client, _ := newDelegateResourceBootstrapSession(t)
		adapter := newTask6FrozenDescriptorAdapter()
		client.Register(adapter)
		t.Cleanup(adapter.releaseRun)

		const runtimeOnlyTool = "mcp__task6__runtime_only"
		root.RegisterTool(runtimeOnlyTool, "exists only on the live parent", map[string]any{"type": "object"}, func(context.Context, any) (any, error) {
			return nil, nil
		})

		result := root.createDelegate(context.Background(), delegateArgs{
			Task: "construct below the committed capability ceiling",
		})
		if result.Err != nil {
			t.Fatalf("createDelegate with parent runtime-only tool: %v", result.Err)
		}
		var request llm.Request
		select {
		case request = <-adapter.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("provider did not receive the capability-ceiling request")
		}
		adapter.releaseRun()

		descriptor := delegateAggregateSnapshot(t, root.delegateController, result.DelegateID).Descriptor
		if !hasString(descriptor.ToolNameCeiling, runtimeOnlyTool) {
			t.Fatalf("committed ceiling = %v, want parent-authorized runtime-only tool", descriptor.ToolNameCeiling)
		}
		child := root.subagents.get(descriptor.ChildSessionID)
		if child == nil || child.sess == nil {
			t.Fatalf("committed child %q is not retained", descriptor.ChildSessionID)
		}
		if child.sess.reg.RegisteredNames()[runtimeOnlyTool] || requestToolNames(request)[runtimeOnlyTool] {
			t.Fatalf("parent runtime-only tool reached child/provider: child=%v provider=%v", child.sess.reg.Names(), requestToolNames(request))
		}
	})

	t.Run("named policy survives post-commit parent registry mutation", func(t *testing.T) {
		root, client, _ := newDelegateResourceBootstrapSession(t)
		adapter := newTask6FrozenDescriptorAdapter()
		client.Register(adapter)
		t.Cleanup(adapter.releaseRun)

		const agentType = "task6-tool-ceiling-agent"
		root.pluginAgents[agentType] = plugin.Agent{
			Name:         "tool-ceiling-agent",
			Description:  "Freezes an explicit named-agent policy",
			Model:        "inherit",
			Tools:        []string{"read_file"},
			SystemPrompt: "Use only the committed tools.",
			PluginName:   "task6-test",
		}
		var mutateOnce sync.Once
		root.delegateController.mu.Lock()
		root.delegateController.emitUpdate = func(delegateUpdatePlan) {
			mutateOnce.Do(func() {
				mutated := root.pluginAgents[agentType]
				mutated.Tools[0] = "shell"
				root.pluginAgents[agentType] = mutated
				root.reg.Remove("read_file")
			})
		}
		root.delegateController.mu.Unlock()

		result := root.createDelegate(context.Background(), delegateArgs{
			Task:      "retain the committed named-agent policy",
			AgentType: agentType,
		})
		if result.Err != nil {
			t.Fatalf("createDelegate after parent registry mutation: %v", result.Err)
		}
		var request llm.Request
		select {
		case request = <-adapter.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("provider did not receive the frozen named-policy request")
		}
		adapter.releaseRun()

		descriptor := delegateAggregateSnapshot(t, root.delegateController, result.DelegateID).Descriptor
		if !hasString(descriptor.ToolNameCeiling, "read_file") || hasString(descriptor.ToolNameCeiling, "shell") {
			t.Fatalf("committed named-agent ceiling = %v, want read_file without shell", descriptor.ToolNameCeiling)
		}
		child := root.subagents.get(descriptor.ChildSessionID)
		if child == nil || child.sess == nil {
			t.Fatalf("committed child %q is not retained", descriptor.ChildSessionID)
		}
		childTools := child.sess.reg.RegisteredNames()
		providerTools := requestToolNames(request)
		if !childTools["read_file"] || !providerTools["read_file"] || childTools["shell"] || providerTools["shell"] {
			t.Fatalf("post-commit mutation changed named policy: child=%v provider=%v", child.sess.reg.Names(), providerTools)
		}
	})

	t.Run("recovery core tool absent from parent cannot reappear", func(t *testing.T) {
		root, client, _ := newDelegateResourceBootstrapSession(t)
		adapter := newTask6FrozenDescriptorAdapter()
		client.Register(adapter)
		t.Cleanup(adapter.releaseRun)
		root.reg.Remove("read_transcript")

		result := root.createDelegate(context.Background(), delegateArgs{
			Task: "do not regain the parent-removed recovery reader",
		})
		if result.Err != nil {
			t.Fatalf("createDelegate without parent recovery reader: %v", result.Err)
		}
		var request llm.Request
		select {
		case request = <-adapter.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("provider did not receive the parent-restricted request")
		}
		adapter.releaseRun()

		descriptor := delegateAggregateSnapshot(t, root.delegateController, result.DelegateID).Descriptor
		if hasString(descriptor.ToolNameCeiling, "read_transcript") {
			t.Fatalf("committed ceiling regained parent-removed recovery reader: %v", descriptor.ToolNameCeiling)
		}
		child := root.subagents.get(descriptor.ChildSessionID)
		if child == nil || child.sess == nil {
			t.Fatalf("committed child %q is not retained", descriptor.ChildSessionID)
		}
		if child.sess.reg.RegisteredNames()["read_transcript"] || requestToolNames(request)["read_transcript"] {
			t.Fatalf("parent-removed recovery reader reappeared: child=%v provider=%v", child.sess.reg.Names(), requestToolNames(request))
		}
	})
}

func TestDelegateResourceCreate_RestoredRootStartsNewChildWithStartupHooks(t *testing.T) {
	const (
		startupMarker = "TASK6 STARTUP CHILD HOOK"
		resumeMarker  = "TASK6 RESUME CHILD HOOK"
	)
	pluginDir := makePluginDir(t, "task6-child-start-kind")
	hooksDir := filepath.Join(pluginDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hooksJSON := `{
		"hooks": {
			"SessionStart": [
				{"matcher": "startup", "hooks": [{"type": "command", "command": "echo TASK6 STARTUP CHILD HOOK"}]},
				{"matcher": "resume", "hooks": [{"type": "command", "command": "echo TASK6 RESUME CHILD HOOK"}]}
			]
		}
	}`
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(hooksJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	stateDir := t.TempDir()
	workspace := t.TempDir()
	client := llm.NewClient()
	adapter := newTask6FrozenDescriptorAdapter()
	client.Register(adapter)
	t.Cleanup(adapter.releaseRun)
	profile := NewOpenAIProfile("gpt-5.2")
	seed, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 2,
		PluginDirs:       []string{pluginDir},
		NoProjectPrompts: true,
		ForceRealIO:      true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	meta := seed.Meta()
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		seed.Close()
		t.Fatal(err)
	}
	seed.Close()

	root, err := RestoreSessionFromMetaWithConfig(client, profile, execenv.NewLocalExecutionEnvironment(workspace), meta, RestoreSessionConfig{
		StateDir:    stateDir,
		ForceRealIO: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
		},
	})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(root.Close)
	if got := root.cfg.SessionStartKind; got != plugin.SessionStartKindResume {
		t.Fatalf("restored root SessionStartKind = %q, want resume", got)
	}

	result := root.createDelegate(context.Background(), delegateArgs{
		Task:                "start a new child from the restored root",
		DelegationAllowance: 0,
	})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}

	var request llm.Request
	select {
	case request = <-adapter.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not receive the new child's request")
	}
	adapter.releaseRun()

	requestText := task6RequestText(request)
	if !strings.Contains(requestText, startupMarker) {
		t.Fatalf("new child provider request omitted startup hook marker: %s", requestText)
	}
	if strings.Contains(requestText, resumeMarker) {
		t.Fatalf("new child provider request included restored-root resume hook marker: %s", requestText)
	}
}

func TestDelegateResourceCreate_PostCommitFailureRemainsInspectableAfterRestart(t *testing.T) {
	root, client, profile := newDelegateResourceBootstrapSession(t)
	wantErr := errors.New("injected permanent child construction failure")
	root.cfg.testOnly.subagentPrepareFault = func(point string) error {
		if point == "new_session" {
			return wantErr
		}
		return nil
	}

	result := root.createDelegate(context.Background(), delegateArgs{
		Task:                "remain inspectable",
		DelegationAllowance: 0,
	})
	if !errors.Is(result.Err, wantErr) {
		t.Fatalf("createDelegate error = %v, want construction failure", result.Err)
	}
	meta := root.Meta()
	if err := schema.SaveSessionMeta(root.stateDir, meta); err != nil {
		t.Fatal(err)
	}
	stateDir := root.stateDir
	workspace := root.currentEnv().WorkingDirectory()
	root.Close()

	restored, err := restoreDelegateResourceBootstrapSession(client, profile, workspace, meta, stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer restored.Close()
	aggregate := delegateAggregateSnapshot(t, restored.delegateController, result.DelegateID)
	if aggregate.Phase != delegatestore.PhaseClosed || aggregate.Resumable || aggregate.NotResumableReason != "construction_failed" {
		t.Fatalf("restored failed delegate = %#v, want inspectable closed aggregate", aggregate)
	}
}

func TestDelegateResourceCreate_MissingRestoreInputsCloseResumabilityBeforeCleanup(t *testing.T) {
	root, client, profile := newDelegateResourceBootstrapSession(t)
	descriptor := task6DelegateDescriptor("missing restore transcript")
	descriptor.Isolation = "worktree"
	reservation, err := root.delegateController.ReserveCreate(rootDelegateActor(root.ID()), descriptor)
	if err != nil {
		t.Fatalf("ReserveCreate: %v", err)
	}
	if err := os.MkdirAll(reservation.worktreePath, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinelPath := filepath.Join(reservation.worktreePath, "retained-artifact")
	if err := os.WriteFile(sentinelPath, []byte("retain"), 0o600); err != nil {
		t.Fatal(err)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	meta := root.Meta()
	if err := schema.SaveSessionMeta(root.stateDir, meta); err != nil {
		t.Fatal(err)
	}
	stateDir := root.stateDir
	workspace := root.currentEnv().WorkingDirectory()
	root.Close()

	restored, err := restoreDelegateResourceBootstrapSession(client, profile, workspace, meta, stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer restored.Close()
	aggregate := delegateAggregateSnapshot(t, restored.delegateController, started.lease.delegateID)
	if aggregate.Phase != delegatestore.PhaseClosed || aggregate.Resumable || aggregate.NotResumableReason != notResumableMissingChildSessionMeta {
		t.Fatalf("missing-input aggregate = %#v, want closed/%s", aggregate, notResumableMissingChildSessionMeta)
	}
	if got, err := os.ReadFile(sentinelPath); err != nil || string(got) != "retain" {
		t.Fatalf("artifact was cleaned before durable resumability closure: bytes=%q err=%v", got, err)
	}
}

func TestDelegateResourceCreate_ResumabilityAppendFailureDestroysNothing(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	wantErr := errors.New("injected permanent child construction failure")
	var artifactPath string
	root.cfg.testOnly.subagentPrepareFault = func(point string) error {
		if point != "new_session" {
			return nil
		}
		root.delegateController.mu.Lock()
		for _, aggregate := range root.delegateController.durable {
			if aggregate != nil && aggregate.CurrentRunOpen {
				artifactPath = filepath.Join(root.stateDir, sessionsSubdir, aggregate.Descriptor.ChildSessionID+".transcript.jsonl")
				break
			}
		}
		root.delegateController.mu.Unlock()
		if artifactPath != "" {
			if err := os.MkdirAll(filepath.Dir(artifactPath), 0o700); err != nil {
				t.Fatalf("create artifact parent: %v", err)
			}
			if err := os.WriteFile(artifactPath, []byte("retained exact artifact"), 0o600); err != nil {
				t.Fatalf("create artifact: %v", err)
			}
		}
		if err := root.delegateController.store.Close(); err != nil {
			t.Fatalf("close delegate store: %v", err)
		}
		return wantErr
	}

	result := root.createDelegate(context.Background(), delegateArgs{
		Task:                "retain after append failure",
		DelegationAllowance: 0,
	})
	if !errors.Is(result.Err, wantErr) || !strings.Contains(result.Err.Error(), "store is closed") {
		t.Fatalf("createDelegate error = %v, want construction and closure-append failures", result.Err)
	}
	root.delegateController.mu.Lock()
	aggregate := root.delegateController.durable[result.DelegateID]
	live := root.delegateController.live[result.DelegateID]
	turnsInUse := root.delegateController.turnsInUse
	root.delegateController.mu.Unlock()
	if aggregate == nil || aggregate.Phase != delegatestore.PhaseRunning || !aggregate.CurrentRunOpen {
		t.Fatalf("append-failed aggregate = %#v, want exact running generation retained", aggregate)
	}
	if live == nil || live.binding == nil || !live.recoveryRequired || turnsInUse != 1 {
		t.Fatalf("append-failed live state = %#v capacity=%d, want fenced binding and retained capacity", live, turnsInUse)
	}
	if got, err := os.ReadFile(artifactPath); err != nil || string(got) != "retained exact artifact" {
		t.Fatalf("append failure destroyed exact artifact: bytes=%q err=%v", got, err)
	}
}

func TestDelegateResourceCreate_StopBeforeAttachDisposesUnadoptedSession(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	runtime, started, isolation, prepared := prepareCommittedUnadoptedDelegate(t, root, "stop before parent attach")
	_, cancelPlan, _, err := root.delegateController.StopSubtree(rootDelegateActor(root.ID()), started.lease.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)

	result := runtime.failCommittedStart(started, isolation, prepared, true, context.Canceled, "construction_failed")
	if result.DelegateID != started.lease.delegateID {
		t.Fatalf("failed create delegate ID = %q, want %q", result.DelegateID, started.lease.delegateID)
	}
	if got := root.subagents.get(prepared.sub.id); got != nil {
		t.Fatalf("stop-winning unadopted child was inserted into parent manager: result_err=%v disposition=%d child=%#v", result.Err, committedStartFailureDisposition(result.Err), got)
	}
	if got := prepared.sub.sess.State(); got != SessionClosed {
		t.Fatalf("stop-winning unadopted child state = %q, want %q", got, SessionClosed)
	}
	aggregate := delegateAggregateSnapshot(t, root.delegateController, started.lease.delegateID)
	if !aggregate.Resumable || aggregate.CurrentRunOpen {
		t.Fatalf("stop-winning delegate = %#v, want settled with durable resumability retained", aggregate)
	}
	root.delegateController.mu.Lock()
	live := root.delegateController.live[started.lease.delegateID]
	var resident *Session
	if live != nil {
		resident = live.runtime
	}
	root.delegateController.mu.Unlock()
	if resident != nil {
		t.Errorf("stop-winning delegate retained runtime %p in state %q, want no closed controller runtime", resident, resident.State())
	}

	if _, err := root.delegateController.Reconcile(emptyDelegateReconcileEvidence(root.delegateController)); err != nil {
		t.Fatalf("complete stop: %v", err)
	}
	reservation, err := root.delegateController.ReserveStart(rootDelegateActor(root.ID()), started.lease.delegateID)
	if err != nil {
		t.Fatalf("ReserveStart replacement: %v", err)
	}
	replacementStart, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart replacement: %v", err)
	}
	replacement := newTestSession(t)
	if err := root.delegateController.AttachRuntime(replacementStart.lease, replacement); err != nil {
		t.Fatalf("AttachRuntime replacement after stopped-start cleanup: %v", err)
	}
}

func TestDelegateResourceCreate_StopSettlementTransfersRuntimeBeforeUpdateEmission(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	runtime, started, isolation, prepared := prepareCommittedUnadoptedDelegate(t, root, "stop settlement runtime transfer")
	_, cancelPlan, _, err := root.delegateController.StopSubtree(rootDelegateActor(root.ID()), started.lease.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)

	updateEntered := make(chan struct{})
	releaseUpdate := make(chan struct{})
	var updateOnce sync.Once
	root.delegateController.mu.Lock()
	root.delegateController.emitUpdate = func(delegateUpdatePlan) {
		updateOnce.Do(func() { close(updateEntered) })
		<-releaseUpdate
	}
	root.delegateController.mu.Unlock()

	constructionErr := errors.New("construction failed after controller attachment")
	resultCh := make(chan delegateResult, 1)
	go func() {
		resultCh <- runtime.failCommittedStart(started, isolation, prepared, true, constructionErr, "construction_failed")
	}()
	<-updateEntered

	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseUpdate) })
	}
	t.Cleanup(release)

	root.delegateController.mu.Lock()
	live := root.delegateController.live[started.lease.delegateID]
	var resident *Session
	if live != nil {
		resident = live.runtime
	}
	root.delegateController.mu.Unlock()
	if resident != nil {
		t.Errorf("controller retained stopped unadopted runtime %p before update emission, want close ownership already transferred", resident)
	}

	_, reconcileErr := root.delegateController.Reconcile(emptyDelegateReconcileEvidence(root.delegateController))
	var reserveErr, commitErr, attachErr error
	var replacement *Session
	if reconcileErr == nil {
		var reservation *delegateStartReservation
		reservation, reserveErr = root.delegateController.ReserveStart(rootDelegateActor(root.ID()), started.lease.delegateID)
		if reserveErr == nil {
			var replacementStart delegateStartCommit
			replacementStart, commitErr = root.delegateController.CommitStart(reservation)
			if commitErr == nil {
				replacement = newTestSession(t)
				attachErr = root.delegateController.AttachRuntime(replacementStart.lease, replacement)
			}
		}
	}
	release()
	result := <-resultCh

	if reconcileErr != nil {
		t.Errorf("complete stop while cleanup blocked: %v", reconcileErr)
	}
	if reserveErr != nil {
		t.Errorf("ReserveStart replacement while cleanup blocked: %v", reserveErr)
	}
	if commitErr != nil {
		t.Errorf("CommitStart replacement while cleanup blocked: %v", commitErr)
	}
	if attachErr != nil {
		t.Errorf("AttachRuntime replacement while cleanup blocked: %v", attachErr)
	}
	if replacement == nil && reconcileErr == nil && reserveErr == nil && commitErr == nil {
		t.Error("replacement runtime was not constructed")
	}
	if !errors.Is(result.Err, constructionErr) {
		t.Errorf("failed create error = %v, want %v", result.Err, constructionErr)
	}
	if got := prepared.sub.sess.State(); got != SessionClosed {
		t.Errorf("stop-winning unadopted child state = %q after cleanup, want %q", got, SessionClosed)
	}
}

func TestDelegateResourceCreate_CloseAfterDrainRefusesFailedStartRetention(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	runtime, started, isolation, prepared := prepareCommittedUnadoptedDelegate(t, root, "close after manager drain")
	root.subagents.drainForClose()
	if err := root.delegateController.store.Close(); err != nil {
		t.Fatalf("close delegate store: %v", err)
	}

	constructionErr := errors.New("construction failed after controller attachment")
	result := runtime.failCommittedStart(started, isolation, prepared, true, constructionErr, "construction_failed")
	if !errors.Is(result.Err, constructionErr) || !strings.Contains(result.Err.Error(), "store is closed") {
		t.Fatalf("failed create error = %v, want construction and append failures", result.Err)
	}
	if got := root.subagents.get(prepared.sub.id); got != nil {
		t.Fatalf("late failed-start retention escaped the close drain: %#v", got)
	}
	if got := prepared.sub.sess.State(); got != SessionClosed {
		t.Fatalf("late failed-start candidate state = %q, want %q", got, SessionClosed)
	}
	aggregate := delegateAggregateSnapshot(t, root.delegateController, started.lease.delegateID)
	root.delegateController.mu.Lock()
	live := root.delegateController.live[started.lease.delegateID]
	root.delegateController.mu.Unlock()
	if aggregate.Phase != delegatestore.PhaseRunning || !aggregate.CurrentRunOpen || !aggregate.Resumable || live == nil || !live.recoveryRequired {
		t.Fatalf("append-failed delegate = aggregate %#v live %#v, want fenced exact generation", aggregate, live)
	}
	for _, path := range []string{
		started.transcriptPath,
		filepath.Join(root.stateDir, sessionsSubdir, prepared.sub.id+".meta.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("durable isolation artifact %s was not retained: %v", path, err)
		}
	}
}

func prepareCommittedUnadoptedDelegate(t *testing.T, root *Session, task string) (delegateRuntime, delegateStartCommit, delegateIsolation, *preparedSubagentRun) {
	t.Helper()
	runtime := delegateRuntime{owner: root}
	ctx := context.Background()
	args := delegateArgs{Task: task, DelegationAllowance: 0}
	selection, err := root.selectSubagentModel(ctx, args.Model, args.AgentType)
	if err != nil {
		t.Fatalf("selectSubagentModel: %v", err)
	}
	descriptor, project, err := runtime.describe(ctx, args, task, "", nil, selection)
	if err != nil {
		t.Fatalf("describe delegate: %v", err)
	}
	reservation, err := root.delegateController.ReserveCreate(rootDelegateActor(root.ID()), descriptor)
	if err != nil {
		t.Fatalf("ReserveCreate: %v", err)
	}
	isolation, err := runtime.prepareIsolation(ctx, reservation, project, nil)
	if err != nil {
		t.Fatalf("prepareIsolation: %v", err)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart: %v", err)
	}
	prepared, err := runtime.construct(ctx, args, selection, started, isolation)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	t.Cleanup(prepared.disposeUnadopted)
	if err := root.delegateController.AttachRuntime(started.lease, prepared.sub.sess); err != nil {
		t.Fatalf("AttachRuntime: %v", err)
	}
	return runtime, started, isolation, prepared
}

func TestDelegateResourceCreate_DescendantEventCallbackSurvivesSpawnConfig(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	observed := make(chan events.SessionEvent, 1)
	var callbackMu sync.Mutex
	targetSessionID := ""
	root.SetDescendantEventFunc(func(event events.SessionEvent) {
		callbackMu.Lock()
		target := targetSessionID
		callbackMu.Unlock()
		warning, ok := event.Data.(events.WarningData)
		if event.Kind == events.EventWarning && event.SessionID == target && ok && warning.Message == "task6 descendant sentinel" {
			observed <- event
		}
	})
	result := root.createDelegate(context.Background(), delegateArgs{
		Task:                "preserve descendant callback",
		DelegationAllowance: 0,
	})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	children := root.subagents.sessions()
	if len(children) != 1 {
		t.Fatalf("tracked child count = %d, want 1", len(children))
	}
	child := children[0]
	if child.descendantEvent == nil {
		t.Fatal("child lost root descendant-event callback")
	}
	callbackMu.Lock()
	targetSessionID = child.ID()
	callbackMu.Unlock()
	want := events.SessionEvent{Kind: events.EventWarning, SessionID: child.ID(), Data: events.WarningData{Message: "task6 descendant sentinel"}}
	child.descendantEvent(want)
	select {
	case got := <-observed:
		if got.Kind != want.Kind || got.SessionID != want.SessionID {
			t.Fatalf("descendant callback event = %#v, want %#v", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("descendant callback did not receive child event")
	}
}

func TestDelegateResourceCreate_ChildTranscriptIsPreseededBeforeRun(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	adapter := newTask6TranscriptBarrierAdapter()
	t.Cleanup(adapter.releaseRun)
	client := llm.NewClient()
	client.Register(adapter)
	root, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 2,
		NoProjectPrompts: true,
		ForceRealIO:      true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)

	const task = "preseed this exact task"
	result := root.createDelegate(context.Background(), delegateArgs{
		Task:                task,
		DelegationAllowance: 0,
	})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	var childID string
	select {
	case childID = <-adapter.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider was not reached")
	}
	_, entries, _, err := readTranscript(filepath.Join(stateDir, sessionsSubdir, childID+".transcript.jsonl"))
	if err != nil {
		t.Fatalf("read child transcript at provider boundary: %v", err)
	}
	matches := 0
	for _, entry := range entries {
		if entry.Turn.Kind == schema.TurnUserInput && entry.Turn.Message.Text() == task {
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("child transcript at provider boundary has %d exact user inputs, want 1: %#v", matches, entries)
	}
	adapter.releaseRun()
}

func TestDelegateResourceCreate_InputTranscriptAppendRunsAfterControllerUnlock(t *testing.T) {
	root, client, _ := newDelegateResourceBootstrapSession(t)
	adapter := newTask6TranscriptBarrierAdapter()
	client.Register(adapter)
	t.Cleanup(adapter.releaseRun)

	observerCalls := 0
	controllerUnlocked := false
	root.cfg.testOnly.delegateInitialInputAppend = func(*Session) {
		observerCalls++
		controllerUnlocked = root.delegateController.mu.TryLock()
		if controllerUnlocked {
			root.delegateController.mu.Unlock()
		}
	}

	const task = "registered input persists outside controller lock"
	result := executeTask6RegisteredDelegate(t, context.Background(), root, task, 0)
	var providerChildID string
	select {
	case providerChildID = <-adapter.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider was not reached after registered stable create")
	}
	if observerCalls != 1 {
		t.Fatalf("initial input append observer calls = %d, want 1 real boundary", observerCalls)
	}
	if !controllerUnlocked {
		t.Fatal("real initial transcript append boundary ran while the delegate controller mutex was held")
	}
	childID, _ := result["child_session_id"].(string)
	if providerChildID != childID {
		t.Fatalf("provider child = %q, want registered child %q", providerChildID, childID)
	}
	_, entries, _, err := readTranscript(filepath.Join(root.stateDir, sessionsSubdir, childID+".transcript.jsonl"))
	if err != nil {
		t.Fatalf("read real child transcript at provider boundary: %v", err)
	}
	matches := 0
	for _, entry := range entries {
		if entry.Turn.Kind == schema.TurnUserInput && entry.Turn.Message.Text() == task {
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("real child transcript at provider boundary has %d exact user inputs, want 1: %#v", matches, entries)
	}
	adapter.releaseRun()
}

func TestDelegateResourceCreate_RegisteredToolReturnsOnlyStableDelegateIdentity(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	result := executeTask6RegisteredDelegate(t, context.Background(), root, "registered stable identity", 0)

	delegateID, _ := result["delegate_id"].(string)
	if err := identifier.ValidateDelegateID(delegateID); err != nil {
		t.Fatalf("registered delegate_id = %q: %v", delegateID, err)
	}
	childSessionID, _ := result["child_session_id"].(string)
	if err := identifier.ValidateSessionID(childSessionID); err != nil {
		t.Fatalf("registered child_session_id = %q: %v", childSessionID, err)
	}
	if transcriptRef, _ := result["transcript_ref"].(string); transcriptRef != encodeRef("", childSessionID) {
		t.Fatalf("registered transcript_ref = %q, want child transcript reference", transcriptRef)
	}
	if got := result["type"]; got != "delegate" {
		t.Fatalf("registered type = %#v, want delegate", got)
	}
	for _, forbidden := range []string{
		"job_id", "started_job_id", "latest_job_id", "current_job_id", "activation_job_id",
		"output", "truncated", "structured_result", "running_in_background", "timed_out",
	} {
		if value, exists := result[forbidden]; exists {
			t.Fatalf("registered create returned activation field %q=%#v", forbidden, value)
		}
	}
}

func TestDelegateResourceCreate_RegisteredSchemaOmitsCreationMaxWait(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	for _, tc := range []struct {
		name        string
		wantMaxWait bool
	}{
		{name: "delegate", wantMaxWait: false},
		{name: "delegate_send", wantMaxWait: true},
		{name: "job_stop", wantMaxWait: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registered := root.reg.Get(tc.name)
			if registered == nil {
				t.Fatalf("registered %s tool is absent", tc.name)
			}
			properties, ok := registered.Tool.Definition.Parameters["properties"].(map[string]any)
			if !ok {
				t.Fatalf("registered %s properties = %T, want map[string]any", tc.name, registered.Tool.Definition.Parameters["properties"])
			}
			_, gotMaxWait := properties["max_wait_ms"]
			if gotMaxWait != tc.wantMaxWait {
				t.Fatalf("registered %s max_wait_ms presence = %t, want %t", tc.name, gotMaxWait, tc.wantMaxWait)
			}
		})
	}
}

func TestDelegateResourceCreate_RegisteredRejectsCreationMaxWait(t *testing.T) {
	root, client, _ := newDelegateResourceBootstrapSession(t)
	provider := newTask6TranscriptBarrierAdapter()
	client.Register(provider)
	t.Cleanup(provider.releaseRun)

	constructionReached := ""
	root.cfg.testOnly.subagentPrepareFault = func(point string) error {
		if point == "new_session" {
			constructionReached = point
		}
		return nil
	}
	storePath := delegateResourceStorePath(root.stateDir, root.ID())
	storeBefore, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{
		"task":        "reject creation wait",
		"max_wait_ms": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	call := root.reg.ExecuteCall(context.Background(), root.currentEnv(), llm.ToolCallData{
		ID:        "task6-reject-creation-wait",
		Name:      "delegate",
		Arguments: raw,
	})
	if !call.IsError {
		t.Errorf("registered delegate accepted creation max_wait_ms: %#v", call)
	}
	const registryRejectionPrefix = "tool args schema validation failed:"
	if !strings.HasPrefix(call.Output, registryRejectionPrefix) {
		t.Errorf("registered delegate rejection = %q, want prefix %q", call.Output, registryRejectionPrefix)
	}
	for _, evidence := range []string{"additionalProperties", "max_wait_ms"} {
		if !strings.Contains(call.Output, evidence) {
			t.Errorf("registered delegate rejection = %q, want schema evidence %q", call.Output, evidence)
		}
	}
	if !call.IsError {
		select {
		case childID := <-provider.entered:
			t.Errorf("registered delegate reached provider for child %q", childID)
		case <-time.After(5 * time.Second):
			t.Error("registered delegate admitted creation max_wait_ms but did not reach the provider sentinel")
		}
	} else {
		select {
		case childID := <-provider.entered:
			t.Errorf("registered delegate schema rejection reached provider for child %q", childID)
		default:
		}
	}
	if constructionReached != "" {
		t.Errorf("registered delegate schema rejection reached child construction at %q", constructionReached)
	}
	storeAfter, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storeAfter, storeBefore) {
		t.Errorf("delegate store bytes changed after registered schema rejection: before=%d bytes after=%d bytes", len(storeBefore), len(storeAfter))
	}
	root.delegateController.mu.Lock()
	defer root.delegateController.mu.Unlock()
	if len(root.delegateController.durable) != 0 || len(root.delegateController.reservations) != 0 || len(root.delegateController.live) != 0 {
		t.Fatalf("rejected creation wait mutated controller: durable=%d reservations=%d live=%d", len(root.delegateController.durable), len(root.delegateController.reservations), len(root.delegateController.live))
	}
}

func TestDelegateResourceCreate_RegisteredToolUsesRootController(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	wantID := identifier.MustNewDelegateID()
	root.delegateController.mu.Lock()
	root.delegateController.newDelegateID = func() string { return wantID }
	root.delegateController.mu.Unlock()

	result := executeTask6RegisteredDelegate(t, context.Background(), root, "registered root controller", 0)
	if got, _ := result["delegate_id"].(string); got != wantID {
		t.Fatalf("registered delegate_id = %q, want controller-minted %q", got, wantID)
	}
	aggregate := delegateAggregateSnapshot(t, root.delegateController, wantID)
	if aggregate.Descriptor.OwnerSessionID != root.ID() {
		t.Fatalf("registered owner session = %q, want root %q", aggregate.Descriptor.OwnerSessionID, root.ID())
	}
	for _, record := range root.jobManager.list(listFilter{IncludeNested: true}) {
		if string(record.Type) == delegateResourceType {
			t.Fatalf("registered stable create wrote delegate JobRecord %#v", record)
		}
	}
}

func TestDelegateResourceCreate_RegisteredNestedCreateUsesCurrentLease(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	parentResult := executeTask6RegisteredDelegate(t, context.Background(), root, "registered parent", 1)
	parentID, _ := parentResult["delegate_id"].(string)
	parentChildID, _ := parentResult["child_session_id"].(string)

	root.delegateController.mu.Lock()
	parentLive := root.delegateController.live[parentID]
	if parentLive == nil || parentLive.binding == nil {
		root.delegateController.mu.Unlock()
		t.Fatalf("registered parent %q has no live generation binding", parentID)
	}
	parentLease := parentLive.binding.lease
	root.delegateController.mu.Unlock()
	parent := root.subagents.get(parentChildID)
	if parent == nil || parent.sess == nil {
		t.Fatalf("registered parent child session %q is not retained", parentChildID)
	}
	leaseContext := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, parentLease)
	childResult := executeTask6RegisteredDelegate(t, leaseContext, parent.sess, "registered nested child", 0)
	childID, _ := childResult["delegate_id"].(string)

	aggregate := delegateAggregateSnapshot(t, root.delegateController, childID)
	if aggregate.Descriptor.ParentDelegateID != parentID {
		t.Fatalf("nested parent delegate = %q, want current lease owner %q", aggregate.Descriptor.ParentDelegateID, parentID)
	}
	if aggregate.Descriptor.OwnerSessionID != root.ID() {
		t.Fatalf("nested owner session = %q, want root %q", aggregate.Descriptor.OwnerSessionID, root.ID())
	}
}

func executeTask6RegisteredDelegate(t *testing.T, ctx context.Context, session *Session, task string, allowance int) map[string]any {
	t.Helper()
	arguments := map[string]any{"task": task}
	if allowance != 0 {
		arguments["delegation_allowance"] = allowance
	}
	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	call := session.reg.ExecuteCall(ctx, session.currentEnv(), llm.ToolCallData{
		ID:        "task6-registered-" + strings.ReplaceAll(task, " ", "-"),
		Name:      "delegate",
		Arguments: raw,
	})
	if call.IsError {
		t.Fatalf("registered delegate returned error: %s", call.Output)
	}
	var result map[string]any
	if err := json.Unmarshal(toolResultJSON(call), &result); err != nil {
		t.Fatalf("decode registered delegate output %q: %v", call.Output, err)
	}
	return result
}

func task6DelegateDescriptor(task string) delegatestore.Descriptor {
	return delegatestore.Descriptor{
		Task:              task,
		AgentType:         "default",
		ResolvedProfileID: "openai",
		ResolvedModel:     "gpt-5.2",
		ToolNameCeiling:   []string{"communicate"},
		Resumable:         true,
	}
}

func delegateAggregateSnapshot(t *testing.T, controller *delegateTreeController, delegateID string) delegatestore.Aggregate {
	t.Helper()
	controller.mu.Lock()
	defer controller.mu.Unlock()
	aggregate := controller.durable[delegateID]
	if aggregate == nil {
		t.Fatalf("delegate %q is absent from stable controller", delegateID)
	}
	return *aggregate
}

type task6TranscriptBarrierAdapter struct {
	entered     chan string
	release     chan struct{}
	enteredOnce sync.Once
	releaseOnce sync.Once
}

func newTask6TranscriptBarrierAdapter() *task6TranscriptBarrierAdapter {
	return &task6TranscriptBarrierAdapter{
		entered: make(chan string, 1),
		release: make(chan struct{}),
	}
}

func (a *task6TranscriptBarrierAdapter) Name() string { return "openai" }

func (a *task6TranscriptBarrierAdapter) Complete(ctx context.Context, request llm.Request) (llm.Response, error) {
	a.enteredOnce.Do(func() { a.entered <- request.SessionID })
	select {
	case <-a.release:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	return llm.Response{Provider: "openai", Model: request.Model, Message: llm.Assistant("done")}, nil
}

func (a *task6TranscriptBarrierAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *task6TranscriptBarrierAdapter) releaseRun() {
	a.releaseOnce.Do(func() { close(a.release) })
}

type task6FrozenDescriptorAdapter struct {
	entered     chan llm.Request
	release     chan struct{}
	releaseOnce sync.Once
}

func newTask6FrozenDescriptorAdapter() *task6FrozenDescriptorAdapter {
	return &task6FrozenDescriptorAdapter{
		entered: make(chan llm.Request, 1),
		release: make(chan struct{}),
	}
}

func (a *task6FrozenDescriptorAdapter) Name() string { return "openai" }

func (a *task6FrozenDescriptorAdapter) Complete(ctx context.Context, request llm.Request) (llm.Response, error) {
	select {
	case a.entered <- request:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	select {
	case <-a.release:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	return llm.Response{Provider: "openai", Model: request.Model, Message: llm.Assistant("done")}, nil
}

func (a *task6FrozenDescriptorAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *task6FrozenDescriptorAdapter) releaseRun() {
	a.releaseOnce.Do(func() { close(a.release) })
}

func task6RequestText(request llm.Request) string {
	var text strings.Builder
	for _, message := range request.Messages {
		text.WriteString(message.Text())
		text.WriteByte('\n')
	}
	return text.String()
}

func task6CommunicateOutputSchema(t *testing.T, request llm.Request) any {
	t.Helper()
	for _, definition := range request.Tools {
		if definition.Name != "communicate" {
			continue
		}
		properties, ok := definition.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("communicate properties = %T, want map[string]any", definition.Parameters["properties"])
		}
		return properties["output"]
	}
	t.Fatal("provider request omitted communicate tool")
	return nil
}
