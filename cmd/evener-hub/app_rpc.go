package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/fspaths"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/plugins"
	"primeradiant.com/evener/rendezvous"
)

func newHubSourceRegistry(cfg hubcore.WebConfig) *appsource.Registry {
	registry := appsource.NewRegistry()
	registry.Add(appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		if cfg.Roster != nil {
			live := cfg.Roster.List()
			entries := make([]appsource.LocalDaemonEntry, 0, len(live))
			for _, item := range live {
				if item.Crashed {
					continue
				}
				entry := appsource.LocalDaemonEntry{
					Entry:         item.Entry,
					SessionID:     item.SessionID,
					Status:        item.Status,
					PendingAsk:    item.PendingAsk,
					RunningJobs:   item.RunningJobs,
					CompletedJobs: item.CompletedJobs,
				}
				entries = append(entries, entry)
				// In-process descendants are addressed as their own AppWire
				// threads, but are served by their owner's daemon endpoint.
				for _, childID := range item.RunningSubagentIDs {
					child := entry
					child.OwnerSessionID = entry.SessionID
					child.SessionID = childID
					// The child's own projected status when the daemon carries
					// it — inheriting the parent's status would render a
					// settled delegate as working (or vice versa). "" (old
					// daemon) keeps the inherited status, the pre-states
					// behavior.
					if childState := strings.TrimSpace(item.RunningSubagentStates[childID]); childState != "" {
						child.Status = childState
					}
					child.ReadOnlyAlias = true
					entries = append(entries, child)
				}
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
	return registry
}

var (
	resolveTurnStartSource = sourceForThread
	resumeTurnStartThread  = hubThreadResume
	authLoginComplete      = func(c *hubAuthController, ctx context.Context, p appwire.AuthLoginCompleteParams) (appwire.AuthLoginCompleteResponse, error) {
		return c.LoginComplete(ctx, p)
	}
	authDevicePoll = func(c *hubAuthController, ctx context.Context, p appwire.AuthDevicePollParams) (appwire.AuthDevicePollResponse, error) {
		return c.DevicePoll(ctx, p)
	}
	launchTrustRepo = func(c *hubLaunchController, ctx context.Context, p appwire.LaunchConfigTrustRepoParams) (appwire.LaunchConfigResolved, error) {
		return c.TrustRepo(ctx, p)
	}
)

type threadReadRelayPolicy interface {
	RelayOnThreadRead() bool
}

func relayOnThreadRead(source appsource.Source) bool {
	if policy, ok := source.(threadReadRelayPolicy); ok {
		return policy.RelayOnThreadRead()
	}
	return true
}

// listItemTurns returns a packed item-mode page when the source has item
// candidates or when its source page contains data. A legacy source with
// no data or a ListTurns error is left for the caller's saved-transcript
// fallback; candidate and packing errors are terminal just as they are for a
// native ItemCandidateSource.
func listItemTurns(
	ctx context.Context,
	source appsource.Source,
	params appwire.ThreadTurnsListParams,
	logf func(format string, args ...any),
) (appwire.ThreadTurnsListResponse, bool, error) {
	itemLimit, err := appwire.NormalizeTranscriptItemLimit(params.ItemLimit)
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, true, err
	}
	params.ItemsView = string(appwire.TurnItemsViewFragment)
	var live appwire.ThreadTurnsListResponse
	var candidates transcriptItemCandidateResult
	if _, native := source.(appsource.ItemCandidateSource); native {
		candidates, err = sourceItemCandidateResultForList(ctx, source, params, live)
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, true, err
		}
	} else {
		live, err = source.ListTurns(ctx, params)
		if err != nil || len(live.Data) == 0 {
			return live, false, err
		}
		candidates, err = sourceItemCandidateResultForList(ctx, source, params, live)
		if err != nil {
			return appwire.ThreadTurnsListResponse{}, true, err
		}
	}

	meta, metaErr := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: params.Ref, ThreadID: params.ThreadID, IncludeTurns: false})
	if metaErr != nil && logf != nil {
		logf("thread turns metadata enrichment unavailable: %v", metaErr)
	}
	packed, packErr := packThreadTurnsItemCandidates(candidates, func(response appwire.ThreadTurnsListResponse) (appwire.ThreadTurnsListResponse, error) {
		if metaErr == nil {
			thread := appwire.Thread{
				ID:        meta.Thread.ID,
				SessionID: meta.Thread.SessionID,
				CWD:       meta.Thread.CWD,
				Turns:     response.Data,
			}
			thread = enrichThreadFileBackedOutputImages(stampThreadImageURLs(thread))
			response.Data = thread.Turns
		}
		return response, nil
	}, itemLimit)
	if packErr != nil {
		return appwire.ThreadTurnsListResponse{}, true, packErr
	}
	return packed, true, nil
}

func blockedUnknownMutationError(clientMutationID string, err error) error {
	if isDaemonRestartRequiredError(err) {
		return restartRequiredMutationError(err, clientMutationID)
	}
	return appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: err.Error(),
		Data: appwire.ErrorData{
			EvenerErrorInfo:  appwire.ErrorMutationOutcomeUnknown,
			ClientMutationID: clientMutationID,
			MutationOutcome:  appwire.MutationOutcomeUnknown,
			RetryDisposition: appwire.RetryDispositionBlocked,
			Cause:            "persistenceUnavailable",
		},
	}
}

// allowsPastFallbackAfterLiveReadFailure preserves atomic rejoin once a live
// relay is available. A subscribed local read with no rendezvous entry never
// acquired a relay, so it may still hydrate the persisted transcript.
func allowsPastFallbackAfterLiveReadFailure(source appsource.Source, params appwire.ThreadReadParams, err error) bool {
	if !params.Subscribe {
		return true
	}
	_, requiresLiveHandoff := source.(appsource.RelaySessionSource)
	return !requiresLiveHandoff || isDeadSessionError(err)
}

