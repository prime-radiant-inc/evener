package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/server"
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

	thread, ok, err := pastThreadForRead(cfg, appwire.ThreadReadParams{Ref: "local:" + sessionID})
	if err != nil || !ok {
		t.Fatalf("pastThreadForRead: thread=%+v found=%v err=%v", thread, ok, err)
	}
	want := &appwire.TaskAggregate{Total: 2, Done: 1}
	if thread.Serf.Tasks == nil || *thread.Serf.Tasks != *want {
		t.Fatalf("persisted task aggregate=%+v, want %+v", thread.Serf.Tasks, want)
	}
}

func TestPastThreadReadTaskAggregatePreservesAbsentAndZero(t *testing.T) {
	t.Run("missing task file is unknown", func(t *testing.T) {
		cfg, sessionID, _ := seedPastSessionWithTasks(t, nil)
		thread, ok, err := pastThreadForRead(cfg, appwire.ThreadReadParams{Ref: "local:" + sessionID})
		if err != nil || !ok {
			t.Fatalf("pastThreadForRead: found=%v err=%v", ok, err)
		}
		if thread.Serf.Tasks != nil {
			t.Fatalf("missing task aggregate=%+v, want nil", thread.Serf.Tasks)
		}
	})

	t.Run("present empty task file is zero", func(t *testing.T) {
		cfg, sessionID, _ := seedPastSessionWithTasks(t, []task.TaskInput{})
		thread, ok, err := pastThreadForRead(cfg, appwire.ThreadReadParams{Ref: "local:" + sessionID})
		if err != nil || !ok {
			t.Fatalf("pastThreadForRead: found=%v err=%v", ok, err)
		}
		if thread.Serf.Tasks == nil || thread.Serf.Tasks.Total != 0 || thread.Serf.Tasks.Done != 0 {
			t.Fatalf("empty-file task aggregate=%+v, want present zero", thread.Serf.Tasks)
		}
	})
}

func TestTaskAggregateMalformedPersistedStoreMatchesLiveAndColdUnknown(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-live-0000000000")
	workDir := t.TempDir()
	client := llm.NewClient()
	client.Register(taskAggregateScriptedAdapter{})
	sess, err := agent.NewSession(client, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), agent.SessionConfig{
		StateDir:         stateDir,
		NoProjectPrompts: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	tasksPath := filepath.Join(stateDir, "tasks", sess.ID()+".json")
	if err := os.MkdirAll(filepath.Dir(tasksPath), 0o755); err != nil {
		t.Fatalf("mkdir task store: %v", err)
	}
	if err := os.WriteFile(tasksPath, []byte("{malformed task JSON"), 0o644); err != nil {
		t.Fatalf("write malformed task store: %v", err)
	}
	if _, err := sess.TasksWithError(); err == nil {
		t.Fatal("malformed task store load unexpectedly succeeded")
	}

	// This is the same live producer shape wired by cmd/serf/serve.go. A load
	// failure is unavailable aggregate data, while a valid empty View remains a
	// present zero aggregate.
	live := server.NewServer(server.ServerConfig{})
	live.SetAppIdentity("local", sess.ID())
	live.SetStatus(server.StatusInfo{SessionID: sess.ID(), State: "idle", Model: sess.Profile().Model(), Profile: sess.Profile().ID(), WorkingDir: workDir})
	live.SetTaskAggregateFunc(func() *appwire.TaskAggregate {
		tasks, err := sess.TasksWithError()
		if err != nil {
			return nil
		}
		done := 0
		for _, item := range tasks {
			if item.Status == task.TaskDone {
				done++
			}
		}
		return &appwire.TaskAggregate{Total: len(tasks), Done: done}
	})
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
	coldRead, found, err := pastThreadForRead(hubcore.WebConfig{Past: past}, appwire.ThreadReadParams{Ref: "local:" + sess.ID()})
	if err != nil || !found {
		t.Fatalf("cold thread/read: found=%v err=%v", found, err)
	}
	if liveRead.Thread.Serf.Tasks != nil {
		t.Fatalf("live malformed task aggregate=%+v, want unknown", liveRead.Thread.Serf.Tasks)
	}
	if coldRead.Serf.Tasks != nil {
		t.Fatalf("cold malformed task aggregate=%+v, want unknown", coldRead.Serf.Tasks)
	}
}
