package hub

import (
	"bytes"
	"context"
	"encoding/json"
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
	remote  map[string]*remoteSessionOwner
}

// remoteSessionOwner is a proxy to the enabling hub's reverse-call owner on
// this identified bridge, not an independent hosting-hub Manager authority.
type remoteSessionOwner struct {
	hubID   string
	owner   appwire.SessionHubClient
	ctx     context.Context
	binding appwire.SessionBinding
}

func (r *remoteSessionOwner) SessionToolsHubID() string     { return r.hubID }
func (r *remoteSessionOwner) SessionToolsBindingID() string { return r.binding.BindingID }
func (r *remoteSessionOwner) State(ctx context.Context) (appwire.ManagerView, error) {
	if r.ctx.Err() != nil {
		return appwire.ManagerView{}, appwire.Unavailable("enabling hub disconnected")
	}
	return r.owner.State(ctx)
}
func (r *remoteSessionOwner) Invoke(ctx context.Context, request appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	if r.ctx.Err() != nil {
		return appwire.SessionOperationResponse{}, appwire.Unavailable("enabling hub disconnected")
	}
	return r.owner.Invoke(ctx, request)
}
func (r *remoteSessionOwner) Watch(ctx context.Context, request appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	if r.ctx.Err() != nil {
		return nil, appwire.Unavailable("enabling hub disconnected")
	}
	return r.owner.Watch(ctx, request)
}

type sessionObservation struct {
	preferenceKey string
	cancel        context.CancelFunc
}

type boundSessionOperations struct {
	owner   *sessionOperations
	local   *appsource.LocalDaemonSource
	remote  *appsource.RemoteHubSource
	binding appwire.SessionBinding
}

type sessionBindingContextKey struct{}
type remoteSessionAdmissionKey struct{}

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
	if o.cfg.SessionPreferences == nil {
		return
	}
	o.sources.SetOnSessionToolsAdd(o.installSource)
	for _, source := range o.sources.All() {
		o.installSource(source)
	}
	o.server.InstallSessionTools(func(ctx context.Context, binding appwire.SessionBinding, upstream appwire.SessionHubClient) error {
		peer := appserver.SessionToolsPeerID(ctx)
		if peer == "" || peer != binding.HubID {
			return appwire.InvalidRequest("identified enabling peer required")
		}
		source, _ := o.sources.Source("local")
		local, ok := source.(*appsource.LocalDaemonSource)
		if !ok {
			return appwire.Unavailable("hosting daemon source unavailable")
		}
		thread, err := local.SessionToolsThread(binding)
		if err != nil {
			return err
		}
		key, err := managerPreferenceRef(o.cfg, thread)
		if err != nil {
			return err
		}
		setting, err := o.cfg.SessionPreferences.Manager(key)
		if err != nil {
			return err
		}
		if !setting.Enabled || setting.HubID != peer || setting.Revision != binding.PreferenceRevision {
			return appwire.InvalidRequest("saved Manager authority differs from identified peer")
		}
		proxy := &remoteSessionOwner{hubID: peer, owner: upstream, ctx: ctx, binding: binding}
		o.mu.Lock()
		if o.remote == nil {
			o.remote = map[string]*remoteSessionOwner{}
		}
		if previous := o.remote[binding.Ref]; previous != nil && previous.ctx.Err() == nil {
			o.mu.Unlock()
			return appwire.Conflict("workspace already has an enabling hub binding")
		}
		o.remote[binding.Ref] = proxy
		o.mu.Unlock()
		local.ReconcileSessionTools()
		if err := local.AwaitSessionToolsBinding(ctx, binding); err != nil {
			o.mu.Lock()
			if o.remote[binding.Ref] == proxy {
				delete(o.remote, binding.Ref)
			}
			o.mu.Unlock()
			local.ReconcileSessionTools()
			return appwire.Unavailable("Manager daemon binding not installed: " + err.Error())
		}
		go func() {
			<-ctx.Done()
			o.mu.Lock()
			if o.remote[binding.Ref] == proxy {
				delete(o.remote, binding.Ref)
			}
			o.mu.Unlock()
			local.ReconcileSessionTools()
		}()
		return nil
	})
}

