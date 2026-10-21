package hub

import (
	"context"
	"sync"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

type sessionOperations struct {
	cfg     hubcore.WebConfig
	sources *appsource.Registry
	relays  hubRelayFunctions
	server  *appserver.Server
	mu      sync.Mutex
	watches map[*sessionObservation]struct{}
}

type sessionObservation struct {
	preferenceKey string
	cancel        context.CancelFunc
}

type boundSessionOperations struct {
	owner   *sessionOperations
	local   *appsource.LocalDaemonSource
	binding appwire.SessionBinding
}

func (o *sessionOperations) install() {
	if o.cfg.SessionPreferences == nil {
		return
	}
	for _, source := range o.sources.All() {
		if local, ok := source.(*appsource.LocalDaemonSource); ok {
			local.SetSessionTools(func(binding appwire.SessionBinding) (appwire.SessionHubClient, error) {
				if o.cfg.SessionPreferences == nil {
					return nil, appwire.Unavailable("session preferences unavailable")
				}
				hubID, err := o.cfg.SessionPreferences.HubID()
				if err != nil {
					return nil, err
				}
				binding.HubID = hubID
				return &boundSessionOperations{owner: o, local: local, binding: binding}, nil
			}, func() (string, error) { return o.cfg.SessionPreferences.HubID() })
		}
	}
}

func reconcileSessionTools(sources *appsource.Registry) {
	for _, source := range sources.All() {
		if local, ok := source.(*appsource.LocalDaemonSource); ok {
			local.ReconcileSessionTools()
		}
	}
}

func startSessionTools(ctx context.Context, sources *appsource.Registry, start func(func())) {
	for _, source := range sources.All() {
		if local, ok := source.(*appsource.LocalDaemonSource); ok {
			start(func() { local.RunSessionTools(ctx) })
		}
	}
}

func (b *boundSessionOperations) State(ctx context.Context) (appwire.ManagerView, error) {
	var view appwire.ManagerView
	if err := ctx.Err(); err != nil {
		return view, err
	}
	thread, err := b.local.SessionToolsThread(b.binding)
	if err != nil {
		return view, err
	}
	key, err := managerPreferenceRef(b.owner.cfg, thread)
	if err != nil {
		return view, err
	}
	setting, err := b.owner.cfg.SessionPreferences.Manager(key)
	if err != nil {
		return view, err
	}
	if setting.Enabled && setting.HubID != b.binding.HubID {
		return view, appwire.Conflict("another hub owns Manager authority")
	}
	return appwire.ManagerView{Ref: thread.Evener.Ref, InstanceID: thread.Evener.InstanceID, Setting: setting}, nil
}

func (b *boundSessionOperations) Invoke(ctx context.Context, request appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	view, err := b.State(ctx)
	if err != nil {
		return appwire.SessionOperationResponse{}, err
	}
	return b.owner.Invoke(ctx, view, request)
}

func (o *sessionOperations) Invoke(ctx context.Context, caller appwire.ManagerView, request appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	var response appwire.SessionOperationResponse
	if err := request.Validate(); err != nil {
		return response, err
	}
	// caller was resolved from the authenticated connection, never tool arguments.
	// Read durable state again at the owner boundary, rather than trusting its
	// previously projected enabled bit.
	thread := appwire.Thread{Evener: appwire.EvenerThread{Ref: caller.Ref, InstanceID: caller.InstanceID}}
	key, err := managerPreferenceRef(o.cfg, thread)
	if err != nil {
		return response, err
	}
	setting, err := o.cfg.SessionPreferences.Manager(key)
	if err != nil {
		return response, err
	}
	hubID, err := o.cfg.SessionPreferences.HubID()
	if err != nil {
		return response, err
	}
	if !setting.Enabled || setting.HubID != hubID {
		return response, appwire.InvalidRequest("Manager mode is not enabled")
	}
	response.Actor = appwire.SessionActor{Kind: "manager", ManagerRef: caller.Ref}
	if request.Discover != nil {
		threads, err := hubThreadList(ctx, o.cfg, o.sources, *request.Discover)
		if err != nil {
			return response, err
		}
		discovery := &appwire.SessionDiscovery{Threads: threads, Sources: []appwire.SessionSource{}}
		for _, source := range o.sources.All() {
			discovery.Sources = append(discovery.Sources, appwire.SessionSource{ID: source.ID()})
		}
		response.Discover = discovery
		for _, thread := range threads.Data {
			response.FollowUps = append(response.FollowUps, statusFollowUp(thread.Evener.Ref))
		}
	} else {
		read, err := o.read(ctx, *request.Read, false)
		if err != nil {
			return response, err
		}
		response.Read = &read
		response.FollowUps = []appwire.SessionStatusFollowUp{statusFollowUp(read.Thread.Evener.Ref)}
	}
	return response, nil
}

func statusFollowUp(ref string) appwire.SessionStatusFollowUp {
	return appwire.SessionStatusFollowUp{Tool: "job_status", Arguments: appwire.SessionStatusArguments{Hub: appwire.SessionStatusTarget{Ref: ref}}}
}

func (o *sessionOperations) resyncThread(thread appwire.Thread, key string) {
	o.mu.Lock()
	for watch := range o.watches {
		if watch.preferenceKey == key {
			watch.cancel()
		}
	}
	o.mu.Unlock()
	o.relays.resyncThread(thread, key)
}

func (b *boundSessionOperations) Watch(ctx context.Context, request appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	view, err := b.State(ctx)
	if err != nil {
		return nil, err
	}
	if !view.Setting.Enabled {
		return nil, appwire.InvalidRequest("Manager mode is not enabled")
	}
	for _, event := range request.Events {
		if event != "status" {
			return nil, appwire.InvalidParams("only status observation is implemented")
		}
	}
	if request.Completion {
		return nil, appwire.InvalidParams("completion observation is not implemented")
	}
	ref, err := appwire.ParseRef(request.Ref)
	if err != nil {
		return nil, appwire.InvalidParams(err.Error())
	}
	source, err := b.owner.sources.SourceForRef(ref.String())
	if err != nil {
		return nil, err
	}
	key, err := managerPreferenceRef(b.owner.cfg, appwire.Thread{Evener: appwire.EvenerThread{Ref: view.Ref, InstanceID: view.InstanceID}})
	if err != nil {
		return nil, err
	}
	watchCtx, cancel := context.WithCancel(ctx)
	observation := &sessionObservation{preferenceKey: key, cancel: cancel}
	b.owner.mu.Lock()
	if b.owner.watches == nil {
		b.owner.watches = map[*sessionObservation]struct{}{}
	}
	b.owner.watches[observation] = struct{}{}
	b.owner.mu.Unlock()
	cleanup := func() { cancel(); b.owner.mu.Lock(); delete(b.owner.watches, observation); b.owner.mu.Unlock() }
	current, err := b.State(watchCtx)
	if err != nil {
		cleanup()
		return nil, err
	}
	if !current.Setting.Enabled {
		cleanup()
		return nil, appwire.InvalidRequest("Manager mode is not enabled")
	}
	read, err := b.owner.read(watchCtx, appwire.ThreadReadParams{Ref: ref.String()}, false)
	if err != nil {
		cleanup()
		return nil, err
	}
	notifications, err := source.SubscribeThread(watchCtx, appwire.ThreadReadParams{Ref: ref.String(), Subscribe: true})
	if err != nil {
		cleanup()
		return nil, err
	}
	updates := make(chan appwire.SessionWatchUpdate, 1)
	updates <- appwire.SessionWatchUpdate{Snapshot: &read, Metadata: appwire.SessionObservationMetadata{Ref: read.Thread.Evener.Ref, InstanceID: read.Thread.Evener.InstanceID, GenerationID: read.BootGeneration}}
	go func() {
		defer close(updates)
		defer cleanup()
		for {
			select {
			case <-watchCtx.Done():
				return
			case notification, ok := <-notifications:
				if !ok {
					return
				}
				if notification.Method != appwire.NotifyThreadStatusChanged && notification.Method != appwire.NotifyEvenerThreadResync && notification.Method != appwire.NotifyThreadClosed {
					continue
				}
				current, err := b.State(watchCtx)
				if err != nil || !current.Setting.Enabled {
					return
				}
				update := appwire.SessionWatchUpdate{Notification: &notification, Metadata: appwire.SessionObservationMetadata{Ref: read.Thread.Evener.Ref, InstanceID: read.Thread.Evener.InstanceID, GenerationID: read.BootGeneration}}
				select {
				case updates <- update:
				case <-watchCtx.Done():
					return
				}
			}
		}
	}()
	return updates, nil
}

func (o *sessionOperations) Read(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return o.read(ctx, params, true)
}

func (o *sessionOperations) read(ctx context.Context, params appwire.ThreadReadParams, capture bool) (appwire.ThreadReadResponse, error) {
	cfg, sources, relays := o.cfg, o.sources, o.relays

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
				// Refresh may fail on an unrelated marker. Recheck the target
				// before allowing its saved, non-authoritative projection.
				_, required, ownershipErr = restartRequiredDaemon(ctx, cfg, params.Ref, params.ThreadID)
				if ctx.Err() != nil || (!required && !isDaemonDiscoveryError(ownershipErr)) {
					return appwire.ThreadReadResponse{}, appwire.Unavailable(err.Error())
				}
			}
		}
	}
	source, err := sourceForThreadWithDeletionFence(ctx, cfg, sources, params.Ref, params.ThreadID)
	if err != nil {
		if isTargetDeletedError(err) {
			return appwire.ThreadReadResponse{}, err
		}
		resp, ok, pastErr := unavailableThreadReadResponse(ctx, cfg, sources, params)
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
		allowPast := allowsPastFallbackAfterLiveReadFailure(source, params, err)
		if _, local := localPastThreadID(params); local && cfg.Roster != nil && daemonOwnershipMayHaveChanged(err) {
			refreshErr := hubRosterRefresh(ctx, cfg.Roster)
			_, required, ownershipErr := restartRequiredDaemon(ctx, cfg, params.Ref, params.ThreadID)
			if ctx.Err() != nil {
				return appwire.ThreadReadResponse{}, ctx.Err()
			}
			if required {
				allowPast = true
				err = daemonRestartRequiredError(ctx, cfg, params.Ref, params.ThreadID, "")
			} else if isDaemonDiscoveryError(ownershipErr) {
				allowPast = true
			} else {
				if refreshErr != nil {
					return appwire.ThreadReadResponse{}, appwire.Unavailable(refreshErr.Error())
				}
				if ownershipErr != nil {
					return appwire.ThreadReadResponse{}, appwire.Unavailable(ownershipErr.Error())
				}
			}
		}
		if allowPast {
			saved, ok, pastErr := unavailableThreadReadResponse(ctx, cfg, sources, params)
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

	// A past session (a daemon that does not own the ref answers with an
	// empty live thread) gets its turns from the windowed past item page,
	// so that page runs BEFORE the merge and the merge never asks for the
	// full past-turn projection. The old order computed the O(transcript)
	// full projection first and then replaced its turns with this window,
	// making every click on a long session pay for a projection it
	// discarded in the same request.
	var pastPage *appwire.ThreadReadResponse
	if params.IncludeTurns && liveItemCandidatesEmpty {
		past, ok, pastErr := pastThreadItemReadResponse(ctx, cfg, params)
		if pastErr != nil {
			read.finish(false)
			return appwire.ThreadReadResponse{}, daemonlessReadError(pastErr)
		}
		if ok {
			pastPage = &past
		}
	}
	// The merge still supplies past turns when the item page could not (a
	// live source with candidates but no turns, or no past entry at all),
	// preserving the old includePastTurns decision exactly.
	wantPastTurns := params.IncludeTurns && pastPage == nil && len(resp.Thread.Turns) == 0
	resp.Thread, err = mergePastThreadForRead(ctx, cfg, params, resp.Thread, wantPastTurns)
	resp.Thread = applyThreadResumeRequirement(ctx, cfg, params.Ref, params.ThreadID, resp.Thread)
	resp.Thread = applyHubCapabilities(cfg, resp.Thread)
	if err != nil {
		read.finish(false)
		return appwire.ThreadReadResponse{}, err
	}
	if params.IncludeTurns {
		if pastPage != nil {
			resp.Thread.Turns = pastPage.Thread.Turns
			resp.OlderCursor = pastPage.OlderCursor
			resp = withReadHistoryIdentity(resp, fallbackHistoryIdentity(read.response, *pastPage))
			resp.Thread = enrichSourcedThreadImages(source, resp.Thread)
			annotateThreadProjects([]appwire.Thread{resp.Thread})
		} else {
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
				response.Thread = enrichSourcedThreadImages(source, response.Thread)
				annotateThreadProjects([]appwire.Thread{response.Thread})
				return response, nil
			}, itemLimit)
			if packErr != nil {
				read.finish(false)
				return appwire.ThreadReadResponse{}, packErr
			}
			resp = withReadHistoryIdentity(packed, read.response)
			resp.Changes = withDroppedWindowItems(source, resp.Changes, candidates, resp.Thread)
		}
	} else {
		// A live daemon's turns carry sha-addressed tool-result descriptors with
		// no route on them (the daemon does not serve the bytes; this hub does),
		// so the route is stamped here before the file-backed pass adds any
		// /doc/image descriptors of its own.
		resp.Thread = enrichSourcedThreadImages(source, resp.Thread)
		annotateThreadProjects([]appwire.Thread{resp.Thread})
	}
	// Local forks copy persisted history in the hub. A live daemon's
	// own unsupported fork flag does not describe this hub-owned action.
	resp.Thread = applyHubCapabilities(cfg, resp.Thread)
	resp.RequestGeneration = params.RequestGeneration
	if err := appwire.ValidateThreadReadItemResponse(resp); err != nil {
		read.finish(false)
		return appwire.ThreadReadResponse{}, err
	}
	read.response = resp
	if !capture {
		if read.handoff != nil && (!read.handoff.Prepare() || !read.finish(true)) {
			read.finish(false)
			return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("thread snapshot is unavailable")
		}
		return resp, nil
	}
	if read.handoff != nil {
		if !relays.captureThreadRead(ctx, params, read) {
			return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("thread subscription is unavailable")
		}
	} else if sourceSupportsThreadRelay(source) && (params.Subscribe || relayOnThreadRead(source)) {
		// A source with no relay fan-out is never relayed, even for a
		// Subscribe read: startRelay calls SubscribeThread, so relaying one
		// would fail the read. Subscribing callers still get the snapshot.
		if err := relays.startRelay(ctx, source, params, resp.Thread); err != nil {
			return appwire.ThreadReadResponse{}, err
		}
	}
	return resp, nil
}
