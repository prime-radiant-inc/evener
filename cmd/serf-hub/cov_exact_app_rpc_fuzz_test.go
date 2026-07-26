package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/rendezvous"
)

type exactRPCSource struct {
	*scriptedAppSource
	mu                              sync.Mutex
	readErr, turnsErr, subscribeErr error
	notifications                   chan appwire.Notification
	startThread                     appwire.Thread
	resumeThread                    appwire.Thread
	started                         chan struct{}
	release                         chan struct{}
	startTurnErr                    error
	startTurnErrors                 []error
	canceled                        chan struct{}
}

func (s *exactRPCSource) ReadThread(ctx context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	if s.readErr != nil {
		return appwire.ThreadReadResponse{}, s.readErr
	}
	return s.scriptedAppSource.ReadThread(ctx, p)
}
func (s *exactRPCSource) ListTurns(context.Context, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	if s.turnsErr != nil {
		return appwire.ThreadTurnsListResponse{}, s.turnsErr
	}
	return appwire.ThreadTurnsListResponse{Data: s.thread.Turns}, nil
}
func (s *exactRPCSource) SubscribeThread(ctx context.Context, _ appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.subscribeErr != nil {
		return nil, s.subscribeErr
	}
	if s.notifications != nil {
		return s.notifications, nil
	}
	ch := make(chan appwire.Notification)
	go func() {
		<-ctx.Done()
		if s.canceled != nil {
			close(s.canceled)
		}
		close(ch)
	}()
	return ch, nil
}
func (s *exactRPCSource) StartThread(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	return appwire.ThreadStartResponse{Thread: s.startThread}, nil
}
func (s *exactRPCSource) ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	return appwire.ThreadResumeResponse{Thread: s.resumeThread}, nil
}
func (s *exactRPCSource) StartTurn(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	s.mu.Lock()
	if len(s.startTurnErrors) > 0 {
		err := s.startTurnErrors[0]
		s.startTurnErrors = s.startTurnErrors[1:]
		s.mu.Unlock()
		return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_exact"}}, err
	}
	s.mu.Unlock()
	if s.startTurnErr != nil {
		return appwire.TurnStartResponse{}, s.startTurnErr
	}
	return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_exact"}}, nil
}

func exactDispatch(ctx context.Context, t *testing.T, server *appserver.Server, method string, params any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return server.Router().Dispatch(ctx, appwire.Request{ID: appwire.NewIntID(1), Method: method, Params: raw})
}

func exactDispatchFailures(t *testing.T, server *appserver.Server) {
	t.Helper()
	ctx := context.Background()
	badImage := []appwire.InputItem{{Type: "image", Data: make([]byte, hubcore.SendMaxImageBytes+1)}}
	calls := []struct {
		method string
		params any
	}{
		{appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "missing:x"}},
		{appwire.MethodThreadTurnsList, appwire.ThreadTurnsListParams{Ref: "missing:x"}},
		{appwire.MethodSerfSubagentPreview, appwire.SerfSubagentPreviewParams{Ref: "missing:x"}},
		{appwire.MethodTurnStart, appwire.TurnStartParams{Ref: "missing:x", Input: badImage}},
		{appwire.MethodTurnStart, appwire.TurnStartParams{Ref: "missing:x"}},
		{appwire.MethodTurnSteer, appwire.TurnSteerParams{Ref: "missing:x"}},
		{appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{Ref: "missing:x"}},
		{appwire.MethodSerfSandboxEscalationResolve, appwire.SandboxEscalationResolveParams{Ref: "missing:x"}},
		{appwire.MethodTurnQueue, appwire.TurnQueueParams{Ref: "missing:x", Input: badImage}},
		{appwire.MethodTurnQueue, appwire.TurnQueueParams{Ref: "missing:x"}},
		{appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{Ref: "missing:x", Input: badImage}},
		{appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{Ref: "missing:x"}},
		{appwire.MethodThreadClear, appwire.ThreadClearParams{Ref: "missing:x"}},
		{appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: "missing:x"}},
		{appwire.MethodThreadModelSet, appwire.ThreadModelSetParams{Ref: "missing:x"}},
		{appwire.MethodSerfThreadNameSet, appwire.ThreadNameSetParams{Ref: "missing:x"}},
		{appwire.MethodThreadReasoningEffortSet, appwire.ThreadReasoningEffortSetParams{Ref: "missing:x"}},
		{appwire.MethodGoalSet, appwire.GoalSetParams{Ref: "missing:x"}},
		{appwire.MethodSerfTasksList, appwire.TaskListParams{Ref: "missing:x"}},
	}
	for _, c := range calls {
		_, _ = exactDispatch(ctx, t, server, c.method, c.params)
	}
}

