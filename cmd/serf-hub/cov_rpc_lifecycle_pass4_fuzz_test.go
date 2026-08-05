package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/rendezvous"
)

type lifecycleFuzzSource struct {
	*scriptedAppSource
	subscribeErr  error
	notifications chan appwire.Notification
	compactCalls  int
}

func (s *lifecycleFuzzSource) StartThread(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	return appwire.ThreadStartResponse{Thread: s.thread}, nil
}

func (s *lifecycleFuzzSource) ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	return appwire.ThreadResumeResponse{Thread: s.thread}, nil
}

func (s *lifecycleFuzzSource) ForkThread(context.Context, appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	child := s.thread
	child.ID, child.SessionID, child.Serf.Ref = "child", "child", s.id+":child"
	return appwire.ThreadForkResponse{Thread: child}, nil
}

func (s *lifecycleFuzzSource) SubscribeThread(context.Context, appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	if s.subscribeErr != nil {
		return nil, s.subscribeErr
	}
	if s.notifications == nil {
		s.notifications = make(chan appwire.Notification)
		close(s.notifications)
	}
	return s.notifications, nil
}

func (s *lifecycleFuzzSource) CompactThread(context.Context, appwire.ThreadCompactStartParams) error {
	s.compactCalls++
	if s.compactCalls == 1 {
		return appwire.SessionUnavailable("gone")
	}
	return nil
}

func fuzzLifecycleDispatch(ctx context.Context, t *testing.T, server *appserver.Server, method string, params any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return server.Router().Dispatch(ctx, appwire.Request{ID: appwire.NewIntID(1), Method: method, Params: raw})
}

// FuzzRPCLifecyclePass4 drives stateful relay and thread lifecycle paths using
// only scripted sources and a process-launch boundary fake.
func FuzzRPCLifecyclePass4(f *testing.F) {
	f.Add(uint8(0))
	f.Add(uint8(1))
	f.Add(uint8(2))
	f.Fuzz(func(t *testing.T, variant uint8) {
		ctx := context.Background()
		caps := appwire.ThreadCapabilities{Send: true, Compact: true, ForkFromTurn: true}
		thread := appwire.Thread{
			ID: "thread", SessionID: "thread", Source: "remote", Name: "root",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Serf:   appwire.SerfThread{Ref: "remote:thread", Capabilities: caps},
			Turns:  []appwire.Turn{{ID: "turn_1"}, {ID: "turn_2"}},
		}
		base := &scriptedAppSource{id: "remote", thread: thread}
		source := &lifecycleFuzzSource{scriptedAppSource: base}
		if variant%3 == 1 {
			source.subscribeErr = errors.New("subscribe failed")
		} else {
			source.notifications = make(chan appwire.Notification, 1)
			source.notifications <- appwire.Notification{Method: appwire.NotifyThreadStatusChanged}
			close(source.notifications)
		}
		registry := appsource.NewRegistry()
		registry.Add(source)
		server := newHubAppServer(hubcore.WebConfig{}, registry)

		_, _ = fuzzLifecycleDispatch(ctx, t, server, appwire.MethodThreadRead,
			appwire.ThreadReadParams{Ref: "remote:thread", IncludeTurns: true, Subscribe: true, ReplaceSubscription: variant&1 != 0})
		_, _ = fuzzLifecycleDispatch(ctx, t, server, appwire.MethodThreadRead,
			appwire.ThreadReadParams{Ref: "remote:thread", Subscribe: true})
		_, _ = fuzzLifecycleDispatch(ctx, t, server, appwire.MethodThreadStart,
			appwire.ThreadStartParams{Harness: "remote"})
		_, _ = fuzzLifecycleDispatch(ctx, t, server, appwire.MethodThreadResume,
			appwire.ThreadResumeParams{Ref: "remote:thread"})
		_, _ = fuzzLifecycleDispatch(ctx, t, server, appwire.MethodThreadFork,
			appwire.ThreadForkParams{Ref: "remote:thread", SourceTurnID: "turn_1", EditedInput: "edit"})
		_, _ = fuzzLifecycleDispatch(ctx, t, server, appwire.MethodSerfThreadTranscriptsList,
			appwire.ThreadTranscriptListParams{Ref: "remote:thread"})
		_, _ = hubThreadList(ctx, hubcore.WebConfig{}, registry, appwire.ThreadListParams{IncludeSubagents: true})

		localThread := thread
		localThread.Source, localThread.Serf.Ref = "local", "local:thread"
		localBase := &scriptedAppSource{id: "local", thread: localThread}
		local := &lifecycleFuzzSource{scriptedAppSource: localBase}
		localRegistry := appsource.NewRegistry()
		localRegistry.Add(local)
		spawner := &fakeRPCModelContractSpawner{
			fakeRPCSpawner: fakeRPCSpawner{
				spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
					return rendezvous.Entry{ThreadID: "thread", SessionID: "thread", PID: 7}, nil
				},
				resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
					return rendezvous.Entry{ThreadID: "thread", SessionID: "thread", PID: 7}, nil
				},
			},
			contract: appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5"}}},
		}
		cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Spawner: spawner}
		_, _ = hubThreadStart(ctx, cfg, localRegistry, appwire.ThreadStartParams{Model: "openai/gpt-5"})
		_, _ = hubThreadResume(ctx, cfg, localRegistry, appwire.ThreadResumeParams{Ref: "local:thread"})
		_ = compactThreadWithResume(ctx, cfg, localRegistry, appwire.ThreadCompactStartParams{Ref: "local:thread"})
	})
}