func (o *sessionOperations) installSource(source appsource.Source) {
	if remote, ok := source.(*appsource.RemoteHubSource); ok {
		remote.SetSessionTools(func(binding appwire.SessionBinding) (appwire.SessionHubClient, error) {
			return &boundSessionOperations{owner: o, remote: remote, binding: binding}, nil
		}, func() (string, error) { return o.cfg.SessionPreferences.HubID() })
	}
	if local, ok := source.(*appsource.LocalDaemonSource); ok {
		local.SetSessionTools(func(binding appwire.SessionBinding) (appwire.SessionHubClient, error) {
			if o.cfg.SessionPreferences == nil {
				return nil, appwire.Unavailable("session preferences unavailable")
			}
			hubID, err := o.cfg.SessionPreferences.HubID()
			if err != nil {
				return nil, err
			}
			thread, err := local.SessionToolsThread(binding)
			if err != nil {
				return nil, err
			}
			key, err := managerPreferenceRef(o.cfg, thread)
			if err != nil {
				return nil, err
			}
			setting, err := o.cfg.SessionPreferences.Manager(key)
			if err != nil {
				return nil, err
			}
			if setting.Enabled && setting.HubID != hubID {
				o.mu.Lock()
				remote := o.remote[binding.Ref]
				o.mu.Unlock()
				if remote == nil || remote.hubID != setting.HubID || remote.ctx.Err() != nil {
					return nil, appwire.Unavailable("enabling hub binding unavailable")
				}
				return remote, nil
			}
			binding.HubID = hubID
			return &boundSessionOperations{owner: o, local: local, binding: binding}, nil
		}, func() (string, error) { return o.cfg.SessionPreferences.HubID() })
		local.SetSessionToolsAuthority(func(binding appwire.SessionBinding) (string, string, error) {
			thread, err := local.SessionToolsThread(binding)
			if err != nil {
				return "", "", err
			}
			key, err := managerPreferenceRef(o.cfg, thread)
			if err != nil {
				return "", "", err
			}
			setting, err := o.cfg.SessionPreferences.Manager(key)
			if err != nil {
				return "", "", err
			}
			id, err := o.cfg.SessionPreferences.HubID()
			if err != nil {
				return "", "", err
			}
			if setting.Enabled && setting.HubID != id {
				o.mu.Lock()
				remote := o.remote[binding.Ref]
				o.mu.Unlock()
				if remote == nil || remote.hubID != setting.HubID || remote.ctx.Err() != nil {
					return "", "", nil
				}
				return remote.hubID, remote.binding.BindingID, nil
			}
			return id, "", nil
		})
	}
}

func registerSessionToolsPeer(server *appserver.Server, cfg hubcore.WebConfig) {
	appserver.HandleTyped(server.Router(), appwire.MethodHubSessionToolsIdentify, func(ctx context.Context, params appwire.SessionToolsPeerParams) (appwire.SessionToolsPeerResponse, error) {
		var out appwire.SessionToolsPeerResponse
		if hostRoutingOrigin(ctx) != hostRoutingOriginBridge || cfg.SessionPreferences == nil {
			return out, appwire.InvalidRequest("authenticated hub bridge required for peer identity")
		}
		id, err := cfg.SessionPreferences.HubID()
		if err != nil {
			return out, appwire.Unavailable(err.Error())
		}
		if err := appserver.IdentifySessionToolsPeer(ctx, params.HubID); err != nil {
			return out, err
		}
		return appwire.SessionToolsPeerResponse{HubID: id}, nil
	})
}

