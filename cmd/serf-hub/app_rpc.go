package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appsource"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/diagnostic"
	"primeradiant.com/serf/internal/launchconfig"
	"primeradiant.com/serf/rendezvous"
)

func newHubSourceRegistry(cfg WebConfig) *appsource.Registry {
	registry := appsource.NewRegistry()
	registry.Add(appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		if cfg.Roster != nil {
			live := cfg.Roster.List()
			entries := make([]appsource.LocalDaemonEntry, 0, len(live))
			for _, item := range live {
				if strings.EqualFold(item.Status, "CLOSED") {
					continue
				}
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

var hubRelayIdleExitHook func(threadID string)
var hubRelayAfterIdleDeleteHook func(threadID string)

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

func newHubAppServer(cfg WebConfig, sources *appsource.Registry) *appserver.Server {
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
						if hubRelayIdleExitHook != nil {
							hubRelayIdleExitHook(threadID)
						}
						relayMu.Lock()
						if server.SubscriberCount(relayKey) == 0 {
							if relayedThreads[relayKey] == relayHandle {
								delete(relayedThreads, relayKey)
							}
							relayMu.Unlock()
							if hubRelayAfterIdleDeleteHook != nil {
								hubRelayAfterIdleDeleteHook(threadID)
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
		source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, "")
		if err != nil {
			return appwire.EmptyResponse{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "compact"); err != nil {
			return appwire.EmptyResponse{}, err
		}
		return appwire.EmptyResponse{}, source.CompactThread(ctx, params)
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
	launchController := newHubLaunchController(hubStateRoot)
	appserver.HandleTyped(server.Router(), appwire.MethodSerfLaunchResolve, func(ctx context.Context, params appwire.LaunchConfigResolveParams) (appwire.LaunchConfigResolved, error) {
		return launchController.Resolve(ctx, params)
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
		return completeDirs(params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodSerfHarnessesList, func(context.Context, appwire.HarnessListParams) (appwire.HarnessListResponse, error) {
		return appwire.HarnessListResponse{Data: launchHarnessDescriptors(cfg)}, nil
	})
	return server
}

func hubModelList(ctx context.Context, cfg WebConfig, sources *appsource.Registry, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
	harness := strings.TrimSpace(params.Harness)
	if harness != "" && harness != "serf" && harness != "local" {
		source, err := sourceForModelHarness(ctx, cfg, sources, harness)
		if err != nil {
			return appwire.ModelListResponse{}, err
		}
		sourceParams := params
		sourceParams.Harness = ""
		resp, err := source.ListModels(ctx, sourceParams)
		if err != nil {
			return appwire.ModelListResponse{}, err
		}
		resp.Data = sanitizeModelDescriptors(resp.Data)
		resp.Diagnostics = sanitizeModelDiagnostics(resp.Diagnostics)
		return resp, nil
	}

	launchResp, err := serfLaunchModelList(ctx, cfg, params.CWD)
	if hasSerfLaunchModelLister(cfg) {
		if err != nil {
			return appwire.ModelListResponse{}, err
		}
		return launchResp, nil
	}
	source, ok := sources.Source("local")
	if ok {
		resp, err := source.ListModels(ctx, params)
		if err == nil && len(resp.Data) > 0 {
			return resp, nil
		}
	}
	return appwire.ModelListResponse{}, nil
}

func hubThreadTranscriptList(ctx context.Context, cfg WebConfig, sources *appsource.Registry, params appwire.ThreadTranscriptListParams) (appwire.ThreadTranscriptListResponse, error) {
	root, err := hubTranscriptRoot(ctx, cfg, sources, params.Ref)
	if err != nil {
		return appwire.ThreadTranscriptListResponse{}, err
	}
	rootRef := threadRef(root)
	if rootRef == "" {
		rootRef = strings.TrimSpace(params.Ref)
	}
	if rootRef == "" {
		return appwire.ThreadTranscriptListResponse{}, appwire.InvalidParams("thread ref is required")
	}

	targets := []appwire.ThreadTranscriptTarget{{
		Ref:      rootRef,
		ThreadID: firstNonEmpty(root.ID, root.SessionID),
		Title:    "main session (live)",
		Kind:     "main",
		Status:   root.Status.Type,
		Source:   transcriptTargetSource(rootRef, root.Source),
	}}
	seen := map[string]struct{}{rootRef: {}}
	addTarget := func(thread appwire.Thread, turnsUsed int) {
		if thread.Serf.Kind != "subagent" || thread.Serf.ParentRef != rootRef {
			return
		}
		ref := threadRef(thread)
		if ref == "" {
			return
		}
		if _, ok := seen[ref]; ok {
			return
		}
		seen[ref] = struct{}{}
		title := firstNonEmpty(thread.Name, thread.Preview, thread.AgentNickname, "subagent "+firstNonEmpty(thread.ID, thread.SessionID, ref))
		targets = append(targets, appwire.ThreadTranscriptTarget{
			Ref:       ref,
			ThreadID:  firstNonEmpty(thread.ID, thread.SessionID),
			Title:     title,
			Kind:      "subagent",
			Status:    thread.Status.Type,
			Source:    transcriptTargetSource(ref, thread.Source),
			TurnsUsed: turnsUsed,
		})
	}

	if sources != nil {
		for _, source := range sources.All() {
			resp, err := source.ListThreads(ctx, appwire.ThreadListParams{IncludeSubagents: true})
			if err != nil {
				continue
			}
			for _, thread := range resp.Data {
				if thread.Source == "" {
					thread.Source = source.ID()
				}
				if thread.Serf.Ref == "" {
					threadID := firstNonEmpty(thread.ID, thread.SessionID)
					if threadID != "" {
						thread.Serf.Ref = appwire.Ref{SourceID: source.ID(), ThreadID: threadID}.String()
					}
				}
				addTarget(thread, len(thread.Turns))
			}
		}
	}
	if cfg.Past != nil {
		_ = cfg.Past.Rebuild()
		for _, entry := range cfg.Past.All() {
			addTarget(pastEntryThread(entry, false), entry.Meta.TurnCount)
		}
	}

	return appwire.ThreadTranscriptListResponse{Data: targets}, nil
}

func hubTranscriptRoot(ctx context.Context, cfg WebConfig, sources *appsource.Registry, ref string) (appwire.Thread, error) {
	source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, ref, "")
	if err == nil {
		resp, readErr := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: false})
		if readErr == nil {
			return resp.Thread, nil
		}
		err = readErr
	}
	if thread, ok := pastThreadForRead(cfg, appwire.ThreadReadParams{Ref: ref, IncludeTurns: false}); ok {
		return thread, nil
	}
	return appwire.Thread{}, err
}

func threadRef(thread appwire.Thread) string {
	if strings.TrimSpace(thread.Serf.Ref) != "" {
		return thread.Serf.Ref
	}
	sourceID := strings.TrimSpace(thread.Source)
	threadID := firstNonEmpty(thread.ID, thread.SessionID)
	if sourceID == "" || threadID == "" {
		return ""
	}
	return appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
}

func transcriptTargetSource(refText, fallback string) string {
	if ref, err := appwire.ParseRef(refText); err == nil && ref.SourceID != "" {
		return ref.SourceID
	}
	return fallback
}

func sourceForModelHarness(ctx context.Context, cfg WebConfig, sources *appsource.Registry, harness string) (appsource.Source, error) {
	if cfg.CodexLauncher != nil && cfg.CodexLauncher.Manages(harness) {
		return cfg.CodexLauncher.EnsureSource(ctx, harness, sources)
	}
	source, ok := sources.Source(harness)
	if !ok {
		return nil, appwire.Unavailable("model list source is not available: " + harness)
	}
	return source, nil
}

func shouldResumeAfterTurnStartError(err error) bool {
	return isSessionUnavailableError(err)
}

func isSessionUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return false
	}
	return wire.Code == appwire.CodeUnavailable && serfErrorInfoFromData(wire.Data) == string(appwire.ErrorSessionUnavailable)
}

func ensureThreadActionAvailable(ctx context.Context, source appsource.Source, ref, threadID, action string) error {
	resp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref, ThreadID: threadID, IncludeTurns: false})
	if err != nil {
		return err
	}
	if threadActionAvailable(resp.Thread.Serf.Capabilities, action) {
		return nil
	}
	return appwire.Unavailable(action + " is not available for this session")
}

func threadActionAvailable(caps appwire.ThreadCapabilities, action string) bool {
	switch action {
	case "send":
		return caps.Send
	case "steer":
		return caps.Steer
	case "interrupt":
		return caps.Interrupt
	case "compact":
		return caps.Compact
	case "clear":
		return caps.Clear
	case "fork":
		return caps.ForkFromTurn
	case "shutdown":
		return caps.Shutdown
	case "model":
		return caps.ChangeModel
	case "queue":
		return caps.Queue
	default:
		return false
	}
}

func hubThreadList(ctx context.Context, cfg WebConfig, sources *appsource.Registry, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	var threads []appwire.Thread
	liveIDs := map[string]struct{}{}
	if err := ensureManagedCodexSources(ctx, cfg, sources, params); err != nil {
		return appwire.ThreadListResponse{}, err
	}
	for _, source := range sources.All() {
		if !sourceAllowedForList(source.ID(), params) {
			continue
		}
		resp, err := source.ListThreads(ctx, params)
		if err != nil {
			if sourceExplicitlyRequestedForList(source.ID(), params) {
				return appwire.ThreadListResponse{}, err
			}
			continue
		}
		for _, thread := range resp.Data {
			sourceID := threadListSourceID(source.ID(), thread)
			for _, id := range []string{thread.ID, thread.SessionID} {
				if key := threadListSourceKey(sourceID, id); key != "" {
					liveIDs[key] = struct{}{}
				}
			}
			thread = mergePastMetadataForList(cfg, source.ID(), thread)
			thread = sanitizeStaleProcessingStatus(cfg, thread)
			if appThreadMatches(thread, params) {
				threads = append(threads, thread)
			}
		}
	}
	if cfg.Past != nil {
		limit := params.Limit
		if limit <= 0 {
			limit = 100
		}
		for _, entry := range cfg.Past.Search(params.SearchTerm, limit, 0) {
			if _, ok := liveIDs[threadListSourceKey("local", entry.ID)]; ok {
				continue
			}
			thread := pastEntryThread(entry, false)
			if appThreadMatches(thread, params) {
				threads = append(threads, thread)
			}
		}
	}
	sort.SliceStable(threads, func(i, j int) bool {
		return appwireThreadLess(threads[i], threads[j])
	})
	if params.Limit > 0 && len(threads) > params.Limit {
		threads = threads[:params.Limit]
	}
	return appwire.ThreadListResponse{Data: threads}, nil
}

func ensureManagedCodexSources(ctx context.Context, cfg WebConfig, sources *appsource.Registry, params appwire.ThreadListParams) error {
	if cfg.CodexLauncher == nil || sources == nil {
		return nil
	}
	for _, launch := range cfg.CodexLaunches {
		sourceID := strings.TrimSpace(launch.ID)
		if sourceID == "" {
			sourceID = "codex"
		}
		if !sourceAllowedForList(sourceID, params) {
			continue
		}
		if _, err := cfg.CodexLauncher.EnsureSource(ctx, sourceID, sources); err != nil && sourceExplicitlyRequestedForList(sourceID, params) {
			return err
		}
	}
	return nil
}

func threadListSourceID(defaultSourceID string, thread appwire.Thread) string {
	if thread.Source != "" {
		return thread.Source
	}
	if ref, err := appwire.ParseRef(thread.Serf.Ref); err == nil && ref.SourceID != "" {
		return ref.SourceID
	}
	return defaultSourceID
}

func threadListSourceKey(sourceID, threadID string) string {
	return appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
}

func sourceAllowedForList(sourceID string, params appwire.ThreadListParams) bool {
	if len(params.SourceIDs) == 0 {
		return true
	}
	for _, want := range params.SourceIDs {
		if want == sourceID {
			return true
		}
	}
	return false
}

func sourceExplicitlyRequestedForList(sourceID string, params appwire.ThreadListParams) bool {
	for _, requested := range params.SourceIDs {
		if requested == sourceID {
			return true
		}
	}
	return false
}

func mergePastMetadataForList(cfg WebConfig, sourceID string, live appwire.Thread) appwire.Thread {
	if cfg.Past == nil {
		return live
	}
	if threadListSourceID(sourceID, live) != "local" {
		return live
	}
	var entry PastEntry
	var ok bool
	for _, id := range []string{live.ID, live.SessionID} {
		if id == "" {
			continue
		}
		entry, ok = cfg.Past.Find(id)
		if ok {
			break
		}
	}
	if !ok {
		return live
	}
	past := pastEntryThread(entry, false)
	if live.ID == "" {
		live.ID = past.ID
	}
	if live.SessionID == "" {
		live.SessionID = past.SessionID
	}
	if live.Preview == "" || live.Preview == live.ID || live.Preview == live.SessionID {
		live.Preview = past.Preview
	}
	if live.Name == "" {
		live.Name = past.Name
	}
	if live.ModelProvider == "" {
		live.ModelProvider = past.ModelProvider
	}
	if past.CreatedAt != 0 {
		live.CreatedAt = past.CreatedAt
	}
	if past.UpdatedAt != 0 {
		live.UpdatedAt = past.UpdatedAt
	}
	if live.Path == "" || live.Path == "." {
		live.Path = past.Path
	}
	if live.CWD == "" {
		live.CWD = past.CWD
	}
	if live.Source == "" {
		live.Source = past.Source
	}
	if live.Serf.Ref == "" {
		live.Serf.Ref = past.Serf.Ref
	}
	if live.Serf.Profile == "" {
		live.Serf.Profile = past.Serf.Profile
	}
	return live
}

func appThreadMatches(thread appwire.Thread, params appwire.ThreadListParams) bool {
	if len(params.Statuses) > 0 {
		status := strings.ToLower(thread.Status.Type)
		found := false
		for _, want := range params.Statuses {
			if strings.EqualFold(normalizeThreadListStatusFilter(want), status) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(params.SourceIDs) > 0 {
		found := false
		for _, sourceID := range params.SourceIDs {
			if sourceID == thread.Source {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	q := strings.ToLower(strings.TrimSpace(params.SearchTerm))
	if q == "" {
		return true
	}
	haystack := strings.ToLower(strings.Join([]string{
		thread.ID,
		thread.SessionID,
		thread.Name,
		thread.Preview,
		thread.CWD,
		thread.Path,
		thread.ModelProvider,
		thread.Serf.Profile,
	}, " "))
	return strings.Contains(haystack, q)
}

func normalizeThreadListStatusFilter(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active":
		return appwire.ThreadStatusActive
	case "notloaded":
		return appwire.ThreadStatusNotLoaded
	case "systemerror":
		return appwire.ThreadStatusSystemError
	default:
		return strings.ToLower(strings.TrimSpace(status))
	}
}

// sanitizeStaleProcessingStatus rewrites a thread's status when the live source
// claims the session is processing but the on-disk transcript shows the last
// thing recorded was a failed LLM call. The agent loop has a known gap where a
// retryable stream error (e.g. "stream ended without finish event") returns
// from session.Input without flipping the in-memory state back to IDLE; the
// daemon then keeps answering /status with PROCESSING forever, even though
// nothing is in flight. Hub readers conclude "processing" and the workspace UI
// disables steer/send. We catch the discrepancy here by consulting the past
// index for the same thread: if the last transcript line is an api_call whose
// Error field is non-empty, the session is wedged and the correct status is
// error (kata r6y9). All other tail shapes — completed assistant turns, bare
// USER_INPUT entries with no api_call yet, successful api_calls mid-round — are
// left alone because the daemon may legitimately still be processing them.
func sanitizeStaleProcessingStatus(cfg WebConfig, thread appwire.Thread) appwire.Thread {
	if cfg.Past == nil {
		return thread
	}
	if thread.Status.Type != appwire.ThreadStatusActive {
		return thread
	}
	threadID := firstNonEmpty(thread.ID, thread.SessionID)
	if threadID == "" {
		return thread
	}
	if thread.Serf.Ref != "" {
		ref, err := appwire.ParseRef(thread.Serf.Ref)
		if err == nil && ref.SourceID != "" && ref.SourceID != "local" {
			return thread
		}
	} else if thread.Source != "" && thread.Source != "local" {
		return thread
	}
	entry, ok := cfg.Past.Find(threadID)
	if !ok {
		return thread
	}
	transcriptPath := filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")
	tailKind, tailHasError := transcriptTailSummary(transcriptPath)
	if tailKind == "api_call" && tailHasError {
		thread.Status = appwire.ThreadStatus{Type: appwire.ThreadStatusSystemError}
	}
	return thread
}

// transcriptTailSummary returns the kind ("entry" | "api_call" | "") of the
// final non-empty line of the transcript at path, and whether that api_call
// recorded a non-empty Error field. Returns ("", false) on read failures so the
// caller leaves the thread status unchanged when it cannot inspect the tail.
func transcriptTailSummary(path string) (kind string, hasError bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), transcriptJSONLMaxLineBytes)
	var lastLine []byte
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		lastLine = append(lastLine[:0], line...)
	}
	if scanner.Err() != nil || len(lastLine) == 0 {
		return "", false
	}
	var head struct {
		Kind  string `json:"kind"`
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(lastLine, &head); err != nil {
		return "", false
	}
	return head.Kind, strings.TrimSpace(head.Error) != ""
}

func pastThreadForRead(cfg WebConfig, params appwire.ThreadReadParams) (appwire.Thread, bool) {
	if cfg.Past == nil {
		return appwire.Thread{}, false
	}
	threadID, ok := localPastThreadID(params)
	if !ok {
		return appwire.Thread{}, false
	}
	entry, ok := cfg.Past.Find(threadID)
	if !ok {
		return appwire.Thread{}, false
	}
	return pastEntryThread(entry, params.IncludeTurns), true
}

func localPastThreadID(params appwire.ThreadReadParams) (string, bool) {
	if params.Ref != "" {
		ref, err := appwire.ParseRef(params.Ref)
		if err != nil {
			return "", false
		}
		if ref.SourceID != "local" {
			return "", false
		}
		return ref.ThreadID, true
	}
	threadID := strings.TrimSpace(params.ThreadID)
	if threadID == "" {
		return "", false
	}
	return threadID, true
}

func liveThreadCanMergeLocalPast(live appwire.Thread) bool {
	if live.Serf.Ref != "" {
		ref, err := appwire.ParseRef(live.Serf.Ref)
		return err == nil && ref.SourceID == "local"
	}
	if live.Source != "" {
		return live.Source == "local"
	}
	return true
}

func mergePastThreadForRead(cfg WebConfig, params appwire.ThreadReadParams, live appwire.Thread) appwire.Thread {
	if !liveThreadCanMergeLocalPast(live) {
		return live
	}
	if params.ThreadID == "" && params.Ref == "" {
		switch {
		case live.Serf.Ref != "":
			params.Ref = live.Serf.Ref
		case live.ID != "":
			params.Ref = appwire.Ref{SourceID: "local", ThreadID: live.ID}.String()
		case live.SessionID != "":
			params.Ref = appwire.Ref{SourceID: "local", ThreadID: live.SessionID}.String()
		}
	}
	past, ok := pastThreadForRead(cfg, params)
	if !ok {
		return live
	}
	if live.ID == "" {
		live.ID = past.ID
	}
	if live.SessionID == "" {
		live.SessionID = past.SessionID
	}
	if live.Preview == "" || live.Preview == live.ID || live.Preview == live.SessionID {
		live.Preview = past.Preview
	}
	if live.Name == "" {
		live.Name = past.Name
	}
	if live.ModelProvider == "" {
		live.ModelProvider = past.ModelProvider
	}
	if live.CreatedAt == 0 {
		live.CreatedAt = past.CreatedAt
	}
	if live.UpdatedAt == 0 {
		live.UpdatedAt = past.UpdatedAt
	}
	if live.Path == "" {
		live.Path = past.Path
	}
	if live.CWD == "" {
		live.CWD = past.CWD
	}
	if live.Source == "" {
		live.Source = past.Source
	}
	if live.Serf.Ref == "" {
		live.Serf.Ref = past.Serf.Ref
	}
	if live.Serf.Profile == "" {
		live.Serf.Profile = past.Serf.Profile
	}
	if params.IncludeTurns && len(live.Turns) == 0 {
		live.Turns = past.Turns
	}
	return live
}

func pastEntryThread(entry PastEntry, includeTurns bool) appwire.Thread {
	title := entry.Meta.OriginalPrompt
	if title == "" {
		title = entry.Meta.ID
	}
	cwd := entry.Meta.EnvInfo.WorkingDir
	ref := appwire.Ref{SourceID: "local", ThreadID: entry.Meta.ID}.String()
	parentRef := ""
	if entry.Meta.ParentSessionID != "" {
		parentRef = appwire.Ref{SourceID: "local", ThreadID: entry.Meta.ParentSessionID}.String()
	}
	kind := "session"
	if entry.Meta.IsSubagent {
		kind = "subagent"
	} else if entry.Meta.ParentSessionID != "" {
		kind = "fork"
	}
	createdAt := orderCreatedAt(entry.Meta.CreatedAt, entry.Meta.UpdatedAt)
	updatedAt := orderUpdatedAt(entry.Meta.UpdatedAt, entry.Meta.CreatedAt)
	thread := appwire.Thread{
		ID:            entry.Meta.ID,
		SessionID:     entry.Meta.ID,
		Preview:       title,
		Name:          title,
		ModelProvider: entry.Meta.Model,
		CreatedAt:     unixSeconds(createdAt),
		UpdatedAt:     unixSeconds(updatedAt),
		Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusNotLoaded},
		Path:          filepath.Base(cwd),
		CWD:           cwd,
		Source:        "local",
		Serf: appwire.SerfThread{
			Ref:       ref,
			ParentRef: parentRef,
			Kind:      kind,
			Profile:   entry.Meta.ProfileID,
			Capabilities: appwire.ThreadCapabilities{
				Send:         true,
				ForkFromTurn: true,
			},
		},
	}
	if includeTurns {
		thread.Turns = pastEntryTurns(entry)
	}
	return thread
}

func pastEntryTurns(entry PastEntry) []appwire.Turn {
	transcriptPath := filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")
	f, err := os.Open(transcriptPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), transcriptJSONLMaxLineBytes)
	toolNames := map[string]string{}
	var turns []appwire.Turn
	entryIndex := 0
	for scanner.Scan() {
		raw := scanner.Bytes()
		var head struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(raw, &head); err != nil || head.Kind != "entry" {
			if head.Kind == "api_call" {
				var call agent.TranscriptAPICall
				if err := json.Unmarshal(raw, &call); err == nil && strings.TrimSpace(call.Error) != "" {
					info := diagnostic.FromFields(call.Source, call.Title, call.Hint, call.Error)
					entryIndex++
					turns = append(turns, appwire.Turn{
						ID:        fmt.Sprintf("turn_%d", entryIndex),
						ItemsView: "full",
						Status:    appwire.TurnStatusFailed,
						Error: &appwire.TurnError{
							Message: call.Error,
							Source:  string(info.Source),
							Title:   info.Title,
							Hint:    info.Hint,
						},
					})
				}
			}
			continue
		}
		var entryRec replayEntry
		if err := json.Unmarshal(raw, &entryRec); err != nil {
			continue
		}
		entryIndex++
		turnID := fmt.Sprintf("turn_%d", entryIndex)
		items := appItemsFromReplayTurn(turnID, entryIndex, entryRec.Turn, toolNames)
		if len(items) == 0 {
			continue
		}
		turns = append(turns, appwire.Turn{ID: turnID, Items: items, ItemsView: "full", Status: appwire.TurnStatusCompleted})
	}
	return turns
}

func appItemsFromReplayTurn(turnID string, turnIndex int, turn replayTurn, toolNames map[string]string) []appwire.ThreadItem {
	switch turn.Kind {
	case "USER_INPUT":
		images := appInputImagesFromReplayContent(turn.Message.Content)
		item := appwire.ThreadItem{
			Type:                 "userMessage",
			ID:                   fmt.Sprintf("item_user_%d", turnIndex),
			TurnID:               turnID,
			TranscriptEntryIndex: turnIndex,
			Text:                 joinText(turn.Message.Content),
			Images:               images,
			Status:               "completed",
		}
		return []appwire.ThreadItem{item}
	case "STEERING":
		images := appInputImagesFromReplayContent(turn.Message.Content)
		text := joinText(turn.Message.Content)
		if text == "" && len(images) > 0 {
			text = appImagePlaceholder(len(images))
		}
		return []appwire.ThreadItem{{
			Type:   "steering",
			ID:     fmt.Sprintf("item_steering_%d", turnIndex),
			TurnID: turnID,
			Text:   text,
			Images: images,
			Status: "completed",
		}}
	case "ASSISTANT":
		var items []appwire.ThreadItem
		for i, part := range turn.Message.Content {
			switch part.Kind {
			case "text":
				if part.Text != "" {
					items = append(items, appwire.ThreadItem{
						Type:   "agentMessage",
						ID:     fmt.Sprintf("item_assistant_%d_%d", turnIndex, i),
						TurnID: turnID,
						Text:   part.Text,
						Status: "completed",
					})
				}
			case "commandExecution":
				if part.ToolCall != nil {
					toolNames[part.ToolCall.ID] = part.ToolCall.Name
					if part.ToolCall.Name == "communicate" {
						if text := communicateMessageFromArguments(part.ToolCall.Arguments); text != "" {
							items = append(items, appwire.ThreadItem{
								Type:   "agentMessage",
								ID:     fmt.Sprintf("item_assistant_%d_%d", turnIndex, i),
								TurnID: turnID,
								Text:   text,
								Status: "completed",
							})
						}
						continue
					}
					items = append(items, appwire.ThreadItem{
						Type:          "commandExecution",
						ID:            fmt.Sprintf("item_tool_%d_%d", turnIndex, i),
						TurnID:        turnID,
						ToolName:      part.ToolCall.Name,
						CallID:        part.ToolCall.ID,
						ArgumentsJSON: string(part.ToolCall.Arguments),
						Status:        appwire.TurnStatusInProgress,
					})
				}
			}
		}
		return items
	case "TOOL", "TOOL_RESULTS":
		var items []appwire.ThreadItem
		for i, part := range turn.Message.Content {
			if part.Kind != "tool_result" || part.ToolResult == nil {
				continue
			}
			name := part.ToolResult.Name
			if name == "" {
				name = toolNames[part.ToolResult.ToolCallID]
			}
			if name == "communicate" {
				delete(toolNames, part.ToolResult.ToolCallID)
				continue
			}
			item := appwire.ThreadItem{
				Type:     "commandExecution",
				ID:       fmt.Sprintf("item_tool_result_%d_%d", turnIndex, i),
				TurnID:   turnID,
				ToolName: name,
				CallID:   part.ToolResult.ToolCallID,
				Status:   "completed",
			}
			if part.ToolResult.IsError {
				item.Error = stringifyToolContent(part.ToolResult.Content)
			} else {
				item.Output = stringifyToolContent(part.ToolResult.Content)
			}
			items = append(items, item)
		}
		return items
	default:
		return nil
	}
}

func appInputImagesFromReplayContent(parts []replayPart) []appwire.InputItem {
	var images []appwire.InputItem
	for _, part := range parts {
		if part.Kind != "image" || part.Image == nil || len(part.Image.Data) == 0 {
			continue
		}
		images = append(images, appwire.InputItem{
			Type:      "input_image",
			MediaType: part.Image.MediaType,
			Name:      part.Image.Name,
			Metadata: map[string]string{
				"sha":  imageSha(part.Image.Data),
				"size": strconv.Itoa(len(part.Image.Data)),
			},
		})
	}
	return images
}

func appImagePlaceholder(count int) string {
	switch count {
	case 0:
		return ""
	case 1:
		return "[image]"
	default:
		return fmt.Sprintf("[%d images]", count)
	}
}

func sourceForThread(sources *appsource.Registry, ref, threadID string) (appsource.Source, error) {
	if ref != "" {
		return sources.SourceForRef(ref)
	}
	source, ok := sources.Source("local")
	if !ok {
		return nil, fmt.Errorf("source not found: local")
	}
	if threadID == "" {
		return source, nil
	}
	return source, nil
}

func sourceForThreadWithManagedLaunch(ctx context.Context, cfg WebConfig, sources *appsource.Registry, ref, threadID string) (appsource.Source, error) {
	if sourceID, ok := managedLaunchSourceIDForRef(cfg, ref); ok {
		return cfg.CodexLauncher.EnsureSource(ctx, sourceID, sources)
	}
	return sourceForThread(sources, ref, threadID)
}

func managedLaunchSourceIDForRef(cfg WebConfig, ref string) (string, bool) {
	if ref == "" || cfg.CodexLauncher == nil {
		return "", false
	}
	parsed, err := appwire.ParseRef(ref)
	if err != nil || parsed.SourceID == "local" || !cfg.CodexLauncher.Manages(parsed.SourceID) {
		return "", false
	}
	return parsed.SourceID, true
}

// hubKnowsRef reports whether the hub recognizes ref: either as a
// managed-launch source (e.g. codex) or as a thread tracked in the local past
// index. Used to gate auto-resume retries after a TurnStart failure so that
// non-local refs (which never appear in the local past index) still get the
// retry when their backing daemon dies.
func hubKnowsRef(cfg WebConfig, ref string) bool {
	if _, ok := managedLaunchSourceIDForRef(cfg, ref); ok {
		return true
	}
	_, ok := pastThreadForRead(cfg, appwire.ThreadReadParams{Ref: ref})
	return ok
}

func hubThreadStart(ctx context.Context, cfg WebConfig, sources *appsource.Registry, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	if err := validateAppWireInputItems(params.Input); err != nil {
		return appwire.ThreadStartResponse{}, appwire.InvalidParams(err.Error())
	}
	sourceID := launchSourceID(params)
	if sourceID != "" && sourceID != "local" {
		var source appsource.Source
		if cfg.CodexLauncher != nil && cfg.CodexLauncher.Manages(sourceID) {
			launched, err := cfg.CodexLauncher.EnsureSource(ctx, sourceID, sources)
			if err != nil {
				return appwire.ThreadStartResponse{}, err
			}
			source = launched
		} else {
			var ok bool
			source, ok = sources.Source(sourceID)
			if !ok {
				if cfg.CodexLauncher == nil {
					return appwire.ThreadStartResponse{}, fmt.Errorf("source not found: %s", sourceID)
				}
				launched, err := cfg.CodexLauncher.EnsureSource(ctx, sourceID, sources)
				if err != nil {
					return appwire.ThreadStartResponse{}, err
				}
				source = launched
			}
		}
		if source == nil {
			return appwire.ThreadStartResponse{}, fmt.Errorf("source not found: %s", sourceID)
		}
		return source.StartThread(ctx, params)
	}
	if cfg.Spawner == nil {
		return appwire.ThreadStartResponse{}, appwire.Unavailable("spawner not configured")
	}
	workingDir := params.CWD
	if workingDir != "" {
		resolved, err := canonicalizeDir(workingDir)
		if err != nil {
			return appwire.ThreadStartResponse{}, appwire.InvalidParams("cwd: " + err.Error())
		}
		workingDir = resolved
	}
	var overrides launchconfig.Layer
	if params.LaunchOverrides != nil {
		overrides = launchconfig.FromWire(*params.LaunchOverrides)
	}
	// Legacy scalar fields win over launchOverrides (per spec §5.4).
	if params.Model != "" {
		model := params.Model
		if params.ModelProvider != "" && !strings.HasPrefix(params.Model, params.ModelProvider+"/") {
			model = params.ModelProvider + "/" + params.Model
		}
		modelRef, err := cmdutil.ParseModelRef(model)
		if err != nil {
			return appwire.ThreadStartResponse{}, appwire.InvalidParams(err.Error())
		}
		overrides.Model = modelRef.Qualified()
	}
	if params.Profile != "" {
		overrides.Agent = params.Profile
	}
	if params.ReasoningEffort != "" {
		overrides.ReasoningEffort = params.ReasoningEffort
	}
	spawnResolved, resolveErr := launchconfig.Resolve(cfg.HubStateRoot, workingDir, overrides)
	if resolveErr != nil {
		return appwire.ThreadStartResponse{}, resolveErr
	}
	resolvedModel := strings.TrimSpace(spawnResolved.Effective.Model)
	if resolvedModel == "" {
		return appwire.ThreadStartResponse{}, appwire.InvalidParams("model is required")
	}
	modelRef, err := cmdutil.ParseModelRef(resolvedModel)
	if err != nil {
		return appwire.ThreadStartResponse{}, appwire.InvalidParams(err.Error())
	}
	if err := validateSerfLaunchModel(ctx, cfg, modelRef, workingDir); err != nil {
		return appwire.ThreadStartResponse{}, err
	}
	entry, err := cfg.Spawner.Spawn(ctx, SpawnRequest{
		Resolved:   spawnResolved,
		WorkingDir: workingDir,
		Provider:   modelRef.Provider,
	})
	if err != nil {
		return appwire.ThreadStartResponse{}, err
	}
	if cfg.Roster != nil {
		cfg.Roster.Refresh()
		if entry.ThreadID == "" || entry.SessionID == "" {
			for _, live := range cfg.Roster.List() {
				if live.PID == entry.PID {
					if entry.ThreadID == "" {
						entry.ThreadID = live.SessionID
					}
					if entry.SessionID == "" {
						entry.SessionID = live.SessionID
					}
					break
				}
			}
		}
	}
	ref := appwire.Ref{SourceID: "local", ThreadID: entry.ThreadID}.String()
	source, err := sourceForThread(sources, ref, "")
	if err != nil {
		if entry.ThreadID == "" {
			return appwire.ThreadStartResponse{}, err
		}
		return appwire.ThreadStartResponse{Thread: appwire.Thread{
			ID:            entry.ThreadID,
			SessionID:     entry.SessionID,
			Preview:       entry.SessionID,
			ModelProvider: modelRef.Provider,
			CWD:           workingDir,
			Source:        "local",
			Status:        appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Serf:          appwire.SerfThread{Ref: ref},
		}}, nil
	}
	threadResp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		threadResp.Thread = appwire.Thread{ID: entry.ThreadID, SessionID: entry.SessionID, Source: "local", Serf: appwire.SerfThread{Ref: ref}}
	}
	turn := appwire.Turn{}
	if len(params.Input) > 0 {
		turnResp, err := source.StartTurn(ctx, appwire.TurnStartParams{Ref: ref, Input: params.Input})
		if err != nil {
			return appwire.ThreadStartResponse{}, err
		}
		turn = turnResp.Turn
	}
	return appwire.ThreadStartResponse{Thread: threadResp.Thread, Turn: turn}, nil
}

