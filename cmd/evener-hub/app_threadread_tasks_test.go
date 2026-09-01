package hub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/server"
)

type taskAggregateScriptedAdapter struct{}

func (taskAggregateScriptedAdapter) Name() string { return "openai" }

func (taskAggregateScriptedAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{Message: llm.Assistant("done")}, nil
}

func (taskAggregateScriptedAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func TestPastThreadReadProjectsPersistedTaskAggregate(t *testing.T) {
	cfg, sessionID, stateDir := seedPastSessionWithTasks(t, []task.TaskInput{
		{Type: task.TaskTypeImplement, Description: "one", Prompt: "one"},
		{Type: task.TaskTypeVerify, Description: "two", Prompt: "two"},
	})
	store := task.NewTaskStore(stateDir, sessionID)
	if err := store.Load(); err != nil {
		t.Fatalf("load persisted tasks: %v", err)
	}
	if err := store.Update([]task.TaskUpdate{{ID: 1, Status: task.TaskDone}}); err != nil {
		t.Fatalf("complete persisted task: %v", err)
	}

	thread, ok, err := pastThreadForRead(context.Background(), cfg, appwire.ThreadReadParams{Ref: "local:" + sessionID})
	if err != nil || !ok {
		t.Fatalf("pastThreadForRead: thread=%+v found=%v err=%v", thread, ok, err)
	}
	want := &appwire.TaskAggregate{Total: 2, Done: 1}
	if thread.Evener.Tasks == nil || *thread.Evener.Tasks != *want {
		t.Fatalf("persisted task aggregate=%+v, want %+v", thread.Evener.Tasks, want)
	}
}

func TestPastThreadReadProjectsFirstInProgressTask(t *testing.T) {
	cfg, sessionID, stateDir := seedPastSessionWithTasks(t, []task.TaskInput{
		{Type: task.TaskTypeImplement, Description: "first current", Prompt: "one"},
		{Type: task.TaskTypeVerify, Description: "second current", Prompt: "two"},
	})
	store := task.NewTaskStore(stateDir, sessionID)
	if err := store.Load(); err != nil {
		t.Fatalf("load persisted tasks: %v", err)
	}
	items := store.View()
	for i := range items {
		items[i].Status = task.TaskInProgress
	}
	data, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal persisted tasks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "tasks", sessionID+".json"), data, 0o644); err != nil {
		t.Fatalf("write persisted tasks: %v", err)
	}

	thread, ok, err := pastThreadForRead(context.Background(), cfg, appwire.ThreadReadParams{Ref: "local:" + sessionID})
	if err != nil || !ok {
		t.Fatalf("pastThreadForRead: found=%v err=%v", ok, err)
	}
	if thread.Evener.Tasks == nil || thread.Evener.Tasks.Current == nil || thread.Evener.Tasks.Current.ID != 1 || thread.Evener.Tasks.Current.Description != "first current" {
		t.Fatalf("persisted current task = %+v, want first in-progress task", thread.Evener.Tasks)
	}
}

func TestPastThreadReadProjectsPersistedGoal(t *testing.T) {
	cfg, sessionID, _ := seedPastSessionWithTasks(t, nil)
	entry, ok := cfg.Past.Find(sessionID)
	if !ok {
		t.Fatal("past entry not found")
	}
	entry.Meta.Goal = &schema.GoalSnapshot{Objective: "past goal objective", Status: "active", Iterations: 2}

	thread, err := pastEntryThread(context.Background(), cfg, entry, false)
	if err != nil {
		t.Fatalf("pastEntryThread: %v", err)
	}
	if thread.Evener.Goal == nil || thread.Evener.Goal.Objective != "past goal objective" || thread.Evener.Goal.Status != "active" || thread.Evener.Goal.Iterations != 2 {
		t.Fatalf("persisted goal = %+v, want objective/status/iterations", thread.Evener.Goal)
	}
}

func TestPastThreadReadTaskAggregatePreservesAbsentAndZero(t *testing.T) {
	t.Run("missing task file is unknown", func(t *testing.T) {
		cfg, sessionID, _ := seedPastSessionWithTasks(t, nil)
		thread, ok, err := pastThreadForRead(context.Background(), cfg, appwire.ThreadReadParams{Ref: "local:" + sessionID})
		if err != nil || !ok {
			t.Fatalf("pastThreadForRead: found=%v err=%v", ok, err)
		}
		if thread.Evener.Tasks != nil {
			t.Fatalf("missing task aggregate=%+v, want nil", thread.Evener.Tasks)
		}
	})

	t.Run("present empty task file is zero", func(t *testing.T) {
		cfg, sessionID, _ := seedPastSessionWithTasks(t, []task.TaskInput{})
		thread, ok, err := pastThreadForRead(context.Background(), cfg, appwire.ThreadReadParams{Ref: "local:" + sessionID})
		if err != nil || !ok {
			t.Fatalf("pastThreadForRead: found=%v err=%v", ok, err)
		}
		if thread.Evener.Tasks == nil || thread.Evener.Tasks.Total != 0 || thread.Evener.Tasks.Done != 0 {
			t.Fatalf("empty-file task aggregate=%+v, want present zero", thread.Evener.Tasks)
		}
	})
}

