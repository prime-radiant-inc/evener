package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	taskpkg "primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

type currentWorkEventRecorder struct {
	mu     sync.Mutex
	events []events.SessionEvent
}

func (r *currentWorkEventRecorder) record(event events.SessionEvent) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *currentWorkEventRecorder) snapshot() []events.SessionEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]events.SessionEvent(nil), r.events...)
}

func queuedSessionStart(t *testing.T, session *Session) events.SessionStartData {
	t.Helper()
	event := <-session.Events()
	if event.Kind != events.EventSessionStart {
		t.Fatalf("first queued event = %s, want %s", event.Kind, events.EventSessionStart)
	}
	data, ok := event.Data.(events.SessionStartData)
	if !ok {
		t.Fatalf("SessionStart payload type = %T", event.Data)
	}
	return data
}

func childCurrentWorkEvents(all []events.SessionEvent, childID string) []events.SessionEvent {
	var relevant []events.SessionEvent
	for _, event := range all {
		if event.SessionID == childID && (event.Kind == events.EventSessionStart || event.Kind == events.EventTaskUpdated) {
			relevant = append(relevant, event)
		}
	}
	return relevant
}

func TestRootSessionStartSeedsPostTemplateCurrentTask(t *testing.T) {
	dir := t.TempDir()
	session := newSession(t,
		withDir(dir),
		withConfig(coordinatorWorkflowSessionConfig(t, SessionConfig{
			AgentName:        "coordinator",
			NonInteractive:   true,
			StateDir:         dir,
			NoProjectPrompts: true,
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
			},
		})),
	)

	start := queuedSessionStart(t, session)
	if start.CurrentWork == nil || start.CurrentWork.Tasks == nil || start.CurrentWork.Tasks.Current == nil {
		t.Fatalf("SessionStart CurrentWork = %+v, want populated current task", start.CurrentWork)
	}
	if got := start.CurrentWork.Tasks.Current.Description; got != "Plan" {
		t.Fatalf("SessionStart current task = %q, want %q", got, "Plan")
	}
	if start.TaskStoreOwnerSessionID != session.id {
		t.Fatalf("SessionStart owner = %q, want root %q", start.TaskStoreOwnerSessionID, session.id)
	}
	if start.TaskPublicationRevision == 0 {
		t.Fatal("SessionStart publication revision = 0, want new-producer revision")
	}
	if start.TaskPublicationEpoch == 0 {
		t.Fatal("SessionStart publication epoch = 0, want TaskStore incarnation")
	}
}

func TestFreshChildStartThenTemplatePopulationEmitsTaskCorrection(t *testing.T) {
	dir := t.TempDir()
	root := newSession(t, withDir(dir), withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		StateDir:         dir,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			sandboxProber:       bwrapCapableProber(dir),
		},
	}))
	const agentType = "current-work-fresh-child"
	root.pluginAgents[agentType] = plugin.Agent{
		Name:       agentType,
		AllTools:   true,
		PluginName: "test",
		Tasks: []taskpkg.TaskTemplate{{
			Title:  "Inspect child current work",
			Prompt: "Inspect it.",
		}},
	}
	var recorder currentWorkEventRecorder
	root.SetDescendantEventFunc(recorder.record)

	prepared, err := root.prepareSubagentRun(context.Background(), "inspect", "", "", 1, agentType, "", nil, nil)
	if err != nil {
		t.Fatalf("prepareSubagentRun: %v", err)
	}
	defer releasePreparedTreeSlot(prepared)
	defer prepared.sub.sess.Close()

	relevant := childCurrentWorkEvents(recorder.snapshot(), prepared.sub.id)
	if len(relevant) < 2 {
		t.Fatalf("child current-work events = %#v, want SessionStart then TaskUpdated", relevant)
	}
	if relevant[0].Kind != events.EventSessionStart || relevant[1].Kind != events.EventTaskUpdated {
		t.Fatalf("child current-work order = [%s, %s], want start then update", relevant[0].Kind, relevant[1].Kind)
	}
	start := relevant[0].Data.(events.SessionStartData)
	if start.CurrentWork == nil || start.CurrentWork.Tasks == nil || start.CurrentWork.Tasks.Total != 0 {
		t.Fatalf("pre-population child start = %+v, want authoritative empty task state", start.CurrentWork)
	}
	update := relevant[1].Data.(events.TaskUpdatedData)
	if update.Current == nil || update.Current.Description != "Inspect child current work" {
		t.Fatalf("post-population TaskUpdated = %+v", update)
	}
	if update.TaskStoreOwnerSessionID != prepared.sub.id {
		t.Fatalf("post-population owner = %q, want child %q", update.TaskStoreOwnerSessionID, prepared.sub.id)
	}
	if start.TaskPublicationRevision == 0 || update.TaskPublicationRevision <= start.TaskPublicationRevision {
		t.Fatalf("child start/update publication revisions = %d/%d, want increasing nonzero", start.TaskPublicationRevision, update.TaskPublicationRevision)
	}
	if update.TaskPublicationEpoch != start.TaskPublicationEpoch {
		t.Fatalf("child start/update publication epochs = %d/%d, want same store incarnation", start.TaskPublicationEpoch, update.TaskPublicationEpoch)
	}
}

