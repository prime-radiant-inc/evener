package main

import (
	"testing"

	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/appwire"
)

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
