package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"primeradiant.com/serf/cmd/serf-hub/internal/fspaths"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appsource"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/rendezvous"
)

func newHubSourceRegistry(cfg hubcore.WebConfig) *appsource.Registry {
	registry := appsource.NewRegistry()
	registry.Add(appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		if cfg.Roster != nil {
			live := cfg.Roster.List()
			entries := make([]appsource.LocalDaemonEntry, 0, len(live))
			for _, item := range live {
				entries = append(entries, appsource.LocalDaemonEntry{
					Entry:     item.Entry,
					SessionID: item.SessionID,
					Status:    item.Status,
				})
			}
			return entries
		}
		if cfg.RunDir == "" {
			return nil
		}
		raw, _ := rendezvous.List(cfg.RunDir)
		entries := make([]appsource.LocalDaemonEntry, 0, len(raw))
		for _, entry := range raw {
			entries = append(entries, appsource.LocalDaemonEntry{Entry: entry})
		}
		return entries
	}, http.DefaultClient))
	for _, source := range cfg.CodexSources {
		registry.Add(appsource.NewCodexSource(source, http.DefaultClient))
	}
	return registry
}

type hubRelayHandle struct {
	ready chan struct{}
	err   error
}

type threadReadRelayPolicy interface {
	RelayOnThreadRead() bool
}

func relayOnThreadRead(source appsource.Source) bool {
	if policy, ok := source.(threadReadRelayPolicy); ok {
		return policy.RelayOnThreadRead()
	}
	return true
}