func (o *sessionOperations) registerRemoteInput() {
	appserver.HandleTyped(o.server.Router(), appwire.MethodHubSessionToolsInput, func(ctx context.Context, input appwire.SessionToolsInput) (any, error) {
		if hostRoutingOrigin(ctx) != hostRoutingOriginBridge {
			return nil, appwire.InvalidRequest("identified hub bridge required")
		}
		binding, ok := appserver.SessionToolsPeerBinding(ctx, input.BindingID)
		if !ok || binding.HubID != appserver.SessionToolsPeerID(ctx) {
			return nil, appwire.InvalidRequest("unknown peer Manager binding")
		}
		o.mu.Lock()
		peer := o.remote[binding.Ref]
		o.mu.Unlock()
		if peer == nil || peer.ctx.Err() != nil || peer.binding != binding {
			return nil, appwire.InvalidRequest("Manager binding replaced")
		}
		source, _ := o.sources.Source("local")
		local, ok := source.(*appsource.LocalDaemonSource)
		if !ok {
			return nil, appwire.Unavailable("hosting daemon source unavailable")
		}
		thread, err := local.SessionToolsThread(binding)
		if err != nil {
			return nil, err
		}
		key, err := managerPreferenceRef(o.cfg, thread)
		if err != nil {
			return nil, err
		}
		setting, err := o.cfg.SessionPreferences.Manager(key)
		if err != nil {
			return nil, err
		}
		if !setting.Enabled || setting.HubID != binding.HubID {
			return nil, appwire.InvalidRequest("Manager authority revoked")
		}
		proof := &appwire.SessionActorProof{CallerRef: binding.Ref, Actor: appwire.SessionActor{Kind: "manager"}}
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
	if b.remote != nil {
		read, err := b.remote.ReadThread(ctx, appwire.ThreadReadParams{Ref: b.binding.Ref})
		if err != nil {
			return view, err
		}
		thread := read.Thread
		if thread.Evener.Ref != b.binding.Ref || thread.Evener.InstanceID != b.binding.InstanceID || thread.Evener.ManagerSetting == nil {
			return view, appwire.SessionUnavailable("remote workspace binding or preference changed")
		}
		setting := *thread.Evener.ManagerSetting
		if setting.Enabled && setting.HubID != b.binding.HubID {
			return view, appwire.Conflict("another hub owns Manager authority")
		}
		return appwire.ManagerView{Ref: thread.Evener.Ref, InstanceID: thread.Evener.InstanceID, Setting: setting}, nil
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
	ctx = context.WithValue(ctx, sessionBindingContextKey{}, b.binding)
	if b.remote != nil {
		ctx = context.WithValue(ctx, remoteSessionAdmissionKey{}, b.binding)
		ctx = appsource.WithManagerInputBinding(ctx, b.binding.BindingID)
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
	hubID, err := o.cfg.SessionPreferences.HubID()
	if err != nil {
		return response, err
	}
	setting := caller.Setting
	if binding, remote := ctx.Value(remoteSessionAdmissionKey{}).(appwire.SessionBinding); remote {
		if binding.Ref != caller.Ref || binding.InstanceID != caller.InstanceID || binding.HubID != hubID {
			return response, appwire.InvalidRequest("remote Manager authority changed")
		}
	} else {
		thread := appwire.Thread{Evener: appwire.EvenerThread{Ref: caller.Ref, InstanceID: caller.InstanceID}}
		key, err := managerPreferenceRef(o.cfg, thread)
		if err != nil {
			return response, err
		}
		setting, err = o.cfg.SessionPreferences.Manager(key)
		if err != nil {
			return response, err
		}
	}
	if !setting.Enabled || setting.HubID != hubID {
		return response, appwire.InvalidRequest("Manager mode is not enabled")
	}
	response.Actor = appwire.SessionActor{Kind: "manager", ManagerRef: caller.Ref}
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
		proof, err := sessionInputProof(ctx, caller.Ref)
		if err != nil {
			return response, err
		}
		params := *request.Start
		params.ManagerProof = proof
		start, err := o.Start(ctx, params)
		if err != nil {
			return response, err
		}
		response.Start = &start
	} else if request.Queue != nil {
		proof, err := sessionInputProof(ctx, caller.Ref)
		if err != nil {
			return response, err
		}
		params := *request.Queue
		params.ManagerProof = proof
		queue, err := o.Queue(ctx, params)
		if err != nil {
			return response, err
		}
		response.Queue = &queue
	} else if request.Steer != nil {
		proof, err := sessionInputProof(ctx, caller.Ref)
		if err != nil {
			return response, err
		}
		params := *request.Steer
		params.ManagerProof = proof
		steer, err := o.Steer(ctx, params)
		if err != nil {
			return response, err
		}
		response.Steer = &steer
	} else if request.Control != nil {
		proof, err := sessionInputProof(ctx, caller.Ref)
		if err != nil {
			return response, err
		}
		params := *request.Control
		approval := *params.Approval
		approval.ManagerProof = proof
		params.Approval = &approval
		control, err := o.Control(ctx, params)
		if err != nil {
			return response, err
		}
		control.Actor = response.Actor
		response.Control = &control
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

func (o *sessionOperations) resyncThread(thread appwire.Thread, key string) {
	o.mu.Lock()
	for watch := range o.watches {
		if watch.preferenceKey == key {
			watch.cancel()
		}
	}
	o.mu.Unlock()
	o.relays.resyncThread(thread, key)
	reconcileSessionTools(o.sources)
}

func (o *sessionOperations) Read(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	// The upstream relay may still need a snapshot handoff, but only a caller
	// that requested a subscription owns downstream admission and delivery.
	return o.read(ctx, params, params.Subscribe)
}

func (o *sessionOperations) read(ctx context.Context, params appwire.ThreadReadParams, capture bool) (appwire.ThreadReadResponse, error) {
	return o.readWithHandoff(ctx, params, capture, nil)
}

// beforeCommit installs a private listener while the canonical read still owns
// the snapshot cut. Human reads retain their existing admission/publication path.
func (o *sessionOperations) readWithHandoff(ctx context.Context, params appwire.ThreadReadParams, capture bool, beforeCommit func() error) (appwire.ThreadReadResponse, error) {
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
		if read.handoff != nil {
			if !read.handoff.Prepare() {
				read.finish(false)
				return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("thread snapshot is unavailable")
			}
			if beforeCommit != nil {
				if err := beforeCommit(); err != nil {
					read.finish(false)
					return appwire.ThreadReadResponse{}, err
				}
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