func TestSharedChildStartAndTaskUpdateNameRootOwner(t *testing.T) {
	dir := t.TempDir()
	root := newSession(t, withDir(dir), withConfig(SessionConfig{
		MaxSubagentDepth:       1,
		ShareTasksWithChildren: true,
		NoProjectPrompts:       true,
		StateDir:               dir,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			sandboxProber:       bwrapCapableProber(dir),
		},
	}))
	if err := root.getOrCreateTaskStore().PopulateFromTemplates([]taskpkg.TaskTemplate{{
		Title: "Shared root task", Prompt: "Coordinate it.",
	}}, nil); err != nil {
		t.Fatal(err)
	}
	var recorder currentWorkEventRecorder
	root.SetDescendantEventFunc(recorder.record)

	prepared, err := root.prepareSubagentRun(context.Background(), "inspect shared work", "", "", 1, "", "", nil, nil)
	if err != nil {
		t.Fatalf("prepareSubagentRun: %v", err)
	}
	defer releasePreparedTreeSlot(prepared)
	defer prepared.sub.sess.Close()
	child := prepared.sub.sess

	relevant := childCurrentWorkEvents(recorder.snapshot(), child.id)
	if len(relevant) == 0 || relevant[0].Kind != events.EventSessionStart {
		t.Fatalf("shared child events = %#v, want SessionStart", relevant)
	}
	start := relevant[0].Data.(events.SessionStartData)
	if start.TaskStoreOwnerSessionID != root.id {
		t.Fatalf("shared child start owner = %q, want root %q", start.TaskStoreOwnerSessionID, root.id)
	}
	if start.TaskPublicationRevision == 0 {
		t.Fatal("shared child start publication revision = 0")
	}
	if start.TaskPublicationEpoch == 0 {
		t.Fatal("shared child start publication epoch = 0")
	}
	if start.CurrentWork == nil || start.CurrentWork.Tasks == nil || start.CurrentWork.Tasks.Current == nil || start.CurrentWork.Tasks.Current.Description != "Shared root task" {
		t.Fatalf("shared child start task state = %+v", start.CurrentWork)
	}

	beforeMutation := len(recorder.snapshot())
	arguments, _ := json.Marshal(map[string]any{
		"add": []map[string]any{{
			"type": "implement", "description": "Shared follow-up", "prompt": "Do it.",
		}},
	})
	result := child.reg.ExecuteCall(context.Background(), child.env, llm.ToolCallData{
		ID: "shared-task-mutation", Name: "task_list", Arguments: arguments,
	})
	if result.IsError {
		t.Fatalf("shared child task append: %s", result.Output)
	}
	var update *events.TaskUpdatedData
	for _, event := range recorder.snapshot()[beforeMutation:] {
		if event.SessionID != child.id || event.Kind != events.EventTaskUpdated {
			continue
		}
		data := event.Data.(events.TaskUpdatedData)
		update = &data
		break
	}
	if update == nil {
		t.Fatal("shared child mutation emitted no TaskUpdated")
	}
	if update.TaskStoreOwnerSessionID != root.id || update.Total != 2 || update.Current == nil || update.Current.Description != "Shared root task" {
		t.Fatalf("shared child TaskUpdated = %+v, want owner root and shared summary", *update)
	}
	if update.TaskPublicationRevision <= start.TaskPublicationRevision {
		t.Fatalf("shared child start/update publication revisions = %d/%d, want increasing", start.TaskPublicationRevision, update.TaskPublicationRevision)
	}
	if update.TaskPublicationEpoch != start.TaskPublicationEpoch {
		t.Fatalf("shared child start/update publication epochs = %d/%d, want shared store incarnation", start.TaskPublicationEpoch, update.TaskPublicationEpoch)
	}
}

func TestSessionStartGoalSeedUsesStructuredMetaAndExplicitClear(t *testing.T) {
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	meta := schema.SessionMeta{
		ID:        identifier.MustNewSessionID(),
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    (SessionConfig{NoProjectPrompts: true}).toSnapshot(),
		Goal: &schema.GoalSnapshot{
			Objective:  "Ship structured focus state",
			Status:     "active",
			Iterations: 3,
		},
	}
	restored, err := RestoreSessionFromMetaWithConfig(
		client,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		meta,
		RestoreSessionConfig{testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true}},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer restored.Close()
	restoredStart := queuedSessionStart(t, restored)
	if restoredStart.CurrentWork == nil || restoredStart.CurrentWork.Goal == nil {
		t.Fatalf("restored CurrentWork = %+v, want structured goal", restoredStart.CurrentWork)
	}
	goal := restoredStart.CurrentWork.Goal
	if goal.Objective != meta.Goal.Objective || goal.Status != meta.Goal.Status || goal.Iterations != meta.Goal.Iterations {
		t.Fatalf("goal seed = %+v, want %+v", goal, meta.Goal)
	}

	fresh := newTestSession(t)
	freshStart := queuedSessionStart(t, fresh)
	if freshStart.CurrentWork == nil || freshStart.CurrentWork.Goal != nil {
		t.Fatalf("fresh CurrentWork = %+v, want present seed with explicit nil goal", freshStart.CurrentWork)
	}
}