func launchSourceID(params appwire.ThreadStartParams) string {
	harness := strings.TrimSpace(params.Harness)
	if harness != "" {
		if harness == "serf" {
			return "local"
		}
		return harness
	}
	return ""
}

func validateSerfLaunchModel(ctx context.Context, cfg WebConfig, ref cmdutil.ModelRef, workingDir string) error {
	contract, err := serfLaunchModelList(ctx, cfg, workingDir)
	if err != nil || (len(contract.Data) == 0 && len(contract.Diagnostics) == 0) {
		return nil
	}
	providerEnumerated := false
	for _, model := range contract.Data {
		if strings.EqualFold(strings.TrimSpace(model.Provider), ref.Provider) {
			providerEnumerated = true
			if strings.TrimSpace(model.Model) == ref.Model {
				return nil
			}
		}
	}
	if !providerEnumerated {
		if providerHasLaunchDiagnostic(contract.Diagnostics, ref.Provider) || launchProviderAllowsUnreportedModels(ref.Provider) {
			return nil
		}
		return appwire.HubLaunchError("model provider is not reported by the Serf launch harness: " + ref.Provider)
	}
	return appwire.HubLaunchError("model is not configured for Serf launch: " + ref.Qualified())
}

func launchProviderAllowsUnreportedModels(provider string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), "openrouter-anthropic")
}