// hubLaunchConfigRoot resolves cfg.LaunchConfigRoot, falling back to
// cmdutil.DefaultConfigRoot() when unset — the same defensive fallback
// hubStateRoot below uses, for the same reason (a zero-value WebConfig built
// directly, as some tests do).
func hubLaunchConfigRoot(cfg hubcore.WebConfig) string {
	if cfg.LaunchConfigRoot != "" {
		return cfg.LaunchConfigRoot
	}
	return cmdutil.DefaultConfigRoot()
}

func newHubAppServer(cfg hubcore.WebConfig, sources *appsource.Registry) *appserver.Server {
	return newHubAppServerWithNavigation(cfg, sources, nil, nil)
}

func newHubAppServerWithNavigation(cfg hubcore.WebConfig, sources *appsource.Registry, navigation *NavigationService, resolve topLevelSessionResolver) *appserver.Server {
	return newHubAppServerWithNavigationAndTrace(cfg, sources, navigation, resolve, nil)
}

func newHubAppServerWithNavigationAndTrace(cfg hubcore.WebConfig, sources *appsource.Registry, navigation *NavigationService, resolve topLevelSessionResolver, appwireTrace *appserver.WebSocketTrace) *appserver.Server {
	capability := &appwire.NavigationCapability{Version: 1}
	var capabilityProvider func() *appwire.NavigationCapability
	if navigation != nil {
		capability = nil
		capabilityProvider = func() *appwire.NavigationCapability {
			return navigation.Capability()
		}
	}
	hubLogf := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "[hub] "+format+"\n", args...)
	}
	server := appserver.NewServer(appserver.ServerConfig{
		ServerName:           "evener-hub",
		Version:              Version,
		SourceID:             "local",
		WebSocketTrace:       appwireTrace,
		Navigation:           capability,
		NavigationCapability: capabilityProvider,
		Logf:                 hubLogf,
		Features: appwire.FeatureSet{
			ThreadList:                true,
			ThreadTurnsList:           true,
			TurnStart:                 true,
			TurnSteer:                 true,
			ThreadClear:               true,
			ThreadShutdown:            true,
			ForkFromTurn:              true,
			Tasks:                     true,
			TranscriptList:            true,
			ModelList:                 true,
			DirectoryComplete:         true,
			Auth:                      true,
			TranscriptDisplaySettings: true,
			KeybindingsSettings:       true,
		},
	})
	hubStateRoot := cfg.HubStateRoot
	if hubStateRoot == "" {
		// Defensive only: LoadConfig's applyConfigDefaults always populates
		// HubStateRoot, so a zero-value Config built directly (as in some
		// tests) is the only way this branch runs.
		hubStateRoot = cmdutil.DefaultStateRoot()
	}
	authController := newHubAuthControllerWithStore(hubStateRoot, cfg.CredsStore)
	authController.reg = cfg.Registry
	authController.providersConfigPath = cfg.ProvidersConfigPath
	authController.noUserLayer = cfg.NoUserLayer
	var instancesController *hubInstancesController
	if cfg.Registry != nil && cfg.ProvidersConfigPath != "" {
		instancesController = &hubInstancesController{
			reg:                 cfg.Registry,
			providersConfigPath: cfg.ProvidersConfigPath,
			auth:                authController,
		}
	}
	relayFunctions := newHubRelayFunctions(server, cfg, sources)
	if observeHubRelayFunctions != nil {
		observeHubRelayFunctions(relayFunctions)
	}
	registerThreadHandlers(server, cfg, sources, relayFunctions, hubLogf)
	registerThreadNameSetHandler(server, cfg, sources, navigation)
	registerAuthHandlers(server, authController)
	registerInstanceHandlers(server, instancesController)
	// launch.toml is user-editable configuration, so its root is the config
	// root, not hubStateRoot (machine-generated state).
	launchController := newHubLaunchController(hubLaunchConfigRoot(cfg))
	registerLaunchHandlers(server, launchController)
	pluginsController := newHubPluginsController(cfg.PluginRoot, hubLaunchConfigRoot(cfg))
	registerPluginHandlers(server, pluginsController)
	registerMobilePairingHandler(server, cfg)
	registerNavigationReadHandler(server, navigation)
	registerFavoriteHandler(server, cfg, navigation)
	registerArchiveHandler(server, cfg, func() *NavigationService { return navigation })
	registerSessionDeleteHandler(server, nil)
	registerPinSectionHandlers(server, cfg, navigation, resolve)
	registerMiscHandlers(server, cfg, sources)
	registerPluginAutoUpgradeHandlers(server, plugins.NewManager(cfg.PluginRoot))
	registerTranscriptDisplayHandlers(server, cfg.TranscriptDisplayStore)
	registerKeybindingsHandlers(server, cfg.KeybindingsStore)
	return server
}