func FuzzExactAppRPC(f *testing.F) {
	for i := range uint8(6) {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, variant uint8) {
		oldInterval := hubRelayIdleInterval
		hubRelayIdleInterval = time.Millisecond
		t.Cleanup(func() { hubRelayIdleInterval = oldInterval })
		thread := appwire.Thread{ID: "thread", SessionID: "session", Source: "remote", CWD: t.TempDir(), Serf: appwire.SerfThread{Ref: "remote:thread", Capabilities: appwire.ThreadCapabilities{Send: true, Steer: true, Interrupt: true, Clear: true, Shutdown: true, ChangeModel: true, Rename: true, Queue: true, Goal: true}}, Turns: []appwire.Turn{{ID: "turn_1"}}}
		registry := appsource.NewRegistry()
		source := &exactRPCSource{scriptedAppSource: &scriptedAppSource{id: "remote", thread: thread}, startThread: thread, resumeThread: thread}
		registry.Add(source)
		cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), RelayHooks: hubcore.RelayLifecycleHooks{IdleExit: func(string) {}, AfterIdleDelete: func(string) {}}}
		server := newHubAppServer(cfg, registry)

		switch variant % 6 {
		case 0:
			exactDispatchFailures(t, server)
			noCaps := thread
			noCaps.Serf.Capabilities = appwire.ThreadCapabilities{}
			source.thread = noCaps
			for _, c := range []struct {
				m string
				p any
			}{
				{appwire.MethodTurnStart, appwire.TurnStartParams{Ref: "remote:thread"}}, {appwire.MethodTurnSteer, appwire.TurnSteerParams{Ref: "remote:thread"}},
				{appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{Ref: "remote:thread"}}, {appwire.MethodTurnQueue, appwire.TurnQueueParams{Ref: "remote:thread"}},
				{appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{Ref: "remote:thread"}}, {appwire.MethodThreadClear, appwire.ThreadClearParams{Ref: "remote:thread"}},
				{appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: "remote:thread"}}, {appwire.MethodThreadModelSet, appwire.ThreadModelSetParams{Ref: "remote:thread"}},
				{appwire.MethodSerfThreadNameSet, appwire.ThreadNameSetParams{Ref: "remote:thread"}}, {appwire.MethodGoalSet, appwire.GoalSetParams{Ref: "remote:thread"}},
			} {
				_, _ = exactDispatch(context.Background(), t, server, c.m, c.p)
			}
		case 1:
			// Empty IDs and refs cover the best-effort start/resume relay normalization.
			source.startThread = appwire.Thread{}
			source.resumeThread = appwire.Thread{SessionID: "session", Source: ""}
			_, _ = exactDispatch(context.Background(), t, server, appwire.MethodThreadStart, appwire.ThreadStartParams{Harness: "remote"})
			_, _ = exactDispatch(context.Background(), t, server, appwire.MethodThreadResume, appwire.ThreadResumeParams{Ref: "remote:thread"})
			// A local notification exercises image enrichment before idle cleanup.
			localThread := thread
			localThread.Source = "local"
			localThread.Serf.Ref = "local:thread"
			ch := make(chan appwire.Notification, 1)
			ch <- appwire.Notification{Method: appwire.NotifyThreadStatusChanged}
			close(ch)
			local := &exactRPCSource{scriptedAppSource: &scriptedAppSource{id: "local", thread: localThread}, notifications: ch}
			lr := appsource.NewRegistry()
			lr.Add(local)
			ls := newHubAppServer(cfg, lr)
			_, _ = exactDispatch(context.Background(), t, ls, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "local:thread", Subscribe: true, ReplaceSubscription: true})
			time.Sleep(3 * time.Millisecond)
		case 2:
			// Subscription failure and read failure cover relay and turn error propagation.
			source.subscribeErr = errors.New("subscribe")
			_, _ = exactDispatch(context.Background(), t, server, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "remote:thread", Subscribe: true, ReplaceSubscription: true})
			source.subscribeErr = nil
			source.readErr = errors.New("read")
			_, _ = exactDispatch(context.Background(), t, server, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: "remote:thread"})
			_, _ = exactDispatch(context.Background(), t, server, appwire.MethodSerfSubagentPreview, appwire.SerfSubagentPreviewParams{Ref: "remote:thread"})
			_, _ = exactDispatch(context.Background(), t, server, appwire.MethodTurnStart, appwire.TurnStartParams{Ref: "remote:thread"})
		case 3:
			// Filesystem-backed source enumeration and command sorting.
			t.Setenv("HOME", "")
			_ = newHubAppServer(hubcore.WebConfig{PluginRoot: t.TempDir()}, appsource.NewRegistry())
			runDir := t.TempDir()
			_, _ = rendezvous.Write(runDir, rendezvous.Entry{PID: 7, Address: "127.0.0.1:1", ThreadID: "thread"})
			runRegistry := newHubSourceRegistry(hubcore.WebConfig{RunDir: runDir, CodexSources: []appsource.CodexSourceConfig{{ID: "codex"}}})
			if local, ok := runRegistry.Source("local"); ok {
				_, _ = local.ListThreads(context.Background(), appwire.ThreadListParams{})
			}
			roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{Entry: rendezvous.Entry{ThreadID: "x", PID: 1}, SessionID: "x"})
			rosterRegistry := newHubSourceRegistry(hubcore.WebConfig{Roster: roster})
			if local, ok := rosterRegistry.Source("local"); ok {
				_, _ = local.ListThreads(context.Background(), appwire.ThreadListParams{})
			}
			pluginDir := t.TempDir()
			_ = os.MkdirAll(filepath.Join(pluginDir, ".claude-plugin"), 0o755)
			_ = os.WriteFile(filepath.Join(pluginDir, ".claude-plugin", "plugin.json"), []byte(`{"name":"p"}`), 0o600)
			_ = os.MkdirAll(filepath.Join(pluginDir, "commands"), 0o755)
			_ = os.WriteFile(filepath.Join(pluginDir, "commands", "z.md"), []byte("---\ndescription: z\n---\nz"), 0o600)
			_ = os.WriteFile(filepath.Join(pluginDir, "commands", "a.md"), []byte("---\ndescription: a\n---\na"), 0o600)
			pluginDir2 := t.TempDir()
			_ = os.MkdirAll(filepath.Join(pluginDir2, ".claude-plugin"), 0o755)
			_ = os.WriteFile(filepath.Join(pluginDir2, ".claude-plugin", "plugin.json"), []byte(`{"name":"q"}`), 0o600)
			_ = os.MkdirAll(filepath.Join(pluginDir2, "commands"), 0o755)
			_ = os.WriteFile(filepath.Join(pluginDir2, "commands", "a.md"), []byte("---\ndescription: a\n---\na"), 0o600)
			_, _ = hubCommandList(hubcore.WebConfig{PluginDirs: []string{pluginDir, pluginDir2}})

			for _, c := range []struct {
				m string
				p any
			}{
				{appwire.MethodSerfAuthStatus, appwire.AuthStatusParams{}},
				{appwire.MethodSerfAuthLoginStart, appwire.AuthLoginStartParams{}},
				{appwire.MethodSerfAuthLoginComplete, appwire.AuthLoginCompleteParams{}},
				{appwire.MethodSerfAuthLogout, appwire.AuthLogoutParams{}},
				{appwire.MethodSerfAuthList, appwire.EmptyParams{}},
				{appwire.MethodSerfAuthApiKeySet, appwire.AuthApiKeySetParams{}},
				{appwire.MethodSerfAuthDeviceStart, appwire.AuthDeviceStartParams{}},
				{appwire.MethodSerfAuthDevicePoll, appwire.AuthDevicePollParams{}},
				{appwire.MethodSerfLaunchResolve, appwire.LaunchConfigResolveParams{}},
				{appwire.MethodSerfLaunchSchema, appwire.EmptyParams{}},
				{appwire.MethodSerfLaunchGetLayer, appwire.LaunchConfigGetLayerParams{}},
				{appwire.MethodSerfLaunchSetLayer, appwire.LaunchConfigSetLayerParams{}},
				{appwire.MethodSerfLaunchTrustRepo, appwire.LaunchConfigTrustRepoParams{}},
				{appwire.MethodSerfMarketplaceList, appwire.EmptyParams{}},
				{appwire.MethodSerfMarketplaceAdd, appwire.MarketplaceAddParams{}},
				{appwire.MethodSerfMarketplaceRemove, appwire.MarketplaceNameParams{}},
				{appwire.MethodSerfMarketplaceRefresh, appwire.MarketplaceNameParams{}},
				{appwire.MethodSerfMarketplaceBrowse, appwire.MarketplaceBrowseParams{}},
				{appwire.MethodSerfPluginList, appwire.EmptyParams{}},
				{appwire.MethodSerfPluginInstall, appwire.PluginRefParams{}},
				{appwire.MethodSerfPluginUpgrade, appwire.PluginRefParams{}},
				{appwire.MethodSerfPluginRemove, appwire.PluginRefParams{}},
				{appwire.MethodSerfPluginEnable, appwire.PluginRefParams{}},
				{appwire.MethodSerfPluginDisable, appwire.PluginRefParams{}},
				{appwire.MethodSerfPluginSetAutoUpgrade, appwire.PluginSetAutoUpgradeParams{}},
				{appwire.MethodSerfHarnessesList, appwire.HarnessListParams{}},
				{appwire.MethodSerfCommandList, appwire.EmptyParams{}},
			} {
				_, _ = exactDispatch(context.Background(), t, server, c.m, c.p)
			}
			_, _ = exactDispatch(context.Background(), t, server, appwire.MethodSerfAuthApiKeySet, appwire.AuthApiKeySetParams{Provider: "anthropic", Value: "test-key"})
			_, _ = exactDispatch(context.Background(), t, server, appwire.MethodSerfAuthLogout, appwire.AuthLogoutParams{Provider: "anthropic"})
			launchServer := appserver.NewServer(appserver.ServerConfig{})
			registerLaunchHandlers(launchServer, newHubLaunchController(t.TempDir()))
			_, _ = exactDispatch(context.Background(), t, launchServer, appwire.MethodSerfLaunchSetLayer, appwire.LaunchConfigSetLayerParams{CWD: t.TempDir(), Layer: "global"})

			// Drive successful instance mutations through their registered closures.
			instDir := t.TempDir()
			tomlPath := filepath.Join(instDir, "providers.toml")
			writeMinimalProvidersToml(t, tomlPath)
			instCtl := newTestInstancesController(t, tomlPath, instDir, t.TempDir())
			instServer := appserver.NewServer(appserver.ServerConfig{})
			registerInstanceHandlers(instServer, instCtl)
			for _, c := range []struct {
				m string
				p any
			}{
				{appwire.MethodSerfInstanceList, appwire.EmptyParams{}},
				{appwire.MethodSerfInstanceCreate, appwire.InstanceCreateParams{Name: "work", Type: "anthropic"}},
				{appwire.MethodSerfInstanceEdit, appwire.InstanceEditParams{Name: "work"}},
				{appwire.MethodSerfInstanceSetDefault, appwire.InstanceSetDefaultParams{Name: "work"}},
				{appwire.MethodSerfInstanceRemove, appwire.InstanceRemoveParams{Name: "work"}},
			} {
				if _, err := exactDispatch(context.Background(), t, instServer, c.m, c.p); err != nil {
					t.Fatalf("instance %s: %v", c.m, err)
				}
			}

			pastRoot := t.TempDir()
			pastState := filepath.Join(pastRoot, "projects", "saved")
			pastID := buildRPCParentSession(t, pastState)
			past := hubcore.NewPastIndex(filepath.Join(pastRoot, "projects", "*"))
			if _, err := past.Rebuild(); err != nil {
				t.Fatal(err)
			}
			pastServer := newHubAppServer(hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: past, PluginRoot: t.TempDir()}, appsource.NewRegistry())
			pastRef := "local:" + pastID
			_, _ = exactDispatch(context.Background(), t, pastServer, appwire.MethodThreadRead, appwire.ThreadReadParams{Ref: pastRef, IncludeTurns: true, TurnLimit: 1})
			_, _ = exactDispatch(context.Background(), t, pastServer, appwire.MethodThreadTurnsList, appwire.ThreadTurnsListParams{Ref: pastRef, Limit: 1})
			_, _ = exactDispatch(context.Background(), t, pastServer, appwire.MethodSerfSubagentPreview, appwire.SerfSubagentPreviewParams{Ref: pastRef, Limit: 1})

			// Drive every successful marketplace/plugin mutation through the router.
			marketDir := t.TempDir()
			writeTestMarketplace(t, marketDir)
			pluginCtl := newHubPluginsController(t.TempDir())
			pluginServer := appserver.NewServer(appserver.ServerConfig{})
			registerPluginHandlers(pluginServer, pluginCtl)
			ref := appwire.PluginRefParams{Plugin: "widget", Marketplace: "acme"}
			for _, c := range []struct {
				m string
				p any
			}{
				{appwire.MethodSerfMarketplaceAdd, appwire.MarketplaceAddParams{Source: appwire.MarketplaceSourceInput{Kind: "directory", Path: marketDir}}},
				{appwire.MethodSerfMarketplaceBrowse, appwire.MarketplaceBrowseParams{Name: "acme"}},
				{appwire.MethodSerfMarketplaceRefresh, appwire.MarketplaceNameParams{Name: "acme"}},
				{appwire.MethodSerfPluginInstall, ref},
				{appwire.MethodSerfPluginDisable, ref},
				{appwire.MethodSerfPluginEnable, ref},
				{appwire.MethodSerfPluginSetAutoUpgrade, appwire.PluginSetAutoUpgradeParams{Plugin: "widget", Marketplace: "acme", AutoUpgrade: true}},
				{appwire.MethodSerfPluginUpgrade, ref},
				{appwire.MethodSerfPluginRemove, ref},
				{appwire.MethodSerfMarketplaceRemove, appwire.MarketplaceNameParams{Name: "acme"}},
			} {
				_, _ = exactDispatch(context.Background(), t, pluginServer, c.m, c.p)
			}
		case 4:
			var relay hubRelayFunctions
			observeHubRelayFunctions = func(got hubRelayFunctions) { relay = got }
			_ = newHubAppServer(cfg, registry)
			observeHubRelayFunctions = nil
			t.Cleanup(func() { observeHubRelayFunctions = nil })

			_ = relay.startRelay(context.Background(), source, appwire.ThreadReadParams{}, appwire.Thread{})
			blankRef := thread
			blankRef.Serf.Ref = ""
			_ = relay.startRelay(context.Background(), source, appwire.ThreadReadParams{}, blankRef)
			source.readErr = errors.New("read")
			_, _ = relay.startTurn(context.Background(), source, appwire.TurnStartParams{Ref: "remote:thread"})
			source.readErr = nil
			noSend := thread
			noSend.Serf.Capabilities.Send = false
			source.thread = noSend
			_, _ = relay.startTurn(context.Background(), source, appwire.TurnStartParams{Ref: "remote:thread"})
			source.thread = thread
			errThread := thread
			errThread.Serf.Ref = "err:thread"
			errSource := &exactRPCSource{scriptedAppSource: &scriptedAppSource{id: "err", thread: errThread}, subscribeErr: errors.New("turn relay")}
			_, _ = relay.startTurn(context.Background(), errSource, appwire.TurnStartParams{Ref: "err:thread"})
			_, _ = exactDispatch(context.Background(), t, server, appwire.MethodThreadTurnsList, appwire.ThreadTurnsListParams{Ref: "remote:thread"})

			open := make(chan appwire.Notification)
			source.notifications = open
			_ = relay.startRelay(context.Background(), source, appwire.ThreadReadParams{}, thread)
			_ = relay.startRelay(context.Background(), source, appwire.ThreadReadParams{ReplaceSubscription: true}, thread)
			close(open)

			blocked := &exactRPCSource{
				scriptedAppSource: &scriptedAppSource{id: "blocked", thread: thread},
				subscribeErr:      errors.New("blocked failure"),
				started:           make(chan struct{}, 1),
				release:           make(chan struct{}),
			}
			firstDone := make(chan error, 1)
			go func() {
				firstDone <- relay.startRelay(context.Background(), blocked, appwire.ThreadReadParams{}, thread)
			}()
			<-blocked.started
			canceled, cancel := context.WithCancel(context.Background())
			cancel()
			_ = relay.startRelay(canceled, blocked, appwire.ThreadReadParams{}, thread)
			secondDone := make(chan error, 1)
			waiterReady := make(chan struct{}, 1)
			observeHubRelayWait = func() { waiterReady <- struct{}{} }
			go func() {
				secondDone <- relay.startRelay(context.Background(), blocked, appwire.ThreadReadParams{}, thread)
			}()
			<-waiterReady
			close(blocked.release)
			<-firstDone
			<-secondDone
			observeHubRelayWait = nil
			t.Cleanup(func() { observeHubRelayWait = nil })

			stopThread := thread
			stopThread.ID, stopThread.Serf.Ref = "stop", "stop:stop"
			stopSource := &exactRPCSource{scriptedAppSource: &scriptedAppSource{id: "stop", thread: stopThread}, canceled: make(chan struct{})}
			_ = relay.startRelay(context.Background(), stopSource, appwire.ThreadReadParams{}, stopThread)
			relay.stopRelay("stop:stop")
			<-stopSource.canceled

			idleHit := make(chan struct{})
			var idleServer *appserver.Server
			var idleRelay hubRelayFunctions
			idleCfg := cfg
			idleCfg.RelayHooks.IdleExit = func(string) {
				idleServer.NewConnection("idle-conn").Subscribe("idle:thread")
				close(idleHit)
			}
			observeHubRelayFunctions = func(got hubRelayFunctions) { idleRelay = got }
			idleServer = newHubAppServer(idleCfg, registry)
			observeHubRelayFunctions = nil
			idleThread := thread
			idleSource := &exactRPCSource{scriptedAppSource: &scriptedAppSource{id: "idle", thread: idleThread}, notifications: make(chan appwire.Notification)}
			_ = idleRelay.startRelay(context.Background(), idleSource, appwire.ThreadReadParams{}, idleThread)
			<-idleHit
			close(idleSource.notifications)

			_ = relay.startRelayForThread(context.Background(), appwire.Thread{})
			_ = relay.startRelayForThread(context.Background(), appwire.Thread{SessionID: "session"})
		case 5:
			oldResolve, oldResume := resolveTurnStartSource, resumeTurnStartThread
			oldLogin, oldPoll, oldTrust := authLoginComplete, authDevicePoll, launchTrustRepo
			t.Cleanup(func() {
				resolveTurnStartSource, resumeTurnStartThread = oldResolve, oldResume
				authLoginComplete, authDevicePoll, launchTrustRepo = oldLogin, oldPoll, oldTrust
			})
			resumeTurnStartThread = func(context.Context, hubcore.WebConfig, *appsource.Registry, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
				return appwire.ThreadResumeResponse{}, nil
			}

			calls := 0
			resolveTurnStartSource = func(context.Context, hubcore.WebConfig, *appsource.Registry, string, string) (appsource.Source, error) {
				calls++
				return nil, errors.New("resolve")
			}
			_, _ = exactDispatch(context.Background(), t, server, appwire.MethodTurnStart, appwire.TurnStartParams{})

			calls = 0
			source.startTurnErr = appwire.SessionUnavailable("resume")
			resolveTurnStartSource = func(context.Context, hubcore.WebConfig, *appsource.Registry, string, string) (appsource.Source, error) {
				calls++
				if calls == 1 {
					return source, nil
				}
				return nil, errors.New("resolve after resume")
			}
			_, _ = exactDispatch(context.Background(), t, server, appwire.MethodTurnStart, appwire.TurnStartParams{})

			calls = 0
			resolveTurnStartSource = func(context.Context, hubcore.WebConfig, *appsource.Registry, string, string) (appsource.Source, error) {
				calls++
				return source, nil
			}
			source.startTurnErr = nil
			_, _ = exactDispatch(context.Background(), t, server, appwire.MethodTurnStart, appwire.TurnStartParams{})
			source.startTurnErr = errors.New("plain")
			_, _ = exactDispatch(context.Background(), t, server, appwire.MethodTurnStart, appwire.TurnStartParams{})
			source.startTurnErr = nil
			source.startTurnErrors = []error{appwire.SessionUnavailable("resume"), nil}
			_, _ = exactDispatch(context.Background(), t, server, appwire.MethodTurnStart, appwire.TurnStartParams{})

			authLoginComplete = func(*hubAuthController, context.Context, appwire.AuthLoginCompleteParams) (appwire.AuthLoginCompleteResponse, error) {
				return appwire.AuthLoginCompleteResponse{Status: appwire.AuthStatusResponse{Provider: "openai"}}, nil
			}
			authDevicePoll = func(*hubAuthController, context.Context, appwire.AuthDevicePollParams) (appwire.AuthDevicePollResponse, error) {
				return appwire.AuthDevicePollResponse{State: "authorized", Status: appwire.AuthStatusResponse{Provider: "openai"}}, nil
			}
			_, _ = exactDispatch(context.Background(), t, server, appwire.MethodSerfAuthLoginComplete, appwire.AuthLoginCompleteParams{})
			_, _ = exactDispatch(context.Background(), t, server, appwire.MethodSerfAuthDevicePoll, appwire.AuthDevicePollParams{})
			launchTrustRepo = func(*hubLaunchController, context.Context, appwire.LaunchConfigTrustRepoParams) (appwire.LaunchConfigResolved, error) {
				return appwire.LaunchConfigResolved{}, nil
			}
			_, _ = exactDispatch(context.Background(), t, server, appwire.MethodSerfLaunchTrustRepo, appwire.LaunchConfigTrustRepoParams{})
			_, _ = exactDispatch(context.Background(), t, instServerForExactSetDefaultError(t), appwire.MethodSerfInstanceSetDefault, appwire.InstanceSetDefaultParams{Name: "missing"})
		}
	})
}

func instServerForExactSetDefaultError(t *testing.T) *appserver.Server {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, path)
	server := appserver.NewServer(appserver.ServerConfig{})
	registerInstanceHandlers(server, newTestInstancesController(t, path, dir, t.TempDir()))
	return server
}