func providerHasLaunchDiagnostic(diagnostics []appwire.ModelListDiagnostic, provider string) bool {
	for _, diag := range diagnostics {
		if strings.EqualFold(strings.TrimSpace(diag.Provider), provider) {
			return true
		}
	}
	return false
}

func serfLaunchModels(ctx context.Context, cfg WebConfig) ([]appwire.ModelDescriptor, error) {
	resp, err := serfLaunchModelList(ctx, cfg, "")
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func serfLaunchModelList(ctx context.Context, cfg WebConfig, workingDir string) (appwire.ModelListResponse, error) {
	if strings.TrimSpace(workingDir) != "" {
		if lister, ok := cfg.Spawner.(SerfLaunchModelContractWorkingDirLister); ok && lister != nil {
			resp, err := lister.ListLaunchModelContractForWorkingDir(ctx, workingDir)
			if err != nil {
				return appwire.ModelListResponse{}, err
			}
			resp.Data = sanitizeModelDescriptors(resp.Data)
			resp.Diagnostics = sanitizeModelDiagnostics(resp.Diagnostics)
			return resp, nil
		}
	}
	if lister, ok := cfg.Spawner.(SerfLaunchModelContractLister); ok && lister != nil {
		resp, err := lister.ListLaunchModelContract(ctx)
		if err != nil {
			return appwire.ModelListResponse{}, err
		}
		resp.Data = sanitizeModelDescriptors(resp.Data)
		resp.Diagnostics = sanitizeModelDiagnostics(resp.Diagnostics)
		return resp, nil
	}
	lister, ok := cfg.Spawner.(SerfLaunchModelLister)
	if !ok || lister == nil {
		return appwire.ModelListResponse{}, nil
	}
	models, err := lister.ListLaunchModels(ctx)
	if err != nil {
		return appwire.ModelListResponse{}, err
	}
	return appwire.ModelListResponse{Data: sanitizeModelDescriptors(models)}, nil
}

func hasSerfLaunchModelLister(cfg WebConfig) bool {
	if lister, ok := cfg.Spawner.(SerfLaunchModelContractWorkingDirLister); ok && lister != nil {
		return true
	}
	if lister, ok := cfg.Spawner.(SerfLaunchModelContractLister); ok && lister != nil {
		return true
	}
	if lister, ok := cfg.Spawner.(SerfLaunchModelLister); ok && lister != nil {
		return true
	}
	return false
}

func sanitizeModelDescriptors(models []appwire.ModelDescriptor) []appwire.ModelDescriptor {
	out := make([]appwire.ModelDescriptor, 0, len(models))
	for _, model := range models {
		provider := strings.TrimSpace(model.Provider)
		name := strings.TrimSpace(model.Model)
		if provider == "" || name == "" {
			continue
		}
		out = append(out, appwire.ModelDescriptor{Provider: provider, Model: name})
	}
	return out
}

func sanitizeModelDiagnostics(diagnostics []appwire.ModelListDiagnostic) []appwire.ModelListDiagnostic {
	out := make([]appwire.ModelListDiagnostic, 0, len(diagnostics))
	for _, diag := range diagnostics {
		diag.Provider = strings.TrimSpace(diag.Provider)
		diag.Source = strings.TrimSpace(diag.Source)
		diag.Title = strings.TrimSpace(diag.Title)
		diag.Message = strings.TrimSpace(diag.Message)
		diag.Hint = strings.TrimSpace(diag.Hint)
		if diag.Message == "" {
			continue
		}
		out = append(out, diag)
	}
	return out
}

func launchHarnessDescriptors(cfg WebConfig) []appwire.HarnessDescriptor {
	out := []appwire.HarnessDescriptor{{ID: "serf", Label: "serf", Kind: "serf"}}
	seen := map[string]bool{"serf": true}
	for _, source := range cfg.CodexSources {
		id := strings.TrimSpace(source.ID)
		if id == "" {
			id = "codex"
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, appwire.HarnessDescriptor{ID: id, Label: id, Kind: "codex"})
	}
	for _, launch := range cfg.CodexLaunches {
		id := strings.TrimSpace(launch.ID)
		if id == "" {
			id = "codex"
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, appwire.HarnessDescriptor{ID: id, Label: id, Kind: "codex"})
	}
	return out
}

func hubThreadResume(ctx context.Context, cfg WebConfig, sources *appsource.Registry, params appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	if params.Ref != "" {
		ref, err := appwire.ParseRef(params.Ref)
		if err != nil {
			return appwire.ThreadResumeResponse{}, err
		}
		if ref.SourceID != "local" {
			var source appsource.Source
			if cfg.CodexLauncher != nil && cfg.CodexLauncher.Manages(ref.SourceID) {
				launched, err := cfg.CodexLauncher.EnsureSource(ctx, ref.SourceID, sources)
				if err != nil {
					return appwire.ThreadResumeResponse{}, err
				}
				source = launched
			} else {
				var err error
				source, err = sourceForThread(sources, params.Ref, "")
				if err != nil {
					if cfg.CodexLauncher == nil {
						return appwire.ThreadResumeResponse{}, err
					}
					launched, launchErr := cfg.CodexLauncher.EnsureSource(ctx, ref.SourceID, sources)
					if launchErr != nil {
						return appwire.ThreadResumeResponse{}, launchErr
					}
					source = launched
				}
			}
			return source.ResumeThread(ctx, params)
		}
	}
	if cfg.Spawner == nil {
		return appwire.ThreadResumeResponse{}, appwire.Unavailable("spawner not configured")
	}
	sessionID := strings.TrimSpace(params.Session)
	if sessionID == "" && params.Ref != "" {
		ref, err := appwire.ParseRef(params.Ref)
		if err != nil {
			return appwire.ThreadResumeResponse{}, err
		}
		sessionID = ref.ThreadID
	}
	if sessionID == "" {
		return appwire.ThreadResumeResponse{}, appwire.InvalidParams("sessionId or ref is required")
	}
	entry, err := cfg.Spawner.Resume(ctx, resumeRequestForConfig(cfg, sessionID))
	if err != nil {
		return appwire.ThreadResumeResponse{}, err
	}
	if cfg.Roster != nil {
		cfg.Roster.Refresh()
	}
	threadID := entry.ThreadID
	if threadID == "" {
		threadID = entry.SessionID
	}
	ref := appwire.Ref{SourceID: "local", ThreadID: threadID}.String()
	source, err := sourceForThread(sources, ref, "")
	if err != nil {
		return appwire.ThreadResumeResponse{}, err
	}
	threadResp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		return appwire.ThreadResumeResponse{}, err
	}
	return appwire.ThreadResumeResponse{Thread: threadResp.Thread}, nil
}