func newHubAppServer(cfg hubcore.WebConfig, sources *appsource.Registry) *appserver.Server {
	server := appserver.NewServer(appserver.ServerConfig{
		ServerName: "serf-hub",
		Version:    Version,
		SourceID:   "local",
		Features: appwire.FeatureSet{
			ThreadList:        true,
			ThreadTurnsList:   false,
			TurnStart:         true,
			TurnSteer:         true,
			ThreadClear:       true,
			ThreadShutdown:    true,
			ForkFromTurn:      true,
			Tasks:             true,
			TranscriptList:    true,
			ModelList:         true,
			DirectoryComplete: true,
			Auth:              true,
		},
	})
	hubStateRoot := cfg.HubStateRoot
	if hubStateRoot == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			hubStateRoot = filepath.Join(home, ".serf")
		} else {
			hubStateRoot = ".serf"
		}
	}
	authController := newHubAuthControllerWithStore(hubStateRoot, cfg.CredsStore)
	authController.providersConfigPath = cfg.ProvidersConfigPath
	var instancesController *hubInstancesController
	if cfg.ProvidersConfigPath != "" {
		instancesController = &hubInstancesController{
			providersConfigPath: cfg.ProvidersConfigPath,
			auth:                authController,
		}
	}
	var relayMu sync.Mutex
	relayedThreads := map[string]*hubRelayHandle{}
	startRelay := func(ctx context.Context, source appsource.Source, params appwire.ThreadReadParams, thread appwire.Thread) error {
		threadID := thread.ID
		if threadID == "" {
			return nil
		}
		relayKey := source.ID() + ":" + threadID

		subscribeParams := params
		if subscribeParams.Ref == "" {
			subscribeParams.Ref = thread.Serf.Ref
		}
		if subscribeParams.Ref == "" {
			subscribeParams.Ref = appwire.Ref{SourceID: source.ID(), ThreadID: threadID}.String()
		}

		var relayHandle *hubRelayHandle
		for {
			relayMu.Lock()
			existing := relayedThreads[relayKey]
			if existing == nil {
				relayHandle = &hubRelayHandle{ready: make(chan struct{})}
				relayedThreads[relayKey] = relayHandle
				relayMu.Unlock()
				break
			}
			ready := existing.ready
			relayMu.Unlock()

			select {
			case <-ready:
			case <-ctx.Done():
				return ctx.Err()
			}

			relayMu.Lock()
			active := relayedThreads[relayKey] == existing
			err := existing.err
			relayMu.Unlock()
			if active && err == nil {
				if subscribeParams.ReplaceSubscription {
					appserver.ReplaceSubscriptions(ctx, relayKey)
				} else {
					appserver.Subscribe(ctx, relayKey)
				}
				return nil
			}
			if err != nil {
				return err
			}
		}

		relayCtx, cancelRelay := context.WithCancel(context.WithoutCancel(ctx))
		notifications, err := source.SubscribeThread(relayCtx, subscribeParams)
		if err != nil {
			cancelRelay()
			relayMu.Lock()
			if relayedThreads[relayKey] == relayHandle {
				delete(relayedThreads, relayKey)
			}
			relayHandle.err = err
			close(relayHandle.ready)
			relayMu.Unlock()
			return err
		}
		if subscribeParams.ReplaceSubscription {
			appserver.ReplaceSubscriptions(ctx, relayKey)
		} else {
			appserver.Subscribe(ctx, relayKey)
		}
		relayMu.Lock()
		close(relayHandle.ready)
		relayMu.Unlock()
		go func() {
			ticker := time.NewTicker(250 * time.Millisecond)
			cleanupRelay := func() {
				cancelRelay()
				relayMu.Lock()
				if relayedThreads[relayKey] == relayHandle {
					delete(relayedThreads, relayKey)
				}
				relayMu.Unlock()
			}
			defer ticker.Stop()
			defer cleanupRelay()
			for {
				select {
				case <-relayCtx.Done():
					return
				case <-ticker.C:
					if server.SubscriberCount(relayKey) == 0 {
						if cfg.RelayHooks.IdleExit != nil {
							cfg.RelayHooks.IdleExit(threadID)
						}
						relayMu.Lock()
						if server.SubscriberCount(relayKey) == 0 {
							if relayedThreads[relayKey] == relayHandle {
								delete(relayedThreads, relayKey)
							}
							relayMu.Unlock()
							if cfg.RelayHooks.AfterIdleDelete != nil {
								cfg.RelayHooks.AfterIdleDelete(threadID)
							}
							cancelRelay()
							return
						}
						relayMu.Unlock()
					}
				case notification, ok := <-notifications:
					if !ok {
						return
					}
					server.Broadcast(relayKey, notification.Method, notification.Params)
				}
			}
		}()
		return nil
	}
	startTurn := func(ctx context.Context, source appsource.Source, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		readParams := appwire.ThreadReadParams{Ref: params.Ref, ThreadID: params.ThreadID, IncludeTurns: false}
		threadResp, err := source.ReadThread(ctx, readParams)
		if err != nil {
			return appwire.TurnStartResponse{}, err
		}
		if !threadActionAvailable(threadResp.Thread.Serf.Capabilities, "send") {
			return appwire.TurnStartResponse{}, appwire.Unavailable("send is not available for this session")
		}
		if err := startRelay(ctx, source, readParams, threadResp.Thread); err != nil {
			return appwire.TurnStartResponse{}, err
		}
		return source.StartTurn(ctx, params)
	}
	startRelayForThread := func(ctx context.Context, thread appwire.Thread) error {
		if thread.ID == "" {
			thread.ID = thread.SessionID
		}
		if thread.ID == "" {
			return nil
		}
		ref := thread.Serf.Ref
		if ref == "" {
			sourceID := strings.TrimSpace(thread.Source)
			if sourceID == "" {
				sourceID = "local"
			}
			ref = appwire.Ref{SourceID: sourceID, ThreadID: thread.ID}.String()
		}
		source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, ref, thread.ID)
		if err != nil {
			return nil
		}
		if err := startRelay(ctx, source, appwire.ThreadReadParams{Ref: ref, IncludeTurns: false}, thread); err != nil {
			if isSessionUnavailableError(err) {
				return nil
			}
			return err
		}
		return nil
	}
	appserver.HandleTyped(server.Router(), appwire.MethodThreadList, func(ctx context.Context, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return hubThreadList(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, params.ThreadID)
		if err != nil {
			if thread, ok := pastThreadForRead(cfg, params); ok {
				return appwire.ThreadReadResponse{Thread: thread}, nil
			}
			return appwire.ThreadReadResponse{}, err
		}
		resp, err := source.ReadThread(ctx, params)
		if err != nil {
			if thread, ok := pastThreadForRead(cfg, params); ok {
				return appwire.ThreadReadResponse{Thread: thread}, nil
			}
			return appwire.ThreadReadResponse{}, err
		}
		resp.Thread = mergePastThreadForRead(cfg, params, resp.Thread)
		resp.Thread = sanitizeStaleProcessingStatus(cfg, resp.Thread)
		if params.Subscribe || relayOnThreadRead(source) {
			if err := startRelay(ctx, source, params, resp.Thread); err != nil {
				return appwire.ThreadReadResponse{}, err
			}
		}
		return resp, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadStart, func(ctx context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
		resp, err := hubThreadStart(ctx, cfg, sources, params)
		if err != nil {
			return appwire.ThreadStartResponse{}, err
		}
		if err := startRelayForThread(ctx, resp.Thread); err != nil {
			appserver.Notify(ctx, appwire.NotifyWarning, map[string]any{
				"threadId": resp.Thread.ID,
				"ref":      resp.Thread.Serf.Ref,
				"source":   "hub",
				"title":    "Live updates unavailable",
				"message":  "thread started, but Hub could not attach live updates: " + err.Error(),
			})
		}
		return resp, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadResume, func(ctx context.Context, params appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
		resp, err := hubThreadResume(ctx, cfg, sources, params)
		if err != nil {
			return appwire.ThreadResumeResponse{}, err
		}
		if err := startRelayForThread(ctx, resp.Thread); err != nil {
			return appwire.ThreadResumeResponse{}, err
		}
		return resp, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadFork, func(ctx context.Context, params appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
		return hubThreadFork(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnStart, func(ctx context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		if err := validateAppWireInputItems(params.Input); err != nil {
			return appwire.TurnStartResponse{}, appwire.InvalidParams(err.Error())
		}
		source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, params.ThreadID)
		if err != nil {
			if _, resumeErr := hubThreadResume(ctx, cfg, sources, appwire.ThreadResumeParams{Ref: params.Ref, Session: params.ThreadID}); resumeErr != nil {
				return appwire.TurnStartResponse{}, resumeErr
			}
			source, err = sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, params.ThreadID)
			if err != nil {
				return appwire.TurnStartResponse{}, err
			}
		}
		resp, err := startTurn(ctx, source, params)
		if err == nil {
			return resp, nil
		}
		if params.Ref != "" && !hubKnowsRef(cfg, params.Ref) {
			return appwire.TurnStartResponse{}, err
		}
		if !shouldResumeAfterTurnStartError(err) {
			return appwire.TurnStartResponse{}, err
		}
		if _, resumeErr := hubThreadResume(ctx, cfg, sources, appwire.ThreadResumeParams{Ref: params.Ref, Session: params.ThreadID}); resumeErr != nil {
			return appwire.TurnStartResponse{}, resumeErr
		}
		source, sourceErr := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, params.ThreadID)
		if sourceErr != nil {
			return appwire.TurnStartResponse{}, sourceErr
		}
		return startTurn(ctx, source, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnSteer, func(ctx context.Context, params appwire.TurnSteerParams) (appwire.EmptyResponse, error) {
		source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, params.ThreadID)
		if err != nil {
			return appwire.EmptyResponse{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, params.ThreadID, "steer"); err != nil {
			return appwire.EmptyResponse{}, err
		}
		return appwire.EmptyResponse{}, source.SteerTurn(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnInterrupt, func(ctx context.Context, params appwire.TurnInterruptParams) (appwire.EmptyResponse, error) {
		source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, params.ThreadID)
		if err != nil {
			return appwire.EmptyResponse{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, params.ThreadID, "interrupt"); err != nil {
			return appwire.EmptyResponse{}, err
		}
		return appwire.EmptyResponse{}, source.InterruptTurn(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnQueue, func(ctx context.Context, params appwire.TurnQueueParams) (appwire.EmptyResponse, error) {
		if err := validateAppWireInputItems(params.Input); err != nil {
			return appwire.EmptyResponse{}, appwire.InvalidParams(err.Error())
		}
		source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, "")
		if err != nil {
			return appwire.EmptyResponse{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "queue"); err != nil {
			return appwire.EmptyResponse{}, err
		}
		return appwire.EmptyResponse{}, source.QueueTurn(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnDrainAsSteer, func(ctx context.Context, params appwire.TurnDrainAsSteerParams) (appwire.EmptyResponse, error) {
		if err := validateAppWireInputItems(params.Input); err != nil {
			return appwire.EmptyResponse{}, appwire.InvalidParams(err.Error())
		}
		source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, "")
		if err != nil {
			return appwire.EmptyResponse{}, err
		}
		// drainAsSteer rides on the Steer capability — the daemon checks
		// queue depth separately to return Conflict when there is nothing
		// to drain.
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "steer"); err != nil {
			return appwire.EmptyResponse{}, err
		}
		return appwire.EmptyResponse{}, source.DrainAsSteer(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadClear, func(ctx context.Context, params appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
		source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, "")
		if err != nil {
			return appwire.ThreadClearResponse{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "clear"); err != nil {
			return appwire.ThreadClearResponse{}, err
		}
		return source.ClearThread(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadCompactStart, func(ctx context.Context, params appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, compactThreadWithResume(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadShutdown, func(ctx context.Context, params appwire.ThreadShutdownParams) (appwire.EmptyResponse, error) {
		source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, "")
		if err != nil {
			return appwire.EmptyResponse{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "shutdown"); err != nil {
			return appwire.EmptyResponse{}, err
		}
		return appwire.EmptyResponse{}, source.ShutdownThread(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadModelSet, func(ctx context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
		source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, "")
		if err != nil {
			return appwire.EmptyResponse{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "model"); err != nil {
			return appwire.EmptyResponse{}, err
		}
		return appwire.EmptyResponse{}, source.SetThreadModel(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfAuthStatus, func(_ context.Context, params appwire.AuthStatusParams) (appwire.AuthStatusResponse, error) {
		return authController.Status(params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfAuthLoginStart, func(_ context.Context, params appwire.AuthLoginStartParams) (appwire.AuthLoginStartResponse, error) {
		return authController.LoginStart(params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfAuthLoginComplete, func(ctx context.Context, params appwire.AuthLoginCompleteParams) (appwire.AuthLoginCompleteResponse, error) {
		resp, err := authController.LoginComplete(ctx, params)
		if err == nil {
			notifyAuthUpdated(server, resp.Status.Provider, resp.Status.ActiveSource)
		}
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfAuthLogout, func(ctx context.Context, params appwire.AuthLogoutParams) (appwire.AuthLogoutResponse, error) {
		resp, err := authController.Logout(params)
		if err == nil {
			notifyAuthUpdated(server, resp.Status.Provider, resp.Status.ActiveSource)
		}
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfAuthList, func(_ context.Context, params appwire.EmptyParams) (appwire.AuthListResponse, error) {
		return authController.List(params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfAuthApiKeySet, func(ctx context.Context, params appwire.AuthApiKeySetParams) (appwire.AuthStatusResponse, error) {
		resp, err := authController.ApiKeySet(params)
		if err == nil {
			notifyAuthUpdated(server, resp.Provider, resp.ActiveSource)
		}
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfAuthDeviceStart, func(ctx context.Context, params appwire.AuthDeviceStartParams) (appwire.AuthDeviceStartResponse, error) {
		return authController.DeviceStart(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfAuthDevicePoll, func(ctx context.Context, params appwire.AuthDevicePollParams) (appwire.AuthDevicePollResponse, error) {
		resp, err := authController.DevicePoll(ctx, params)
		if err == nil && resp.State == "authorized" {
			notifyAuthUpdated(server, resp.Status.Provider, resp.Status.ActiveSource)
		}
		return resp, err
	})
	if instancesController != nil {
		appserver.HandleTyped(server.Router(), appwire.MethodSerfInstanceList, func(_ context.Context, _ appwire.EmptyParams) (appwire.InstanceListResponse, error) {
			return instancesController.List(), nil
		})
		appserver.HandleTyped(server.Router(), appwire.MethodSerfInstanceCreate, func(_ context.Context, params appwire.InstanceCreateParams) (appwire.InstanceListResponse, error) {
			if err := instancesController.Create(params); err != nil {
				return appwire.InstanceListResponse{}, err
			}
			return instancesController.List(), nil
		})
		appserver.HandleTyped(server.Router(), appwire.MethodSerfInstanceEdit, func(_ context.Context, params appwire.InstanceEditParams) (appwire.InstanceListResponse, error) {
			if err := instancesController.Edit(params); err != nil {
				return appwire.InstanceListResponse{}, err
			}
			return instancesController.List(), nil
		})
		appserver.HandleTyped(server.Router(), appwire.MethodSerfInstanceRemove, func(_ context.Context, params appwire.InstanceRemoveParams) (appwire.InstanceListResponse, error) {
			if err := instancesController.Remove(params); err != nil {
				return appwire.InstanceListResponse{}, err
			}
			return instancesController.List(), nil
		})
		appserver.HandleTyped(server.Router(), appwire.MethodSerfInstanceSetDefault, func(_ context.Context, params appwire.InstanceSetDefaultParams) (appwire.InstanceListResponse, error) {
			if err := instancesController.SetDefault(params); err != nil {
				return appwire.InstanceListResponse{}, err
			}
			return instancesController.List(), nil
		})
	}
	launchController := newHubLaunchController(hubStateRoot)
	appserver.HandleTyped(server.Router(), appwire.MethodSerfLaunchResolve, func(ctx context.Context, params appwire.LaunchConfigResolveParams) (appwire.LaunchConfigResolved, error) {
		return launchController.Resolve(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfLaunchSchema, func(ctx context.Context, params appwire.EmptyParams) (appwire.LaunchOptionSchemaResponse, error) {
		return launchController.Schema(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfLaunchGetLayer, func(ctx context.Context, params appwire.LaunchConfigGetLayerParams) (appwire.LaunchConfigLayer, error) {
		return launchController.GetLayer(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfLaunchSetLayer, func(ctx context.Context, params appwire.LaunchConfigSetLayerParams) (appwire.LaunchConfigResolved, error) {
		resp, err := launchController.SetLayer(ctx, params)
		if err == nil {
			notifyLaunchUpdated(server, params.CWD, params.Layer)
		}
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfLaunchTrustRepo, func(ctx context.Context, params appwire.LaunchConfigTrustRepoParams) (appwire.LaunchConfigResolved, error) {
		resp, err := launchController.TrustRepo(ctx, params)
		if err == nil {
			notifyLaunchUpdated(server, params.CWD, "repo")
		}
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodModelList, func(ctx context.Context, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return hubModelList(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfTasksList, func(ctx context.Context, params appwire.TaskListParams) (appwire.TaskListResponse, error) {
		source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, "")
		if err != nil {
			return appwire.TaskListResponse{}, err
		}
		return source.ListTasks(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfThreadTranscriptsList, func(ctx context.Context, params appwire.ThreadTranscriptListParams) (appwire.ThreadTranscriptListResponse, error) {
		return hubThreadTranscriptList(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfDirsComplete, func(_ context.Context, params appwire.DirsCompleteParams) (appwire.DirsCompleteResponse, error) {
		return fspaths.CompleteDirs(params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfPathValidate, func(_ context.Context, params appwire.PathValidateParams) (appwire.PathValidateResponse, error) {
		return fspaths.ValidateLaunchPath(params), nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfHarnessesList, func(context.Context, appwire.HarnessListParams) (appwire.HarnessListResponse, error) {
		return appwire.HarnessListResponse{Data: launchHarnessDescriptors(cfg)}, nil
	})
	return server
}

// notifyAuthUpdated broadcasts a serf/auth/updated notification to all connected clients.
func notifyAuthUpdated(server *appserver.Server, provider, activeSource string) {
	server.BroadcastAll(appwire.NotifySerfAuthUpdated, map[string]string{
		"provider":     provider,
		"activeSource": activeSource,
	})
}

// notifyLaunchUpdated broadcasts a serf/launch/updated notification to all connected clients.
func notifyLaunchUpdated(server *appserver.Server, cwd, layer string) {
	server.BroadcastAll(appwire.NotifySerfLaunchUpdated, map[string]string{
		"cwd":   cwd,
		"layer": layer,
	})
}
