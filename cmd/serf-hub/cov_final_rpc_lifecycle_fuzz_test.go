package main

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/rendezvous"
)

type finalLifecycleSource struct {
	*scriptedAppSource
	readErr      error
	compactErr   error
	retryCompact bool
	compactCalls int
}

func (s *finalLifecycleSource) ReadThread(ctx context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	if s.readErr != nil {
		return appwire.ThreadReadResponse{}, s.readErr
	}
	return s.scriptedAppSource.ReadThread(ctx, p)
}

func (s *finalLifecycleSource) CompactThread(context.Context, appwire.ThreadCompactStartParams) error {
	s.compactCalls++
	if s.retryCompact && s.compactCalls == 1 {
		return appwire.SessionUnavailable("gone")
	}
	return s.compactErr
}

// FuzzFinalRPCLifecycle closes the remaining hub lifecycle error contracts at
// real source, spawner, registry, and router boundaries.
func FuzzFinalRPCLifecycle(f *testing.F) {
	for i := byte(0); i < 4; i++ {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, mode byte) {
		ctx := context.Background()
		thread := appwire.Thread{
			ID: "thread", SessionID: "thread", Source: "remote",
			Serf: appwire.SerfThread{Ref: "remote:thread", Capabilities: appwire.ThreadCapabilities{Compact: true}},
		}
		remote := &finalLifecycleSource{scriptedAppSource: &scriptedAppSource{id: "remote", thread: thread}}
		registry := appsource.NewRegistry()
		registry.Add(remote)

		// Input validation, absent source, bad cwd/model, and launch failures.
		_, _ = hubThreadStart(ctx, hubcore.WebConfig{}, registry, appwire.ThreadStartParams{Input: []appwire.InputItem{{Type: "input_image", Data: make([]byte, hubcore.SendMaxImageBytes+1)}}})
		_, _ = hubThreadStart(ctx, hubcore.WebConfig{}, registry, appwire.ThreadStartParams{Harness: "missing"})
		spawner := &fakeRPCModelContractSpawner{
			fakeRPCSpawner: fakeRPCSpawner{
				spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
					return rendezvous.Entry{}, errors.New("spawn failed")
				},
				resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
					return rendezvous.Entry{}, errors.New("resume failed")
				},
			},
			contract: appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5"}}},
		}
		cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Spawner: spawner}
		_, _ = hubThreadStart(ctx, cfg, registry, appwire.ThreadStartParams{CWD: t.TempDir() + "/missing", Model: "openai/gpt-5"})
		_, _ = hubThreadStart(ctx, cfg, registry, appwire.ThreadStartParams{Model: "/"})
		_, _ = hubThreadStart(ctx, cfg, registry, appwire.ThreadStartParams{Model: "openai/gpt-5"})

		// Resume validation and spawner/source failures.
		_, _ = hubThreadResume(ctx, hubcore.WebConfig{}, registry, appwire.ThreadResumeParams{Ref: ":"})
		_, _ = hubThreadResume(ctx, hubcore.WebConfig{}, registry, appwire.ThreadResumeParams{Ref: "missing:thread"})
		_, _ = hubThreadResume(ctx, cfg, registry, appwire.ThreadResumeParams{})
		_, _ = hubThreadResume(ctx, cfg, registry, appwire.ThreadResumeParams{Session: "thread"})

		// Remote fork resolution/capability failures and local validation paths.
		_, _ = hubThreadFork(ctx, hubcore.WebConfig{}, registry, appwire.ThreadForkParams{Ref: ":"})
		_, _ = hubThreadFork(ctx, hubcore.WebConfig{}, registry, appwire.ThreadForkParams{Ref: "missing:thread"})
		_, _ = hubThreadFork(ctx, hubcore.WebConfig{}, registry, appwire.ThreadForkParams{Ref: "remote:thread", EditedInput: "edit"})
		_, _ = hubThreadFork(ctx, hubcore.WebConfig{}, registry, appwire.ThreadForkParams{Ref: "local:thread", SourceTurnID: "1"})
		_, _ = hubThreadFork(ctx, hubcore.WebConfig{}, registry, appwire.ThreadForkParams{Ref: "local:thread", SourceTurnID: "1", EditedInput: "edit"})

		// Compact distinguishes unknown refs, ordinary errors, and failed resume.
		_ = compactThreadWithResume(ctx, hubcore.WebConfig{}, registry, appwire.ThreadCompactStartParams{Ref: "missing:thread"})
		remote.compactErr = errors.New("compact failed")
		_ = compactThreadWithResume(ctx, hubcore.WebConfig{}, registry, appwire.ThreadCompactStartParams{Ref: "remote:thread"})
		remote.compactErr = appwire.SessionUnavailable("gone")
		_ = compactThreadWithResume(ctx, hubcore.WebConfig{}, registry, appwire.ThreadCompactStartParams{Ref: "remote:thread"})
		past, _, pastID := pass5Past(t)
		localThread := thread
		localThread.ID, localThread.SessionID, localThread.Source = pastID, pastID, "local"
		localThread.Serf.Ref = "local:" + pastID
		local := &finalLifecycleSource{scriptedAppSource: &scriptedAppSource{id: "local", thread: localThread}}
		localRegistry := appsource.NewRegistry()
		localRegistry.Add(local)
		knownCfg := cfg
		knownCfg.Past = past
		local.compactErr = nil
		_ = compactThreadWithResume(ctx, knownCfg, localRegistry, appwire.ThreadCompactStartParams{Ref: localThread.Serf.Ref})
		local.compactErr = errors.New("compact failed")
		_ = compactThreadWithResume(ctx, knownCfg, localRegistry, appwire.ThreadCompactStartParams{Ref: localThread.Serf.Ref})
		local.compactErr = appwire.SessionUnavailable("gone")
		_ = compactThreadWithResume(ctx, knownCfg, localRegistry, appwire.ThreadCompactStartParams{Ref: localThread.Serf.Ref})
		local.compactErr, local.retryCompact, local.compactCalls = nil, true, 0
		knownCfg.Spawner = &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			return rendezvous.Entry{ThreadID: pastID, SessionID: pastID}, nil
		}}
		_ = compactThreadWithResume(ctx, knownCfg, localRegistry, appwire.ThreadCompactStartParams{Ref: localThread.Serf.Ref})

		// Construct the full router for each seed; mode keeps the target mutable.
		_ = newHubAppServer(hubcore.WebConfig{HubStateRoot: t.TempDir()}, registry)
		_ = mode
	})
}