func resumeRequestForConfig(cfg WebConfig, id string) ResumeRequest {
	req := ResumeRequest{SessionID: id}
	if cfg.Past != nil {
		if pe, ok := cfg.Past.Find(id); ok {
			req.WorkingDir = pe.Meta.EnvInfo.WorkingDir
			req.StateDir = pe.StateDir
			provider := resumeProviderFromProfileID(pe.Meta.ProfileID)
			if provider != "" && pe.Meta.Model != "" {
				req.Provider = provider
				req.Resolved = launchconfig.Resolved{Effective: launchconfig.Layer{
					Model: provider + "/" + pe.Meta.Model,
				}}
			}
		}
	}
	return req
}

func resumeProviderFromProfileID(profileID string) string {
	switch provider := strings.ToLower(strings.TrimSpace(profileID)); provider {
	case "openai", "anthropic", "google", "gemini", "minimax", "openrouter-anthropic", "kimi", "glm", "openrouter", "ollama":
		return provider
	default:
		return ""
	}
}

func hubThreadFork(ctx context.Context, cfg WebConfig, sources *appsource.Registry, params appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	ref, err := appwire.ParseRef(params.Ref)
	if err != nil {
		return appwire.ThreadForkResponse{}, err
	}
	if ref.SourceID != "local" {
		source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, "")
		if err != nil {
			return appwire.ThreadForkResponse{}, err
		}
		if threadForkRequiresTurnCapability(params) {
			if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "fork"); err != nil {
				return appwire.ThreadForkResponse{}, err
			}
		}
		return source.ForkThread(ctx, params)
	}
	turn, err := parseSourceTurnID(params.SourceTurnID)
	if err != nil {
		return appwire.ThreadForkResponse{}, appwire.InvalidParams(err.Error())
	}
	if strings.TrimSpace(params.EditedInput) == "" {
		return appwire.ThreadForkResponse{}, appwire.InvalidParams("editedInput is required")
	}
	stateDir := cfg.StateDir
	if cfg.Past != nil {
		if pe, ok := cfg.Past.Find(ref.ThreadID); ok {
			stateDir = pe.StateDir
		}
	}
	if stateDir == "" {
		return appwire.ThreadForkResponse{}, appwire.Unavailable("state dir not resolvable for parent thread")
	}
	childID, err := agent.ForkSession(stateDir, ref.ThreadID, turn, params.EditedInput, params.Label)
	if err != nil {
		return appwire.ThreadForkResponse{}, err
	}
	if cfg.Past != nil {
		_ = cfg.Past.Rebuild()
	}
	childRef := appwire.Ref{SourceID: "local", ThreadID: childID}.String()
	return appwire.ThreadForkResponse{Thread: appwire.Thread{
		ID:        childID,
		SessionID: childID,
		Source:    "local",
		Serf:      appwire.SerfThread{Ref: childRef},
	}}, nil
}