func TestTaskAggregateMalformedPersistedStoreMatchesLiveAndColdUnknown(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-live-0000000000")
	workDir := t.TempDir()
	client := llm.NewClient()
	client.Register(taskAggregateScriptedAdapter{})
	profile := provider.NewOpenAIProfile("gpt-5.2")
	environment := execenv.NewLocalExecutionEnvironment(workDir)
	sess, err := agent.NewSession(client, profile, environment, agent.SessionConfig{
		StateDir:         stateDir,
		NoProjectPrompts: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	tasksPath := filepath.Join(stateDir, "tasks", sess.ID()+".json")
	meta := sess.Meta()
	sess.Close()
	if err := os.MkdirAll(filepath.Dir(tasksPath), 0o755); err != nil {
		t.Fatalf("mkdir task store: %v", err)
	}
	if err := os.WriteFile(tasksPath, []byte("{malformed task JSON"), 0o644); err != nil {
		t.Fatalf("write malformed task store: %v", err)
	}
	sess, err = agent.RestoreSessionFromMeta(client, profile, environment, meta, stateDir)
	if err != nil {
		t.Fatalf("restore session with malformed task store: %v", err)
	}
	if _, err := sess.TasksWithError(); err == nil {
		t.Fatal("malformed task store load unexpectedly succeeded")
	}

	// This is the same live producer shape wired by cmd/evener/serve.go. A load
	// failure is unavailable aggregate data, while a valid empty View remains a
	// present zero aggregate.
	live := server.NewServer(server.ServerConfig{})
	live.SetAppIdentity("local", sess.ID())
	live.SetStatus(server.StatusInfo{SessionID: sess.ID(), State: "idle", Model: sess.Profile().Model(), Profile: sess.Profile().ID(), WorkingDir: workDir})
	live.SetThreadEnvelopeSource(sessionTaskEnvelopeSource{sess: sess})
	live.RefreshThreadEnvelope()
	conn := live.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
	response := conn.HandleMessage(context.Background(), appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:" + sess.ID()}))
	liveRead, ok := response.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("live thread/read result=%T", response.Response.Result)
	}

	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatalf("rebuild past index: %v", err)
	}
	coldRead, found, err := pastThreadForRead(context.Background(), hubcore.WebConfig{Past: past}, appwire.ThreadReadParams{Ref: "local:" + sess.ID()})
	if err != nil || !found {
		t.Fatalf("cold thread/read: found=%v err=%v", found, err)
	}
	if liveRead.Thread.Evener.Tasks != nil {
		t.Fatalf("live malformed task aggregate=%+v, want unknown", liveRead.Thread.Evener.Tasks)
	}
	if coldRead.Evener.Tasks != nil {
		t.Fatalf("cold malformed task aggregate=%+v, want unknown", coldRead.Evener.Tasks)
	}
}

// sessionTaskEnvelopeSource mirrors cmd/evener/serve.go's live envelope source for
// the one facet this test asserts on. Everything else reports the zero value a
// daemon with nothing to say reports.
type sessionTaskEnvelopeSource struct{ sess *agent.Session }

func (s sessionTaskEnvelopeSource) ContextPressure() float64 { return 0 }
func (s sessionTaskEnvelopeSource) ContextMetrics() server.ContextMetrics {
	return server.ContextMetrics{}
}
func (s sessionTaskEnvelopeSource) DetailedStatus() server.DetailedStatus {
	return server.DetailedStatus{}
}
func (s sessionTaskEnvelopeSource) AskPending() bool                        { return false }
func (s sessionTaskEnvelopeSource) SessionMeta() schema.SessionMeta         { return schema.SessionMeta{} }
func (s sessionTaskEnvelopeSource) FailedToolCalls() (int, bool)            { return 0, false }
func (s sessionTaskEnvelopeSource) ReasoningInfo() (string, []string, bool) { return "", nil, false }
func (s sessionTaskEnvelopeSource) VisionModel() string                     { return "" }
func (s sessionTaskEnvelopeSource) WorkMetrics() (int64, *appwire.EvenerUsage, int64) {
	return 0, nil, 0
}
func (s sessionTaskEnvelopeSource) PendingEscalations() []appwire.SandboxEscalationRequested {
	return nil
}
func (s sessionTaskEnvelopeSource) ClientMutationProjection() (appwire.QueueState, []appwire.PendingMutation) {
	return appwire.QueueState{}, nil
}

// TaskAggregate is the shape wired by cmd/evener/serve.go: a load failure is
// unavailable task state (nil), never an authoritative empty list.
func (s sessionTaskEnvelopeSource) TaskAggregate() *appwire.TaskAggregate {
	tasks, err := s.sess.TasksWithError()
	if err != nil {
		return nil
	}
	summary := task.Summarize(tasks)
	aggregate := &appwire.TaskAggregate{Total: summary.Total, Done: summary.Done}
	if summary.Current != nil {
		aggregate.Current = &appwire.TaskSummary{ID: summary.Current.ID, Description: summary.Current.Description}
	}
	return aggregate
}