// registerThreadHandlers registers the thread- and turn-lifecycle RPC handlers
// on the server. The relay closures (startRelay, startTurn, startRelayForThread)
// are constructed by newHubAppServer and passed in so the handlers close over
// the same relay state.
func registerThreadHandlers(
	server *appserver.Server,
	cfg hubcore.WebConfig,
	sources *appsource.Registry,
	relays hubRelayFunctions,
	logf func(format string, args ...any),
) {
	appserver.HandleTyped(server.Router(), appwire.MethodThreadList, func(ctx context.Context, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return hubThreadList(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if err := appwire.ValidateThreadReadParams(params); err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		itemLimit, err := appwire.NormalizeTranscriptItemLimit(params.ItemLimit)
		if err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		if params.Ref != "" {
			if _, err := appwire.ParseRef(params.Ref); err != nil {
				return appwire.ThreadReadResponse{}, appwire.InvalidParams(err.Error())
			}
		}
		if cfg.Roster != nil {
			if _, required, ownershipErr := restartRequiredDaemon(ctx, cfg, params.Ref, params.ThreadID); required || ownershipErr != nil {
				if err := hubRosterRefresh(ctx, cfg.Roster); err != nil {
					return appwire.ThreadReadResponse{}, appwire.Unavailable(err.Error())
				}
			}
		}
		source, err := sourceForThreadWithDeletionFence(cfg, sources, params.Ref, params.ThreadID)
		if err != nil {
			if isTargetDeletedError(err) {
				return appwire.ThreadReadResponse{}, err
			}
			resp, ok, pastErr := pastThreadReadResponse(ctx, cfg, params)
			if pastErr != nil {
				return appwire.ThreadReadResponse{}, pastErr
			}
			if ok {
				return resp, nil
			}
			return appwire.ThreadReadResponse{}, err
		}
		read, err := relays.readThread(ctx, source, params)
		if err != nil {
			if allowsPastFallbackAfterLiveReadFailure(source, params, err) {
				if _, local := localPastThreadID(params); local && cfg.Roster != nil && isSessionUnavailableError(err) {
					if err := hubRosterRefresh(ctx, cfg.Roster); err != nil {
						return appwire.ThreadReadResponse{}, appwire.Unavailable(err.Error())
					}
				}
				saved, ok, pastErr := pastThreadReadResponse(ctx, cfg, params)
				if pastErr != nil {
					return appwire.ThreadReadResponse{}, pastErr
				}
				if ok {
					return saved, nil
				}
			}
			return appwire.ThreadReadResponse{}, err
		}
		resp := read.response
		liveItemCandidatesEmpty := false
		if params.IncludeTurns {
			if read.hasItemCandidates {
				liveItemCandidatesEmpty = len(read.itemCandidates.Candidates.Candidates) == 0
			} else if candidates, candidateErr := itemCandidateResultFromReadResponse(resp); candidateErr == nil {
				liveItemCandidatesEmpty = len(candidates.Candidates.Candidates) == 0
			}
		}
		resp.Thread, err = mergePastThreadForRead(ctx, cfg, params, resp.Thread)
		if err != nil {
			read.finish(false)
			return appwire.ThreadReadResponse{}, err
		}
		if params.IncludeTurns {
			usedPastItemPage := false
			if liveItemCandidatesEmpty && len(resp.Thread.Turns) > 0 {
				past, ok, pastErr := pastThreadItemReadResponse(ctx, cfg, params)
				if pastErr != nil {
					read.finish(false)
					return appwire.ThreadReadResponse{}, pastErr
				}
				if ok {
					resp.Thread.Turns = past.Thread.Turns
					resp.OlderCursor = past.OlderCursor
					resp.Thread = enrichThreadFileBackedOutputImages(stampThreadImageURLs(resp.Thread))
					annotateThreadProjects([]appwire.Thread{resp.Thread})
					usedPastItemPage = true
				}
			}
			if !usedPastItemPage {
				candidates := transcriptItemCandidateResultFromSource(read.itemCandidates)
				if !read.hasItemCandidates {
					var candidateErr error
					candidates, candidateErr = sourceItemCandidateResultForRead(ctx, source, params, resp)
					if candidateErr != nil {
						read.finish(false)
						return appwire.ThreadReadResponse{}, candidateErr
					}
				}
				packed, packErr := packThreadReadItemCandidates(candidates, func(response appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error) {
					response.Thread = threadWithPackedTurns(resp.Thread, response.Thread.Turns)
					// A live daemon's turns carry sha-addressed tool-result descriptors
					// with no route on them (the daemon does not serve the bytes; this
					// hub does), so route stamping stays inside the final packer.
					response.Thread = enrichThreadFileBackedOutputImages(stampThreadImageURLs(response.Thread))
					annotateThreadProjects([]appwire.Thread{response.Thread})
					return response, nil
				}, itemLimit)
				if packErr != nil {
					read.finish(false)
					return appwire.ThreadReadResponse{}, packErr
				}
				resp = packed
			}
		} else {
			// A live daemon's turns carry sha-addressed tool-result descriptors with
			// no route on them (the daemon does not serve the bytes; this hub does),
			// so the route is stamped here before the file-backed pass adds any
			// /doc/image descriptors of its own.
			resp.Thread = enrichThreadFileBackedOutputImages(stampThreadImageURLs(resp.Thread))
			annotateThreadProjects([]appwire.Thread{resp.Thread})
		}
		if err := appwire.ValidateThreadReadItemResponse(resp); err != nil {
			read.finish(false)
			return appwire.ThreadReadResponse{}, err
		}
		read.response = resp
		if read.handoff != nil {
			if !relays.captureThreadRead(ctx, params, read) {
				return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("thread subscription is unavailable")
			}
		} else if params.Subscribe || relayOnThreadRead(source) {
			if err := relays.startRelay(ctx, source, params, resp.Thread); err != nil {
				return appwire.ThreadReadResponse{}, err
			}
		}
		return resp, nil
	})
	// thread/unsubscribe drops only the calling connection's downstream
	// subscription — the browser's own read of a thread it is navigating away
	// from. The relay key is derived by the same helper thread/read's relay
	// uses (threadRelayTarget), so the removal lands on the exact registry
	// entry Subscribe created. Resolution deliberately uses the plain registry
	// lookup without session activation because an unsubscribe must not
	// start a session just to stop delivering to it. When no source resolves,
	// the ref's own namespace (parsed from the ref itself) is the best key
	// available; Unsubscribe is conn-scoped and idempotent, so a missed key
	// costs only a subscription the connection-close cleanup reaps anyway.
	appserver.HandleTyped(server.Router(), appwire.MethodThreadUnsubscribe, func(ctx context.Context, params appwire.ThreadUnsubscribeParams) (appwire.EmptyResponse, error) {
		source, err := sourceForThread(sources, params.Ref, params.ThreadID)
		if err != nil {
			if isTargetDeletedError(err) {
				return appwire.EmptyResponse{}, err
			}
			if parsed, parseErr := appwire.ParseRef(strings.TrimSpace(params.Ref)); parseErr == nil && parsed.SourceID != "" {
				appserver.Unsubscribe(ctx, parsed.SourceID+":"+parsed.ThreadID)
				return appwire.EmptyResponse{}, nil
			}
			appserver.Unsubscribe(ctx, "local:"+strings.TrimSpace(params.ThreadID))
			return appwire.EmptyResponse{}, nil
		}
		relayKey, _, keyErr := threadRelayTarget(source, appwire.ThreadReadParams{ThreadID: params.ThreadID, Ref: params.Ref})
		if keyErr != nil {
			return appwire.EmptyResponse{}, keyErr
		}
		appserver.Unsubscribe(ctx, relayKey)
		return appwire.EmptyResponse{}, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadTurnsList, func(ctx context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		if err := appwire.ValidateThreadTurnsListParams(params); err != nil {
			return appwire.ThreadTurnsListResponse{}, err
		}
		// Live source first; fall back to the saved transcript (paged on the
		// hub) for past/not-loaded sessions.
		source, srcErr := sourceForThreadWithDeletionFence(cfg, sources, params.Ref, params.ThreadID)
		if isTargetDeletedError(srcErr) {
			return appwire.ThreadTurnsListResponse{}, srcErr
		}
		var live appwire.ThreadTurnsListResponse
		var liveErr error
		var liveItemHandled bool
		if srcErr == nil {
			_, liveItemNative := source.(appsource.ItemCandidateSource)
			live, liveItemHandled, liveErr = listItemTurns(ctx, source, params, logf)
			if liveItemHandled && liveErr == nil && (!liveItemNative || len(live.Data) > 0) {
				return live, nil
			}
			if liveErr == nil && len(live.Data) > 0 {
				if meta, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: params.Ref, ThreadID: params.ThreadID, IncludeTurns: false}); err == nil {
					// File-backed output-image enrichment is intentionally page-local
					// here: args can only be correlated from command-call items present
					// in this returned page (or on the completed item itself).
					thread := enrichThreadFileBackedOutputImages(stampThreadImageURLs(appwire.Thread{
						ID:        meta.Thread.ID,
						SessionID: meta.Thread.SessionID,
						CWD:       meta.Thread.CWD,
						Turns:     live.Data,
					}))
					live.Data = thread.Turns
				}
				return live, nil
			}
		}
		saved, ok, pastErr := pastThreadTurnsList(ctx, cfg, params)
		if pastErr != nil {
			return appwire.ThreadTurnsListResponse{}, pastErr
		}
		if ok {
			return saved, nil
		}
		if srcErr != nil {
			return appwire.ThreadTurnsListResponse{}, srcErr
		}
		return live, liveErr
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSubagentPreview, func(ctx context.Context, params appwire.EvenerSubagentPreviewParams) (appwire.EvenerSubagentPreviewResponse, error) {
		ref := strings.TrimSpace(params.Ref)
		if ref == "" {
			return appwire.EvenerSubagentPreviewResponse{}, appwire.InvalidParams("ref required")
		}
		source, err := sourceForThreadWithDeletionFence(cfg, sources, ref, "")
		if err != nil {
			if isTargetDeletedError(err) {
				return appwire.EvenerSubagentPreviewResponse{}, err
			}
			thread, ok, pastErr := pastThreadForRead(ctx, cfg, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemsView: "full"})
			if pastErr != nil {
				return appwire.EvenerSubagentPreviewResponse{}, pastErr
			}
			if ok {
				return subagentPreviewFromThread(thread, ref, params.Limit), nil
			}
			return appwire.EvenerSubagentPreviewResponse{}, err
		}
		resp, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemsView: "full"})
		if err != nil {
			thread, ok, pastErr := pastThreadForRead(ctx, cfg, appwire.ThreadReadParams{Ref: ref, IncludeTurns: true, ItemsView: "full"})
			if pastErr != nil {
				return appwire.EvenerSubagentPreviewResponse{}, pastErr
			}
			if ok {
				return subagentPreviewFromThread(thread, ref, params.Limit), nil
			}
			return appwire.EvenerSubagentPreviewResponse{}, err
		}
		return subagentPreviewFromThread(resp.Thread, ref, params.Limit), nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadStart, func(ctx context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
		resp, err := hubThreadStart(ctx, cfg, sources, params)
		if err != nil {
			return appwire.ThreadStartResponse{}, err
		}
		if err := relays.startRelayForThread(ctx, resp.Thread); err != nil {
			appserver.Notify(ctx, appwire.NotifyWarning, appwire.WarningParams{
				ThreadID: resp.Thread.ID,
				Ref:      resp.Thread.Evener.Ref,
				Source:   "hub",
				Title:    "Live updates unavailable",
				Message:  "thread started, but Hub could not attach live updates: " + err.Error(),
			})
		}
		return resp, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadResume, func(ctx context.Context, params appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
		resp, err := hubThreadResume(ctx, cfg, sources, params)
		if err != nil {
			return appwire.ThreadResumeResponse{}, err
		}
		if err := relays.startRelayForThread(ctx, resp.Thread); err != nil {
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
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.TurnStartResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		resolved := false
		attemptStart := func() (appwire.TurnStartResponse, error) {
			source, err := withDeletionTargetOwnership(ctx, cfg, params.Ref, params.ThreadID, params.ClientMutationID, func() (appsource.Source, error) {
				return resolveTurnStartSource(sources, params.Ref, params.ThreadID)
			})
			if err != nil {
				return appwire.TurnStartResponse{}, err
			}
			resolved = true
			return relays.startTurn(ctx, source, params)
		}
		resp, err := attemptStart()
		if err == nil {
			return resp, nil
		}
		if !resolved {
			if wire, ok := errors.AsType[appwire.WireError](err); ok && wire.Code == appwire.CodeInvalidParams {
				return appwire.TurnStartResponse{}, err
			}
			if isTargetDeletedError(err) || isDaemonRestartRequiredError(err) {
				return appwire.TurnStartResponse{}, err
			}
			if _, resumeErr := resumeTurnStartThread(ctx, cfg, sources, appwire.ThreadResumeParams{Ref: params.Ref, Session: params.ThreadID}); resumeErr != nil {
				return appwire.TurnStartResponse{}, blockedUnknownMutationError(params.ClientMutationID, resumeErr)
			}
			resolved = false
			return attemptStart()
		}
		if params.Ref != "" && !hubKnowsRef(cfg, params.Ref) {
			return appwire.TurnStartResponse{}, err
		}
		if !shouldResumeAfterTurnStartError(err) {
			return appwire.TurnStartResponse{}, err
		}
		if _, resumeErr := resumeTurnStartThread(ctx, cfg, sources, appwire.ThreadResumeParams{Ref: params.Ref, Session: params.ThreadID}); resumeErr != nil {
			return appwire.TurnStartResponse{}, blockedUnknownMutationError(params.ClientMutationID, resumeErr)
		}
		resolved = false
		return attemptStart()
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnSteer, func(ctx context.Context, params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
		if err := validateAppWireInputItems(params.Input); err != nil {
			return appwire.TurnSteerResponse{}, appwire.InvalidParams(err.Error())
		}
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.TurnSteerResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		return withDeletionTargetOwnership(ctx, cfg, params.Ref, params.ThreadID, params.ClientMutationID, func() (appwire.TurnSteerResponse, error) {
			source, err := sourceForThread(sources, params.Ref, params.ThreadID)
			if err != nil {
				return appwire.TurnSteerResponse{}, err
			}
			return source.SteerTurn(ctx, params)
		})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnInterrupt, func(ctx context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.TurnInterruptResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		return withDeletionTargetOwnership(ctx, cfg, params.Ref, params.ThreadID, params.ClientMutationID, func() (appwire.TurnInterruptResponse, error) {
			source, err := sourceForThread(sources, params.Ref, params.ThreadID)
			if err != nil {
				return appwire.TurnInterruptResponse{}, err
			}
			return source.InterruptTurn(ctx, params)
		})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSandboxEscalationResolve, func(ctx context.Context, params appwire.SandboxEscalationResolveParams) (appwire.EmptyResponse, error) {
		return withDeletionTargetOwnership(ctx, cfg, params.Ref, params.ThreadID, "", func() (appwire.EmptyResponse, error) {
			if err := refreshDaemonRestartRequiredError(ctx, cfg, params.Ref, params.ThreadID, ""); err != nil {
				return appwire.EmptyResponse{}, err
			}
			source, err := sourceForThread(sources, params.Ref, params.ThreadID)
			if err != nil {
				return appwire.EmptyResponse{}, err
			}
			return appwire.EmptyResponse{}, source.ResolveSandboxEscalation(ctx, params)
		})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnQueue, func(ctx context.Context, params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
		if err := validateAppWireInputItems(params.Input); err != nil {
			return appwire.TurnQueueResponse{}, appwire.InvalidParams(err.Error())
		}
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.TurnQueueResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		return withDeletionTargetOwnership(ctx, cfg, params.Ref, "", params.ClientMutationID, func() (appwire.TurnQueueResponse, error) {
			source, err := sourceForThread(sources, params.Ref, "")
			if err != nil {
				return appwire.TurnQueueResponse{}, err
			}
			return source.QueueTurn(ctx, params)
		})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnDrainAsSteer, func(ctx context.Context, params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
		if err := validateAppWireInputItems(params.Input); err != nil {
			return appwire.TurnDrainAsSteerResponse{}, appwire.InvalidParams(err.Error())
		}
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.TurnDrainAsSteerResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		return withDeletionTargetOwnership(ctx, cfg, params.Ref, "", params.ClientMutationID, func() (appwire.TurnDrainAsSteerResponse, error) {
			source, err := sourceForThread(sources, params.Ref, "")
			if err != nil {
				return appwire.TurnDrainAsSteerResponse{}, err
			}
			return source.DrainAsSteer(ctx, params)
		})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnPromoteQueuedAsSteer, func(ctx context.Context, params appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
		if params.Index < 0 {
			return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.InvalidParams("index must be >= 0")
		}
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		if strings.TrimSpace(params.ExpectedEntryID) == "" {
			return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.InvalidParams("expectedEntryId is required")
		}
		return withDeletionTargetOwnership(ctx, cfg, params.Ref, "", params.ClientMutationID, func() (appwire.TurnPromoteQueuedAsSteerResponse, error) {
			source, err := sourceForThread(sources, params.Ref, "")
			if err != nil {
				return appwire.TurnPromoteQueuedAsSteerResponse{}, err
			}
			return source.PromoteQueuedAsSteer(ctx, params)
		})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodTurnCancelQueued, func(ctx context.Context, params appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
		if params.Index < 0 {
			return appwire.TurnCancelQueuedResponse{}, appwire.InvalidParams("index must be >= 0")
		}
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.TurnCancelQueuedResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		if strings.TrimSpace(params.ExpectedEntryID) == "" {
			return appwire.TurnCancelQueuedResponse{}, appwire.InvalidParams("expectedEntryId is required")
		}
		return withDeletionTargetOwnership(ctx, cfg, params.Ref, "", params.ClientMutationID, func() (appwire.TurnCancelQueuedResponse, error) {
			source, err := sourceForThread(sources, params.Ref, "")
			if err != nil {
				return appwire.TurnCancelQueuedResponse{}, err
			}
			return source.CancelQueued(ctx, params)
		})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadClear, func(ctx context.Context, params appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
		if strings.TrimSpace(params.ClientMutationID) == "" {
			return appwire.ThreadClearResponse{}, appwire.InvalidParams("clientMutationId is required")
		}
		if strings.TrimSpace(params.ExpectedInstanceID) == "" {
			return appwire.ThreadClearResponse{}, appwire.InvalidParams("expectedInstanceId is required")
		}
		return clearThreadWithResume(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadCompactStart, func(ctx context.Context, params appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, compactThreadWithResume(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadShutdown, func(ctx context.Context, params appwire.ThreadShutdownParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, shutdownThreadTolerateExited(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadModelSet, func(ctx context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, setThreadModelWithResume(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadVisionModelSet, func(ctx context.Context, params appwire.ThreadVisionModelSetParams) (appwire.EmptyResponse, error) {
		return appwire.EmptyResponse{}, setThreadVisionModelWithResume(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodThreadReasoningEffortSet, func(ctx context.Context, params appwire.ThreadReasoningEffortSetParams) (appwire.EmptyResponse, error) {
		return withDeletionTargetOwnership(ctx, cfg, params.Ref, "", "", func() (appwire.EmptyResponse, error) {
			if err := refreshDaemonRestartRequiredError(ctx, cfg, params.Ref, "", ""); err != nil {
				return appwire.EmptyResponse{}, err
			}
			source, err := sourceForThread(sources, params.Ref, "")
			if err != nil {
				return appwire.EmptyResponse{}, err
			}
			// No capability gate: there is no reasoning-effort thread capability, and
			// the daemon/source already reject the call when it is unsupported (a
			// non-evener source, or a daemon without the effort hook).
			return appwire.EmptyResponse{}, source.SetThreadReasoningEffort(ctx, params)
		})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodGoalSet, func(ctx context.Context, params appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
		return setGoalWithResume(ctx, cfg, sources, params)
	})
}

// registerAuthHandlers registers the evener/auth/* RPC handlers, routed to the
// auth controller. Successful mutations broadcast evener/auth/updated.
func registerAuthHandlers(server *appserver.Server, authController *hubAuthController) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthStatus, func(_ context.Context, params appwire.AuthStatusParams) (appwire.AuthStatusResponse, error) {
		return authController.Status(params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthTest, func(ctx context.Context, params appwire.AuthTestParams) (appwire.AuthTestResponse, error) {
		return authController.TestCredentials(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthLoginStart, func(_ context.Context, params appwire.AuthLoginStartParams) (appwire.AuthLoginStartResponse, error) {
		return authController.LoginStart(params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthLoginComplete, func(ctx context.Context, params appwire.AuthLoginCompleteParams) (appwire.AuthLoginCompleteResponse, error) {
		resp, err := authLoginComplete(authController, ctx, params)
		if err == nil {
			notifyAuthUpdated(server, resp.Status.Provider, resp.Status.ActiveSource)
		}
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthLogout, func(ctx context.Context, params appwire.AuthLogoutParams) (appwire.AuthLogoutResponse, error) {
		resp, err := authController.Logout(params)
		if err == nil {
			notifyAuthUpdated(server, resp.Status.Provider, resp.Status.ActiveSource)
		}
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthList, func(_ context.Context, params appwire.EmptyParams) (appwire.AuthListResponse, error) {
		return authController.List(params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthApiKeySet, func(ctx context.Context, params appwire.AuthApiKeySetParams) (appwire.AuthStatusResponse, error) {
		resp, err := authController.ApiKeySet(params)
		if err == nil {
			notifyAuthUpdated(server, resp.Provider, resp.ActiveSource)
		}
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthApiKeyClear, func(ctx context.Context, params appwire.AuthApiKeyClearParams) (appwire.AuthStatusResponse, error) {
		resp, err := authController.ApiKeyClear(params)
		if err == nil {
			notifyAuthUpdated(server, resp.Provider, resp.ActiveSource)
		}
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthCredentialJsonSet, func(ctx context.Context, params appwire.AuthCredentialJsonSetParams) (appwire.AuthStatusResponse, error) {
		resp, err := authController.CredentialJsonSet(params)
		if err == nil {
			notifyAuthUpdated(server, resp.Provider, resp.ActiveSource)
		}
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthDeviceStart, func(ctx context.Context, params appwire.AuthDeviceStartParams) (appwire.AuthDeviceStartResponse, error) {
		return authController.DeviceStart(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerAuthDevicePoll, func(ctx context.Context, params appwire.AuthDevicePollParams) (appwire.AuthDevicePollResponse, error) {
		resp, err := authDevicePoll(authController, ctx, params)
		if err == nil && resp.State == "authorized" {
			notifyAuthUpdated(server, resp.Status.Provider, resp.Status.ActiveSource)
		}
		return resp, err
	})
}

// registerInstanceHandlers registers the evener/instance/* CRUD handlers. When no
// instances controller is configured (providers.toml path unset), no handlers
// are registered — matching the original inline guard. Successful mutations
// broadcast evener/auth/updated (see notifyInstanceUpdated) so every other
// connected client refetches its now-stale instance list.
func registerInstanceHandlers(server *appserver.Server, instancesController *hubInstancesController) {
	if instancesController == nil {
		return
	}
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerInstanceList, func(_ context.Context, _ appwire.EmptyParams) (appwire.InstanceListResponse, error) {
		return instancesController.List(), nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerInstanceCreate, func(_ context.Context, params appwire.InstanceCreateParams) (appwire.InstanceListResponse, error) {
		if err := instancesController.Create(params); err != nil {
			return appwire.InstanceListResponse{}, err
		}
		notifyInstanceUpdated(server)
		return instancesController.List(), nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerInstanceEdit, func(_ context.Context, params appwire.InstanceEditParams) (appwire.InstanceListResponse, error) {
		if err := instancesController.Edit(params); err != nil {
			return appwire.InstanceListResponse{}, err
		}
		notifyInstanceUpdated(server)
		return instancesController.List(), nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerInstanceRemove, func(_ context.Context, params appwire.InstanceRemoveParams) (appwire.InstanceListResponse, error) {
		if err := instancesController.Remove(params); err != nil {
			return appwire.InstanceListResponse{}, err
		}
		notifyInstanceUpdated(server)
		return instancesController.List(), nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerInstanceSetDefault, func(_ context.Context, params appwire.InstanceSetDefaultParams) (appwire.InstanceListResponse, error) {
		if err := instancesController.SetDefault(params); err != nil {
			return appwire.InstanceListResponse{}, err
		}
		notifyInstanceUpdated(server)
		return instancesController.List(), nil
	})
}

// registerLaunchHandlers registers the evener/launch/* RPC handlers, routed to the
// launch controller. Successful layer/trust mutations broadcast evener/launch/updated.
func registerLaunchHandlers(server *appserver.Server, launchController *hubLaunchController) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerLaunchResolve, func(ctx context.Context, params appwire.LaunchConfigResolveParams) (appwire.LaunchConfigResolved, error) {
		return launchController.Resolve(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerLaunchSchema, func(ctx context.Context, params appwire.EmptyParams) (appwire.LaunchOptionSchemaResponse, error) {
		return launchController.Schema(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerLaunchGetLayer, func(ctx context.Context, params appwire.LaunchConfigGetLayerParams) (appwire.LaunchConfigLayer, error) {
		return launchController.GetLayer(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerLaunchSetLayer, func(ctx context.Context, params appwire.LaunchConfigSetLayerParams) (appwire.LaunchConfigResolved, error) {
		resp, err := launchController.SetLayer(ctx, params)
		if err == nil {
			notifyLaunchUpdated(server, params.CWD, params.Layer)
		}
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerLaunchTrustRepo, func(ctx context.Context, params appwire.LaunchConfigTrustRepoParams) (appwire.LaunchConfigResolved, error) {
		resp, err := launchTrustRepo(launchController, ctx, params)
		if err == nil {
			notifyLaunchUpdated(server, params.CWD, "repo")
		}
		return resp, err
	})
}

// registerPluginHandlers registers the evener/marketplace/* and evener/plugin/*
// RPC handlers, routed to the plugins controller. Mutations broadcast
// evener/marketplace/updated or evener/plugin/updated.
func registerPluginHandlers(server *appserver.Server, pluginsController *hubPluginsController) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerMarketplaceList, func(_ context.Context, _ appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
		return pluginsController.ListMarketplaces()
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerMarketplaceAdd, func(ctx context.Context, params appwire.MarketplaceAddParams) (appwire.MarketplaceListResponse, error) {
		resp, err := pluginsController.AddMarketplace(ctx, params)
		if err == nil {
			notifyMarketplaceUpdated(server)
		}
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerMarketplaceRemove, func(ctx context.Context, params appwire.MarketplaceNameParams) (appwire.MarketplaceListResponse, error) {
		resp, err := pluginsController.RemoveMarketplace(ctx, params)
		if err == nil {
			notifyMarketplaceUpdated(server)
		}
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerMarketplaceRefresh, func(ctx context.Context, params appwire.MarketplaceNameParams) (appwire.MarketplaceListResponse, error) {
		resp, err := pluginsController.RefreshMarketplace(ctx, params)
		if err == nil {
			notifyMarketplaceUpdated(server)
		}
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerMarketplaceBrowse, func(ctx context.Context, params appwire.MarketplaceBrowseParams) (appwire.MarketplaceBrowseResponse, error) {
		return pluginsController.Browse(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginList, func(_ context.Context, _ appwire.EmptyParams) (appwire.PluginListResponse, error) {
		return pluginsController.ListPlugins()
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginPreview, func(ctx context.Context, params appwire.PluginPreviewParams) (appwire.PluginPreviewResponse, error) {
		return pluginsController.Preview(ctx, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginInstall, func(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
		resp, err := pluginsController.Install(ctx, params)
		if err == nil {
			notifyPluginUpdated(server)
		}
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginUpgrade, func(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
		resp, err := pluginsController.Upgrade(ctx, params)
		if err == nil {
			notifyPluginUpdated(server)
		}
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginRemove, func(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
		resp, err := pluginsController.Remove(ctx, params)
		if err == nil {
			notifyPluginUpdated(server)
		}
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginEnable, func(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
		resp, err := pluginsController.Enable(ctx, params)
		if err == nil {
			notifyPluginUpdated(server)
		}
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginDisable, func(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
		resp, err := pluginsController.Disable(ctx, params)
		if err == nil {
			notifyPluginUpdated(server)
		}
		return resp, err
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPluginSetAutoUpgrade, func(ctx context.Context, params appwire.PluginSetAutoUpgradeParams) (appwire.PluginListResponse, error) {
		resp, err := pluginsController.SetAutoUpgrade(ctx, params)
		if err == nil {
			notifyPluginUpdated(server)
		}
		return resp, err
	})
}

// notifyMarketplaceUpdated broadcasts a evener/marketplace/updated notification
// to all connected clients.
func notifyMarketplaceUpdated(server *appserver.Server) {
	server.BroadcastAll(appwire.NotifyEvenerMarketplaceUpdated, map[string]string{})
}

// notifyPluginUpdated broadcasts a evener/plugin/updated notification to all
// connected clients.
func notifyPluginUpdated(server *appserver.Server) {
	server.BroadcastAll(appwire.NotifyEvenerPluginUpdated, map[string]string{})
}

// recentProjectDirsLimit is the session creation flows' path-dropdown option
// count (issue #35): the 15 most recently used projects.
const recentProjectDirsLimit = 15

// registerMiscHandlers registers hub RPC handlers that are not owned by a
// focused controller registration.
func registerMiscHandlers(server *appserver.Server, cfg hubcore.WebConfig, sources *appsource.Registry) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerUpgrade, hubUpgrade)
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSearch, func(_ context.Context, params appwire.SearchParams) (appwire.SearchResponse, error) {
		return hubSearch(cfg, params), nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodModelList, func(ctx context.Context, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
		return hubModelList(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerTasksList, func(ctx context.Context, params appwire.TaskListParams) (appwire.TaskListResponse, error) {
		return hubTasksList(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerJobsList, func(ctx context.Context, params appwire.JobsListParams) (appwire.JobsListResponse, error) {
		return hubJobsList(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerJobsOutput, func(ctx context.Context, params appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
		return hubJobsOutput(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerThreadTranscriptsList, func(ctx context.Context, params appwire.ThreadTranscriptListParams) (appwire.ThreadTranscriptListResponse, error) {
		return hubThreadTranscriptList(ctx, cfg, sources, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPathsComplete, func(_ context.Context, params appwire.PathsCompleteParams) (appwire.PathsCompleteResponse, error) {
		return fspaths.CompletePaths(params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerDirsCreate, func(_ context.Context, params appwire.DirsCreateParams) (appwire.DirsCreateResponse, error) {
		return hubDirsCreate(cfg, params)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerProjectsRecent, func(_ context.Context, params appwire.ProjectsRecentParams) (appwire.ProjectsRecentResponse, error) {
		limit := params.Limit
		if limit <= 0 {
			limit = recentProjectDirsLimit
		}
		// Non-nil even when there is nothing to report: a nil slice marshals as
		// JSON null, which contradicts the wire type's own non-nullable
		// `data: string[]` and crashes any client that trusts it.
		dirs := []string{}
		if cfg.Past != nil {
			dirs = append(dirs, cfg.Past.RecentProjectDirs(limit)...)
		}
		return appwire.ProjectsRecentResponse{Data: dirs}, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPathValidate, func(_ context.Context, params appwire.PathValidateParams) (appwire.PathValidateResponse, error) {
		return fspaths.ValidateLaunchPath(params), nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerGitHead, func(ctx context.Context, params appwire.GitHeadParams) (appwire.GitHeadResponse, error) {
		return hubGitHead(ctx, cfg, params), nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHarnessesList, func(context.Context, appwire.HarnessListParams) (appwire.HarnessListResponse, error) {
		return appwire.HarnessListResponse{Data: launchHarnessDescriptors()}, nil
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerCommandList, func(ctx context.Context, _ appwire.EmptyParams) (appwire.CommandListResponse, error) {
		return hubCommandList(ctx, cfg)
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSettingsOverview, func(ctx context.Context, _ appwire.EmptyParams) (appwire.SettingsOverviewResponse, error) {
		return hubSettingsOverview(ctx, cfg)
	})
}

// hubCommandList answers evener/command/list by loading every plugin a real
// session would load — internal/plugins.Manager.ResolveForLaunch (explicit
// --plugin-dir-equivalent PluginDirs first, then every installed+enabled
// registry entry) — and flattening their discovered slash commands into a
// catalog. This used to mirror discoverPluginsForSettings's display-only scan
// (web_settings.go, pluginDirsFromConfig: an immediate-subdirectory glob of
// the plugin store) instead, which could never see a plugin installed via the
// marketplace/registry system (living at cache/<marketplace>/<plugin>/<sha>,
// not a direct child of the plugins root) — so a registry-installed plugin's
// commands never appeared here even though a spawned session loaded them.
// The hub catalog combines enabled plugin commands with evener-wide commands.
// Evener-wide discovery receives a nil environment because the hub is
// multi-project: project commands are per-session and must never appear here.
// Loading is fail-soft (plugin.LoadAllFailSoft), so one broken or mid-edit
// plugin dir cannot blank out the whole command catalog.
func hubCommandList(ctx context.Context, cfg hubcore.WebConfig) (appwire.CommandListResponse, error) {
	resolution, err := plugins.NewManager(cfg.PluginRoot).ResolveForLaunch(ctx, cfg.PluginDirs, nil)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "warning: listing plugins: %v\n", err)
	}
	loaded, _ := plugin.LoadAllFailSoft(resolution.SelectedDirs)
	evenerwide, _ := plugin.DiscoverEvenerWideCommands(nil)
	merged := plugin.MergeCommands(loaded, evenerwide)
	var commands []appwire.CommandDescriptor
	for _, cmd := range merged {
		commands = append(commands, appwire.CommandDescriptor{
			Name:         cmd.Name,
			PluginName:   cmd.PluginName,
			Description:  cmd.Description,
			ArgumentHint: cmd.ArgumentHint,
			Source:       cmd.Source,
		})
	}
	sort.Slice(commands, func(i, j int) bool {
		if commands[i].Name != commands[j].Name {
			return commands[i].Name < commands[j].Name
		}
		if commands[i].PluginName != commands[j].PluginName {
			return commands[i].PluginName < commands[j].PluginName
		}
		return commands[i].Source < commands[j].Source
	})
	return appwire.CommandListResponse{Commands: commands}, nil
}

// notifyAuthUpdated broadcasts a evener/auth/updated notification to all connected clients.
func notifyAuthUpdated(server *appserver.Server, provider, activeSource string) {
	// Still map[string]string, not appwire.EvenerAuthUpdatedParams (kcb5):
	// provider/activeSource (from AuthStatus) are legitimately empty when no
	// provider is active, but this map always emits both keys anyway; both
	// fields are tagged `omitempty` on the struct, so a typed literal would
	// drop them whenever blank. Not provably byte-identical; left as a map.
	server.BroadcastAll(appwire.NotifyEvenerAuthUpdated, map[string]string{
		"provider":     provider,
		"activeSource": activeSource,
	})
}

// notifyInstanceUpdated broadcasts a evener/auth/updated notification to all
// connected clients after a provider-instance CRUD mutation (create, edit,
// remove, setDefault). It deliberately reuses the auth/updated channel rather
// than minting a new notification type: the client-side handler
// (notifications.js) already treats evener/auth/updated as payload-agnostic —
// "credentials or instances changed, refetch" — reloading both the instances
// panel and the providers settings tab on receipt, regardless of payload
// content. An empty payload mirrors notifyMarketplaceUpdated/
// notifyPluginUpdated below, which broadcast the same way for the same
// reason: there is no single provider/activeSource pair that honestly
// summarizes "the instance list changed."
func notifyInstanceUpdated(server *appserver.Server) {
	server.BroadcastAll(appwire.NotifyEvenerAuthUpdated, appwire.EvenerAuthUpdatedParams{})
}

// notifyLaunchUpdated broadcasts a evener/launch/updated notification to all connected clients.
func notifyLaunchUpdated(server *appserver.Server, cwd, layer string) {
	server.BroadcastAll(appwire.NotifyEvenerLaunchUpdated, appwire.EvenerLaunchUpdatedParams{
		CWD:   cwd,
		Layer: layer,
	})
}