func threadForkRequiresTurnCapability(params appwire.ThreadForkParams) bool {
	return strings.TrimSpace(params.SourceTurnID) != "" ||
		strings.TrimSpace(params.EditedInput) != "" ||
		strings.TrimSpace(params.Label) != ""
}

func parseSourceTurnID(raw string) (int, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "turn_"))
	if raw == "" {
		return 0, fmt.Errorf("sourceTurnId is required")
	}
	turn, err := strconv.Atoi(raw)
	if err != nil || turn < 1 {
		return 0, fmt.Errorf("sourceTurnId must be a positive turn number")
	}
	return turn, nil
}

func completeDirs(params appwire.DirsCompleteParams) (appwire.DirsCompleteResponse, error) {
	prefix := params.Prefix
	if prefix == "" {
		prefix = os.Getenv("HOME")
	}
	if strings.HasPrefix(prefix, "~/") || prefix == "~" {
		prefix = filepath.Join(os.Getenv("HOME"), strings.TrimPrefix(prefix, "~"))
	}
	cleaned, err := sanitizeDirPrefix(prefix)
	if err != nil {
		return appwire.DirsCompleteResponse{}, nil
	}
	prefix = cleaned

	var listDir, filter string
	if strings.HasSuffix(prefix, string(filepath.Separator)) || prefix == "" {
		listDir = prefix
		if listDir == "" {
			listDir = string(filepath.Separator)
		}
	} else {
		listDir = filepath.Dir(prefix)
		filter = filepath.Base(prefix)
	}

	entries, err := os.ReadDir(listDir)
	if err != nil {
		return appwire.DirsCompleteResponse{}, nil
	}
	limit := params.Limit
	if limit <= 0 || limit > 30 {
		limit = 30
	}
	results := make([]string, 0, limit)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") && filter == "" {
			continue
		}
		if filter != "" && !strings.HasPrefix(strings.ToLower(name), strings.ToLower(filter)) {
			continue
		}
		results = append(results, filepath.Join(listDir, name))
		if len(results) >= limit {
			break
		}
	}
	sort.Strings(results)
	return appwire.DirsCompleteResponse{Data: results}, nil
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
