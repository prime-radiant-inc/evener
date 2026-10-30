package hub

import (
	"bytes"
	"context"
	"encoding/json"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

type sessionOperations struct {
	cfg     hubcore.WebConfig
	sources *appsource.Registry
	// hubID is this hub's durable nonsecret identity, from the identity
	// store. It names the hub in session-tools bindings and the bridge
	// identify exchange; it carries no authority.
	hubID      string
	navigation *NavigationService
	resolve    topLevelSessionResolver
	relays     *hubRelay
	server     *appserver.Server
}

type boundSessionOperations struct {
	owner   *sessionOperations
	local   *appsource.LocalDaemonSource
	binding appwire.SessionBinding
}

type sessionBindingContextKey struct{}

func sessionInputProof(ctx context.Context, callerRef string) (*appwire.SessionActorProof, error) {
	binding, ok := ctx.Value(sessionBindingContextKey{}).(appwire.SessionBinding)
	if !ok || binding.BindingID == "" || binding.Ref != callerRef {
		return nil, appwire.InvalidRequest("authenticated Manager binding is required")
	}
	// The target source fills BindingID from the connection it actually uses.
	// This binding only authenticates the caller at the hub boundary.
	return &appwire.SessionActorProof{CallerRef: callerRef, Actor: appwire.SessionActor{Kind: "manager"}}, nil
}

func (o *sessionOperations) install() {
	if o.hubID == "" {
		return
	}
	o.sources.SetOnSessionToolsAdd(o.installSource)
	for _, source := range o.sources.All() {
		o.installSource(source)
	}
}

func (o *sessionOperations) installSource(source appsource.Source) {
	if local, ok := source.(*appsource.LocalDaemonSource); ok {
		local.SetSessionTools(func(binding appwire.SessionBinding) (appwire.SessionHubClient, error) {
			// Ordinary workers need a binding to authenticate incoming Manager
			// input. Outbound operations still check the caller's Manager bit.
			_, err := local.SessionToolsThread(binding)
			if err != nil {
				return nil, err
			}
			binding.HubID = o.hubID
			return &boundSessionOperations{owner: o, local: local, binding: binding}, nil
		}, func() (string, error) { return o.hubID, nil })
	}
}

func registerSessionToolsPeer(server *appserver.Server, hubID string) {
	appserver.HandleTyped(server.Router(), appwire.MethodHubSessionToolsIdentify, func(ctx context.Context, params appwire.SessionToolsPeerParams) (appwire.SessionToolsPeerResponse, error) {
		var out appwire.SessionToolsPeerResponse
		if hostRoutingOrigin(ctx) != hostRoutingOriginBridge || hubID == "" {
			return out, appwire.InvalidRequest("authenticated hub bridge required for peer identity")
		}
		if err := appserver.IdentifySessionToolsPeer(ctx, params.HubID); err != nil {
			return out, err
		}
		return appwire.SessionToolsPeerResponse{HubID: hubID}, nil
	})
}

func (o *sessionOperations) registerRemoteInput() {
	appserver.HandleTyped(o.server.Router(), appwire.MethodHubSessionToolsInput, func(ctx context.Context, input appwire.SessionToolsInput) (any, error) {
		if hostRoutingOrigin(ctx) != hostRoutingOriginBridge {
			return nil, appwire.InvalidRequest("identified hub bridge required")
		}
		peer := appserver.SessionToolsPeerID(ctx)
		if peer == "" || peer == "local" || !appwire.ValidRefPart(peer) {
			return nil, appwire.InvalidRequest("identified hub bridge required")
		}
		caller, err := appwire.ParseRef(input.CallerRef)
		if err != nil || caller.SourceID != "local" {
			return nil, appwire.InvalidParams("bridge caller must be a local Manager ref")
		}
		// The authenticated peer admits its own Manager. Qualify that caller so
		// the target's saved actor cannot be mistaken for a session on this hub.
		caller.SourceID = peer
		proof := &appwire.SessionActorProof{CallerRef: caller.String(), Actor: appwire.SessionActor{Kind: "manager"}}
		decode := func(out any) error {
			decoder := json.NewDecoder(bytes.NewReader(input.Params))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(out); err != nil {
				return appwire.InvalidParams(err.Error())
			}
			return nil
		}
		switch input.Method {
		case appwire.MethodTurnStart:
			var p appwire.TurnStartParams
			if err := decode(&p); err != nil {
				return nil, err
			}
			if p.ManagerProof != nil {
				return nil, appwire.InvalidParams("actor metadata is not a bridge argument")
			}
			p.ManagerProof = proof
			return o.Start(ctx, p)
		case appwire.MethodTurnQueue:
			var p appwire.TurnQueueParams
			if err := decode(&p); err != nil {
				return nil, err
			}
			if p.ManagerProof != nil {
				return nil, appwire.InvalidParams("actor metadata is not a bridge argument")
			}
			p.ManagerProof = proof
			return o.Queue(ctx, p)
		case appwire.MethodTurnSteer:
			var p appwire.TurnSteerParams
			if err := decode(&p); err != nil {
				return nil, err
			}
			if p.ManagerProof != nil {
				return nil, appwire.InvalidParams("actor metadata is not a bridge argument")
			}
			p.ManagerProof = proof
			return o.Steer(ctx, p)
		case appwire.MethodTurnDrainAsSteer:
			var p appwire.TurnDrainAsSteerParams
			if err := decode(&p); err != nil {
				return nil, err
			}
			if p.ManagerProof != nil {
				return nil, appwire.InvalidParams("actor metadata is not a bridge argument")
			}
			p.ManagerProof = proof
			return drainQueueControl(ctx, o.cfg, o.sources, p)
		case appwire.MethodTurnPromoteQueuedAsSteer:
			var p appwire.TurnPromoteQueuedAsSteerParams
			if err := decode(&p); err != nil {
				return nil, err
			}
			if p.ManagerProof != nil {
				return nil, appwire.InvalidParams("actor metadata is not a bridge argument")
			}
			p.ManagerProof = proof
			return promoteQueuedControl(ctx, o.cfg, o.sources, p)
		case appwire.MethodEvenerSandboxEscalationResolve:
			var p appwire.SandboxEscalationResolveParams
			if err := decode(&p); err != nil {
				return nil, err
			}
			if p.ManagerProof != nil {
				return nil, appwire.InvalidParams("actor metadata is not a bridge argument")
			}
			p.ManagerProof = proof
			return o.resolveApproval(ctx, p)
		default:
			return nil, appwire.MethodNotFound(input.Method)
		}
	})
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
	return b.localCaller()
}

func (b *boundSessionOperations) localCaller() (appwire.ManagerView, error) {
	thread, err := b.local.SessionToolsThread(b.binding)
	if err != nil {
		return appwire.ManagerView{}, err
	}
	return appwire.ManagerView{Ref: thread.Evener.Ref, InstanceID: thread.Evener.InstanceID, Manager: thread.Evener.Manager}, nil
}

func (b *boundSessionOperations) Invoke(ctx context.Context, request appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	view, err := b.State(ctx)
	if err != nil {
		return appwire.SessionOperationResponse{}, err
	}
	ctx = context.WithValue(ctx, sessionBindingContextKey{}, b.binding)
	return b.owner.Invoke(ctx, view, request)
}

func (o *sessionOperations) Invoke(ctx context.Context, caller appwire.ManagerView, request appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	var response appwire.SessionOperationResponse
	if err := request.Validate(); err != nil {
		return response, err
	}
	// The authenticated connection resolves the caller and its start-time
	// Manager property from the daemon, never from tool arguments.
	if !caller.Manager {
		return response, appwire.InvalidRequest("session is not a Manager")
	}
	ctx = appsource.WithManagerInputCaller(ctx, caller.Ref)
	response.Actor = appwire.SessionActor{Kind: "manager", ManagerRef: caller.Ref}
	var proof *appwire.SessionActorProof
	var err error
	if request.Start != nil || request.Queue != nil || request.Steer != nil || request.Control != nil {
		proof, err = sessionInputProof(ctx, caller.Ref)
		if err != nil {
			return response, err
		}
	}
	if request.Discover != nil {
		discovery, err := o.discover(ctx, *request.Discover)
		if err != nil {
			return response, err
		}
		response.Discover = &discovery
		for _, thread := range discovery.Threads.Data {
			response.FollowUps = append(response.FollowUps, statusFollowUp(thread.Evener.Ref))
		}
	} else if request.Read != nil {
		read, err := o.read(ctx, *request.Read, false)
		if err != nil {
			return response, err
		}
		response.Read = &read
		response.FollowUps = []appwire.SessionStatusFollowUp{statusFollowUp(read.Thread.Evener.Ref)}
	} else if request.Turns != nil {
		page, err := o.turns(ctx, *request.Turns)
		if err != nil {
			return response, err
		}
		response.Turns = &page
		if page.NextCursor != "" {
			response.FollowUps = []appwire.SessionStatusFollowUp{{Tool: "read_transcript", Arguments: appwire.SessionStatusArguments{Hub: appwire.SessionStatusTarget{Ref: request.Turns.Ref, Cursor: page.NextCursor, ItemLimit: request.Turns.ItemLimit}}}}
		}
	} else if request.Activity != nil {
		page, err := o.activity(ctx, *request.Activity)
		if err != nil {
			return response, err
		}
		response.Activity = &page
		cursor := ""
		if page.Delegates != nil {
			cursor = page.Delegates.Page.NextCursor
		} else if page.Jobs != nil {
			cursor = page.Jobs.Page.NextCursor
		} else if page.Watches != nil {
			cursor = page.Watches.Page.NextCursor
		}
		if cursor != "" {
			response.FollowUps = []appwire.SessionStatusFollowUp{{Tool: "job_list", Arguments: appwire.SessionStatusArguments{Hub: appwire.SessionStatusTarget{Mode: "activity", Ref: request.Activity.Ref, Resource: request.Activity.Resource, Scope: request.Activity.Scope, Cursor: cursor, Limit: request.Activity.Limit}}}}
		}
	} else if request.Tasks != nil {
		page, err := o.tasks(ctx, *request.Tasks)
		if err != nil {
			return response, err
		}
		response.Tasks = &page
		if page.NextOffset != nil {
			response.FollowUps = []appwire.SessionStatusFollowUp{{Tool: "job_list", Arguments: appwire.SessionStatusArguments{Hub: appwire.SessionStatusTarget{Mode: "tasks", Ref: request.Tasks.Ref, Offset: *page.NextOffset, Limit: request.Tasks.Limit, Snapshot: page.Snapshot}}}}
		}
	} else if request.Job != nil {
		job, err := hubJobsGet(ctx, o.cfg, o.sources, *request.Job)
		if err != nil {
			return response, err
		}
		response.Job = &job
		response.FollowUps = []appwire.SessionStatusFollowUp{{Tool: "read_transcript", Arguments: appwire.SessionStatusArguments{Hub: appwire.SessionStatusTarget{Ref: request.Job.Ref, JobID: request.Job.JobID}}}}
	} else if request.JobOutput != nil {
		output, err := hubJobsOutput(ctx, o.cfg, o.sources, *request.JobOutput)
		if err != nil {
			return response, err
		}
		if output.Data.OffsetBytes < output.Data.RetainedStartBytes {
			return response, appwire.SessionUnavailable("job output page precedes retained start")
		}
		response.JobOutput = &output
		if output.Data.OffsetBytes > output.Data.RetainedStartBytes {
			before := output.Data.OffsetBytes
			response.FollowUps = []appwire.SessionStatusFollowUp{{Tool: "read_transcript", Arguments: appwire.SessionStatusArguments{Hub: appwire.SessionStatusTarget{Ref: request.JobOutput.Ref, JobID: request.JobOutput.JobID, BeforeBytes: &before, MaxBytes: request.JobOutput.MaxBytes}}}}
		}
	} else if request.Models != nil {
		models, err := hubModelList(ctx, o.cfg, o.sources, *request.Models)
		if err != nil {
			return response, err
		}
		response.Models = &models
	} else if request.Create != nil {
		created, err := hubThreadStart(ctx, o.cfg, o.sources, *request.Create)
		if err != nil {
			return response, err
		}
		response.Create = &created
	} else if request.Start != nil {
		params := *request.Start
		params.ManagerProof = proof
		start, err := o.Start(ctx, params)
		if err != nil {
			return response, err
		}
		response.Start = &start
	} else if request.Queue != nil {
		params := *request.Queue
		params.ManagerProof = proof
		queue, err := o.Queue(ctx, params)
		if err != nil {
			return response, err
		}
		response.Queue = &queue
	} else if request.Steer != nil {
		params := *request.Steer
		params.ManagerProof = proof
		steer, err := o.Steer(ctx, params)
		if err != nil {
			return response, err
		}
		response.Steer = &steer
	} else if request.Control != nil {
		params := *request.Control
		if params.Approval != nil {
			approval := *params.Approval
			approval.ManagerProof = proof
			params.Approval = &approval
		}
		if params.DrainQueue != nil {
			if params.DrainQueue.ManagerProof != nil {
				return response, appwire.InvalidParams("actor metadata is not a control argument")
			}
			drain := *params.DrainQueue
			drain.ManagerProof = proof
			params.DrainQueue = &drain
		}
		if params.PromoteQueued != nil {
			if params.PromoteQueued.ManagerProof != nil {
				return response, appwire.InvalidParams("actor metadata is not a control argument")
			}
			promote := *params.PromoteQueued
			promote.ManagerProof = proof
			params.PromoteQueued = &promote
		}
		control, err := o.Control(ctx, params)
		if err != nil {
			return response, err
		}
		control.Actor = response.Actor
		if control.Clear != nil {
			control.Clear.Receipt.Actor = response.Actor
		}
		if control.DrainQueue != nil {
			control.DrainQueue.Receipt.Actor = response.Actor
		}
		if control.PromoteQueued != nil {
			control.PromoteQueued.Receipt.Actor = response.Actor
		}
		if control.CancelQueued != nil {
			control.CancelQueued.Receipt.Actor = response.Actor
		}
		if control.HumanNotes != nil {
			control.HumanNotes.Receipt.Actor = response.Actor
		}
		response.Control = &control
	} else if request.Stop != nil {
		stop, err := o.Stop(ctx, *request.Stop)
		if err != nil {
			return response, err
		}
		stop.Actor = response.Actor
		if stop.Interrupt != nil {
			stop.Interrupt.Receipt.Actor = response.Actor
		}
		response.Stop = &stop
	}
	if request.Discover != nil || request.Read != nil || request.Turns != nil || request.Activity != nil || request.Tasks != nil || request.Job != nil || request.JobOutput != nil || request.Models != nil {
		if err := managerReadWithinCap(response); err != nil {
			return appwire.SessionOperationResponse{}, err
		}
	}
	return response, nil
}

func statusFollowUp(ref string) appwire.SessionStatusFollowUp {
	return appwire.SessionStatusFollowUp{Tool: "job_status", Arguments: appwire.SessionStatusArguments{Hub: appwire.SessionStatusTarget{Ref: ref}}}
}

func (o *sessionOperations) Read(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	// The upstream relay may still need a snapshot handoff, but only a caller
	// that requested a subscription owns downstream admission and delivery.
	return o.read(ctx, params, params.Subscribe)
}

func (o *sessionOperations) read(ctx context.Context, params appwire.ThreadReadParams, capture bool) (appwire.ThreadReadResponse, error) {
	cfg, sources, relays := o.cfg, o.sources, o.relays
	if err := params.ValidateAnswerContinuation(); err != nil {
		return appwire.ThreadReadResponse{}, err
	}
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
	// Deletion is authoritative even when unrelated daemon ownership
	// cannot be discovered. Keep the locked source check below as well:
	// deletion may begin after this early terminal check.
	if err := deletionFenceError(cfg, params.Ref, params.ThreadID, ""); err != nil {
		return appwire.ThreadReadResponse{}, err
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
		recovered := false
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
				// Discovery can publish a child that the first read could not
				// route. Re-resolve under the deletion fence, then use the same
				// live read and handoff once, without refreshing again on failure.
				refreshedSource, sourceErr := sourceForThreadWithDeletionFence(ctx, cfg, sources, params.Ref, params.ThreadID)
				if ctx.Err() != nil {
					return appwire.ThreadReadResponse{}, ctx.Err()
				}
				if isTargetDeletedError(sourceErr) {
					return appwire.ThreadReadResponse{}, sourceErr
				}
				if sourceErr != nil {
					err = sourceErr
				} else if daemon, ok := refreshedSource.(*appsource.LocalDaemonSource); ok {
					source = refreshedSource
					_, err = daemon.ResolveSubscriptionAdmission(params)
					if err == nil {
						read, err = relays.readThread(ctx, source, params)
						recovered = err == nil
					}
					if ctx.Err() != nil {
						if read != nil {
							read.finish(false)
						}
						return appwire.ThreadReadResponse{}, ctx.Err()
					}
					if isTargetDeletedError(err) {
						return appwire.ThreadReadResponse{}, err
					}
				}
			}
		}
		// Keep the first attempt's saved-history eligibility if the
		// discovered owner disappears before the retry lands.
		if !recovered && allowPast {
			saved, ok, pastErr := unavailableThreadReadResponse(ctx, cfg, sources, params)
			if pastErr != nil {
				return appwire.ThreadReadResponse{}, pastErr
			}
			if ok {
				return saved, nil
			}
		}
		if !recovered {
			return appwire.ThreadReadResponse{}, err
		}
	}
	resp := read.response
	liveItemCandidatesEmpty := false
	if params.IncludeTurns {
		if candidates, candidateErr := itemCandidateResultFromReadResponse(resp); candidateErr == nil {
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
			candidates, candidateErr := sourceItemCandidateResultForRead(ctx, source, params, resp)
			if candidateErr != nil {
				read.finish(false)
				return appwire.ThreadReadResponse{}, candidateErr
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
		if read.handoff != nil {
			if !read.handoff.Prepare() {
				read.finish(false)
				return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("thread snapshot is unavailable")
			}
			if !read.finish(true) {
				return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("thread snapshot is unavailable")
			}
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
