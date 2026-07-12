package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/codexlaunch"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/rendezvous"
)

type pass6LifecycleSource struct {
	*scriptedAppSource
	listErr, readErr, startErr, resumeErr, forkErr, turnErr error
	listed                                                  []appwire.Thread
}

func (s *pass6LifecycleSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	return appwire.ThreadListResponse{Data: s.listed}, s.listErr
}
func (s *pass6LifecycleSource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{Thread: s.thread}, s.readErr
}
func (s *pass6LifecycleSource) StartThread(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	return appwire.ThreadStartResponse{Thread: s.thread}, s.startErr
}
func (s *pass6LifecycleSource) ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	return appwire.ThreadResumeResponse{Thread: s.thread}, s.resumeErr
}
func (s *pass6LifecycleSource) ForkThread(context.Context, appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	return appwire.ThreadForkResponse{Thread: s.thread}, s.forkErr
}
func (s *pass6LifecycleSource) StartTurn(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_1"}}, s.turnErr
}

func pass6Past(t *testing.T) *hubcore.PastIndex {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "projects", "repo")
	if err := os.MkdirAll(filepath.Join(state, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(state, schema.SessionMeta{
		ID: "past", ProfileID: "openai", Model: "gpt-5", Name: "past name",
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/past/cwd"},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	return idx
}

func pass6ManagedLauncher(source appsource.Source) *codexlaunch.CodexLauncher {
	l := codexlaunch.NewCodexLauncher([]codexlaunch.CodexLaunchConfig{{ID: "managed"}, {ID: "codex"}})
	for _, id := range []string{"managed", "codex"} {
		l.Sources[id] = source
		l.Running[id] = &codexlaunch.LaunchedCodex{Cmd: &exec.Cmd{}, Exited: make(chan struct{})}
	}
	return l
}

// FuzzThreadLifecycleListPass6 drives lifecycle and list branches directly;
// all external process and source boundaries are deterministic fakes.
func FuzzThreadLifecycleListPass6(f *testing.F) {
	for i := uint8(0); i < 8; i++ {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, variant uint8) {
		ctx := context.Background()
		past := pass6Past(t)
		thread := appwire.Thread{ID: "past", SessionID: "past", Source: "local", Preview: "past", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}, Serf: appwire.SerfThread{Ref: "local:past"}}
		source := &pass6LifecycleSource{scriptedAppSource: &scriptedAppSource{id: "local", thread: thread}, listed: []appwire.Thread{thread}}
		registry := appsource.NewRegistry()
		registry.Add(source)

		okSpawner := &fakeRPCModelContractSpawner{fakeRPCSpawner: fakeRPCSpawner{
			spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
				return rendezvous.Entry{ThreadID: "past", SessionID: "past", PID: 7}, nil
			},
			resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
				return rendezvous.Entry{ThreadID: "past", SessionID: "past", PID: 7}, nil
			},
		}, contract: appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5"}}}}
		cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: past, Spawner: okSpawner}

		_ = launchSourceID(appwire.ThreadStartParams{Harness: "serf"})
		_, _ = hubThreadStart(ctx, hubcore.WebConfig{}, registry, appwire.ThreadStartParams{})
		_, _ = hubThreadStart(ctx, hubcore.WebConfig{Spawner: okSpawner, HubStateRoot: t.TempDir()}, registry, appwire.ThreadStartParams{})
		_, _ = hubThreadStart(ctx, cfg, registry, appwire.ThreadStartParams{Input: []appwire.InputItem{{Type: "bogus"}}})
		badCWD := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(badCWD, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = hubThreadStart(ctx, cfg, registry, appwire.ThreadStartParams{Model: "openai/gpt-5", CWD: badCWD})
		_, _ = hubThreadStart(ctx, cfg, registry, appwire.ThreadStartParams{Model: "broken/model/extra"})
		_, _ = hubThreadStart(ctx, cfg, registry, appwire.ThreadStartParams{ModelProvider: "openai", Model: "gpt-5", Profile: "agent", ReasoningEffort: "high", NonInteractive: func() *bool { v := true; return &v }(), CWD: t.TempDir(), Input: []appwire.InputItem{{Type: "text", Text: "go"}}})
		source.readErr = errors.New("read")
		_, _ = hubThreadStart(ctx, cfg, registry, appwire.ThreadStartParams{Model: "openai/gpt-5"})
		source.readErr = nil
		source.turnErr = errors.New("turn")
		_, _ = hubThreadStart(ctx, cfg, registry, appwire.ThreadStartParams{Model: "openai/gpt-5", Input: []appwire.InputItem{{Type: "text", Text: "go"}}})
		source.turnErr = nil
		failSpawner := &fakeRPCModelContractSpawner{fakeRPCSpawner: fakeRPCSpawner{
			spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
				return rendezvous.Entry{}, errors.New("spawn")
			},
			resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
				return rendezvous.Entry{}, errors.New("resume")
			},
		}, contract: appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5"}}}}
		_, _ = hubThreadStart(ctx, hubcore.WebConfig{Spawner: failSpawner, HubStateRoot: t.TempDir()}, registry, appwire.ThreadStartParams{Model: "openai/gpt-5"})
		emptyReg := appsource.NewRegistry()
		_, _ = hubThreadStart(ctx, cfg, emptyReg, appwire.ThreadStartParams{Model: "openai/gpt-5"})

		remote := &pass6LifecycleSource{scriptedAppSource: &scriptedAppSource{id: "remote", thread: appwire.Thread{ID: "r", Source: "remote", Serf: appwire.SerfThread{Ref: "remote:r"}}}}
		remoteReg := appsource.NewRegistry()
		remoteReg.Add(remote)
		_, _ = hubThreadStart(ctx, hubcore.WebConfig{}, remoteReg, appwire.ThreadStartParams{Harness: "remote"})
		remote.startErr = errors.New("start")
		_, _ = hubThreadStart(ctx, hubcore.WebConfig{}, remoteReg, appwire.ThreadStartParams{Harness: "remote"})
		remote.startErr = nil
		launcher := pass6ManagedLauncher(remote)
		managedCfg := hubcore.WebConfig{CodexLauncher: launcher, CodexLaunches: []codexlaunch.CodexLaunchConfig{{ID: "managed"}, {}}}
		_, _ = hubThreadStart(ctx, managedCfg, remoteReg, appwire.ThreadStartParams{Harness: "managed"})
		_, _ = hubThreadStart(ctx, managedCfg, remoteReg, appwire.ThreadStartParams{Harness: "missing"})

		_, _ = hubThreadResume(ctx, hubcore.WebConfig{}, registry, appwire.ThreadResumeParams{})
		_, _ = hubThreadResume(ctx, cfg, registry, appwire.ThreadResumeParams{})
		_, _ = hubThreadResume(ctx, cfg, registry, appwire.ThreadResumeParams{Ref: "bad"})
		_, _ = hubThreadResume(ctx, cfg, registry, appwire.ThreadResumeParams{Session: "past"})
		_, _ = hubThreadResume(ctx, hubcore.WebConfig{Spawner: failSpawner}, registry, appwire.ThreadResumeParams{Session: "past"})
		_, _ = hubThreadResume(ctx, cfg, emptyReg, appwire.ThreadResumeParams{Session: "past"})
		source.readErr = errors.New("resume read")
		_, _ = hubThreadResume(ctx, cfg, registry, appwire.ThreadResumeParams{Session: "past"})
		source.readErr = nil
		_, _ = hubThreadResume(ctx, managedCfg, remoteReg, appwire.ThreadResumeParams{Ref: "managed:r"})
		_, _ = hubThreadResume(ctx, hubcore.WebConfig{}, remoteReg, appwire.ThreadResumeParams{Ref: "remote:r"})
		_, _ = resumeRequestForConfig(hubcore.WebConfig{}, "none")
		_, _ = resumeRequestForConfig(cfg, "past")

		for _, raw := range []string{"", "turn_0", "turn_x", "turn_2"} {
			_, _ = parseSourceTurnID(raw)
		}
		_ = threadForkRequiresTurnCapability(appwire.ThreadForkParams{})
		_ = threadForkRequiresTurnCapability(appwire.ThreadForkParams{Label: "x"})
		_, _ = hubThreadFork(ctx, hubcore.WebConfig{}, remoteReg, appwire.ThreadForkParams{Ref: "remote:r"})
		_, _ = hubThreadFork(ctx, hubcore.WebConfig{}, remoteReg, appwire.ThreadForkParams{Ref: "bad"})
		remote.forkErr = errors.New("fork")
		_, _ = hubThreadFork(ctx, hubcore.WebConfig{}, remoteReg, appwire.ThreadForkParams{Ref: "remote:r"})
		remote.forkErr = nil
		_, _ = hubThreadFork(ctx, hubcore.WebConfig{}, registry, appwire.ThreadForkParams{Ref: "local:past", SourceTurnID: "x", EditedInput: "edit"})
		_, _ = hubThreadFork(ctx, hubcore.WebConfig{}, registry, appwire.ThreadForkParams{Ref: "local:past", SourceTurnID: "1"})
		_, _ = hubThreadFork(ctx, hubcore.WebConfig{}, registry, appwire.ThreadForkParams{Ref: "local:past", SourceTurnID: "1", EditedInput: "edit"})
		_, _ = hubThreadFork(ctx, hubcore.WebConfig{StateDir: t.TempDir()}, registry, appwire.ThreadForkParams{Ref: "local:past", SourceTurnID: "1", EditedInput: "edit"})

		extra := &pass6LifecycleSource{scriptedAppSource: &scriptedAppSource{id: "remote"}, listed: []appwire.Thread{{ID: "remote-id", Name: "Needle", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}, Serf: appwire.SerfThread{Ref: "remote:remote-id"}}, {SessionID: "sid", Source: "remote", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusNotLoaded}}}}
		registry.Add(extra)
		_, _ = hubThreadList(ctx, cfg, registry, appwire.ThreadListParams{})
		_, _ = hubThreadList(ctx, cfg, registry, appwire.ThreadListParams{SourceIDs: []string{"remote"}, Statuses: []string{"active"}, SearchTerm: "needle", Limit: 1})
		extra.listErr = errors.New("list")
		_, _ = hubThreadList(ctx, cfg, registry, appwire.ThreadListParams{})
		_, _ = hubThreadList(ctx, cfg, registry, appwire.ThreadListParams{SourceIDs: []string{"remote"}})
		_, _ = hubThreadList(ctx, managedCfg, remoteReg, appwire.ThreadListParams{SourceIDs: []string{"managed"}})
		_ = ensureManagedCodexSources(ctx, hubcore.WebConfig{}, nil, appwire.ThreadListParams{})
		badManaged := hubcore.WebConfig{CodexLauncher: codexlaunch.NewCodexLauncher([]codexlaunch.CodexLaunchConfig{{ID: "bad", Binary: "/does/not/exist"}}), CodexLaunches: []codexlaunch.CodexLaunchConfig{{ID: "bad"}}}
		_ = ensureManagedCodexSources(ctx, badManaged, remoteReg, appwire.ThreadListParams{})
		_ = ensureManagedCodexSources(ctx, badManaged, remoteReg, appwire.ThreadListParams{SourceIDs: []string{"bad"}})

		for _, th := range []appwire.Thread{{}, {Source: "remote"}, {Serf: appwire.SerfThread{Ref: "remote:x"}}, {ID: "past", Preview: "past", Path: ".", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}}} {
			_ = threadListSourceID("local", th)
			_ = mergePastMetadataForList(cfg, "local", th)
			_ = sanitizeStaleProcessingStatus(cfg, th)
		}
		_ = mergePastMetadataForList(hubcore.WebConfig{}, "local", thread)
		_ = mergePastMetadataForList(cfg, "remote", thread)
		_ = sanitizeStaleProcessingStatus(hubcore.WebConfig{}, thread)
		_ = sanitizeStaleProcessingStatus(cfg, appwire.Thread{ID: "past", Source: "remote", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}})
		_ = sanitizeStaleProcessingStatus(cfg, appwire.Thread{ID: "missing", Source: "local", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}})
		_ = threadListSourceKey("", "")
		_ = sourceAllowedForList("local", appwire.ThreadListParams{SourceIDs: []string{"remote", "local"}})
		_ = sourceExplicitlyRequestedForList("local", appwire.ThreadListParams{SourceIDs: []string{"remote"}})
		for _, status := range []string{" active ", "notloaded", "systemerror", "IDLE"} {
			_ = normalizeThreadListStatusFilter(status)
		}
		for _, p := range []appwire.ThreadListParams{{}, {Statuses: []string{"idle"}}, {Statuses: []string{"active"}}, {SourceIDs: []string{"local"}}, {SourceIDs: []string{"remote"}}, {SearchTerm: "past"}, {SearchTerm: "absent"}} {
			_ = appThreadMatches(thread, p)
		}
		_ = variant
	})
}
