package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/transcriptindex"
)

type sessionOperations struct {
	cfg     hubcore.WebConfig
	sources *appsource.Registry
	// hubID is this hub's durable nonsecret identity, from the identity
	// store. It names the hub in session-tools bindings and the bridge
	// identify exchange; it carries no authority.
	hubID      string
	navigation *NavigationService
	roster     attentionBroker // fallback for standalone AppRPC servers without navigation
	resolve    topLevelSessionResolver
	relays     *hubRelay
	server     *appserver.Server

	canonicalItemsOnce sync.Once
	canonicalItems     *transcriptCanonicalStore
}

func (o *sessionOperations) canonicalItemStore() *transcriptCanonicalStore {
	o.canonicalItemsOnce.Do(func() {
		o.canonicalItems = newTranscriptCanonicalStore(transcriptCanonicalStoreMaxBytes)
	})
	return o.canonicalItems
}

func (o *sessionOperations) canonicalWindowOptions(
	source appsource.Source,
	base appwire.Thread,
	result transcriptItemCandidateResult,
	params appwire.ThreadWindowParams,
	itemLimit int,
	appliedBy string,
	origin appitempaging.FetchCursorOrigin,
) transcriptWindowPackOptions {
	options := transcriptWindowOptionsFor(result, params, itemLimit, appliedBy, origin)
	options.CanonicalizeCandidates = func(candidates []appitempaging.TranscriptItemCandidate) ([]appitempaging.TranscriptItemCandidate, error) {
		return canonicalizeTranscriptItemCandidates(source, base, candidates)
	}
	o.retainCanonicalWindowOptions(&options)
	return options
}

func (o *sessionOperations) retainCanonicalWindowOptions(options *transcriptWindowPackOptions) {
	store := o.canonicalItemStore()
	canonicalize := options.CanonicalizeCandidates
	type itemKey struct {
		position appwire.ThreadItemPosition
		version  uint64
	}
	encodedCursors := make(map[itemKey]string)
	mintFetchCursor := options.MintFetchCursor
	options.MintFetchCursor = func(candidate appitempaging.TranscriptItemCandidate, token string) (string, error) {
		key := itemKey{position: candidate.Position, version: candidate.Item.Version}
		if cursor := encodedCursors[key]; cursor != "" {
			return cursor, nil
		}
		cursor, err := mintFetchCursor(candidate, token)
		if err == nil {
			encodedCursors[key] = cursor
		}
		return cursor, err
	}
	options.CanonicalizeCandidates = func(candidates []appitempaging.TranscriptItemCandidate) ([]appitempaging.TranscriptItemCandidate, error) {
		canonical := candidates
		var err error
		if canonicalize != nil {
			canonical, err = canonicalize(candidates)
		}
		if err != nil {
			return nil, err
		}
		for index := range canonical {
			if canonical[index].Item.Version == 0 {
				continue
			}
			if options.fetchTokens == nil {
				continue
			}
			token, ok := store.retainedToken(fetchEntryTriple(options.fetchTokens.identity, canonical[index]))
			if !ok {
				continue
			}
			retained, ok := store.borrow(token)
			if !ok {
				continue
			}
			if err := json.Unmarshal(retained, &canonical[index].Item); err != nil {
				return nil, err
			}
		}
		return canonical, nil
	}
	options.RetainFetchItem = store.putItem
	if options.fetchTokens != nil {
		options.fetchTokens.assign = func(identity appitempaging.CursorIdentity, candidate appitempaging.TranscriptItemCandidate) (string, error) {
			return store.tokenFor(fetchEntryTriple(identity, candidate)), nil
		}
	}
}

func (o *sessionOperations) retainRemoteWindowItems(
	ctx context.Context,
	remote *appsource.RemoteHubSource,
	ref string,
	window appwire.Thread,
) error {
	hasFetchCursor := false
	for _, turn := range window.Turns {
		for _, item := range turn.Items {
			hasFetchCursor = hasFetchCursor || item.FetchCursor != ""
		}
	}
	if !hasFetchCursor {
		return nil
	}
	full, err := remote.ReadThread(ctx, appwire.ThreadReadParams{
		Ref: ref, IncludeTurns: true, ItemsView: string(appwire.TurnItemsViewFragment), ItemLimit: appwire.TranscriptItemPageLimit,
	})
	if err != nil {
		return err
	}
	full.Thread = enrichSourcedThreadImages(remote, full.Thread)
	byPosition := make(map[appwire.ThreadItemPosition]appwire.ThreadItem)
	for _, turn := range full.Thread.Turns {
		for _, item := range turn.Items {
			if item.Position != nil {
				byPosition[*item.Position] = item
			}
		}
	}
	store := o.canonicalItemStore()
	for turnIndex := range window.Turns {
		items := window.Turns[turnIndex].Items
		for itemIndex := range items {
			visible := &items[itemIndex]
			if visible.FetchCursor == "" {
				continue
			}
			cursor, identity, err := appitempaging.DecodeFetchCursorForRef(visible.FetchCursor, ref)
			if err != nil {
				return err
			}
			item, ok := byPosition[cursor.Position]
			if !ok {
				// The window can address an exact older item beyond the ordinary
				// inter-hub read's reach. Preserve that valid window; its fetch
				// repeats this origin check and returns fetch-unavailable-remote.
				continue
			}
			if item.Version != cursor.ItemVersion {
				// A change between the window and this retention read belongs to
				// the fetch-time version fence, not to the read that minted it.
				continue
			}
			key := store.tokenFor(remoteCanonicalTriple(identity, cursor.Token))
			// The owning hub's token is stable across re-retentions, so it cannot
			// tell this retained encoding from one enriched after an eviction.
			// The cursor carries the controller's own token instead, and the
			// fetch forwards the recorded remote token for the remote hop.
			store.retainRemoteToken(key, cursor.Token)
			if err := store.putItem(key, item); err != nil {
				return err
			}
			cursor.Token = key
			visible.FetchCursor, err = appitempaging.EncodeFetchCursor(identity, cursor)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// remoteCanonicalTriple names the controller-owned retention entry for a
// remote hub's fetch token. The remote mints that token for its own encoding,
// and the controller retains the enriched copy it measured under a token of
// its own, so an eviction or restart retires the cursor instead of reviving it
// against bytes enriched from a newer source item.
func remoteCanonicalTriple(identity appitempaging.CursorIdentity, token string) string {
	return "remote\x00" + identity.ThreadRef + "\x00" + identity.Incarnation + "\x00" + token
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

func transcriptReadFollowUps(params appwire.ThreadTurnsListParams, page appwire.ThreadTurnsListResponse) []appwire.SessionStatusFollowUp {
	followUps := make([]appwire.SessionStatusFollowUp, 0)
	seen := make(map[string]struct{})
	appendCursor := func(cursor string) {
		if cursor == "" {
			return
		}
		if _, ok := seen[cursor]; ok {
			return
		}
		seen[cursor] = struct{}{}
		followUps = append(followUps, appwire.SessionStatusFollowUp{
			Tool: "read_transcript",
			Arguments: appwire.SessionStatusArguments{Hub: appwire.SessionStatusTarget{
				ThreadWindowParams: params.ThreadWindowParams,
				Ref:                params.Ref, Cursor: cursor, ItemLimit: params.ItemLimit,
			}},
		})
	}
	if page.Window != nil {
		appendCursor(page.Window.ContinuationCursor)
	}
	for _, turn := range page.Data {
		if turn.Elision != nil {
			appendCursor(turn.Elision.ContinuationCursor)
		}
	}
	appendCursor(page.NextCursor)
	return followUps
}

func (o *sessionOperations) Invoke(ctx context.Context, caller appwire.ManagerView, request appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	var response appwire.SessionOperationResponse
	var err error
	request, err = appwire.NormalizeSessionOperationRequest(request)
	if err != nil {
		return response, err
	}
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
		response.FollowUps = transcriptReadFollowUps(*request.Turns, page)
	} else if request.Fetch != nil {
		fetch, err := o.fetch(ctx, *request.Fetch)
		if err != nil {
			return response, err
		}
		response.Fetch = &fetch
		if fetch.FetchCursor != "" {
			response.FollowUps = []appwire.SessionStatusFollowUp{{Tool: "read_transcript", Arguments: appwire.SessionStatusArguments{Hub: appwire.SessionStatusTarget{Ref: request.Fetch.Ref, FetchCursor: fetch.FetchCursor}}}}
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
	if request.Discover != nil || request.Read != nil || request.Turns != nil || request.Fetch != nil || request.Activity != nil || request.Tasks != nil || request.Job != nil || request.JobOutput != nil || request.Models != nil {
		if err := managerReadWithinCap(response); err != nil {
			return appwire.SessionOperationResponse{}, err
		}
	}
	return response, nil
}

func (o *sessionOperations) fetch(ctx context.Context, request appwire.SessionFetchRequest) (appwire.SessionFetchResponse, error) {
	cursor, identity, err := appitempaging.DecodeFetchCursorForRef(request.FetchCursor, request.Ref)
	if err != nil {
		return appwire.SessionFetchResponse{}, err
	}
	requestedCursor := cursor
	cursor.Offset = 0
	if cursor.Origin == appitempaging.FetchCursorOriginRemoteSource {
		// Forward the owning hub's token when the controller still holds the
		// entry it was recorded with. Without one the remote hop still runs and
		// answers its own typed error; the local borrow below then reports the
		// controller's loss as stale rather than preempting that answer.
		if remoteToken, ok := o.canonicalItemStore().remoteToken(cursor.Token); ok {
			cursor.Token = remoteToken
		}
	}
	validationCursor, err := appitempaging.EncodeFetchCursor(identity, cursor)
	if err != nil {
		return appwire.SessionFetchResponse{}, err
	}
	switch cursor.Origin {
	case appitempaging.FetchCursorOriginServingDaemon:
		read, err := o.read(ctx, appwire.ThreadReadParams{Ref: request.Ref, FetchCursor: validationCursor}, false)
		if err != nil {
			return appwire.SessionFetchResponse{}, err
		}
		if read.Fetch == nil {
			return appwire.SessionFetchResponse{}, appwire.TranscriptItemCursorStale()
		}
	case appitempaging.FetchCursorOriginRemoteSource:
		source, err := sourceForThreadWithDeletionFence(ctx, o.cfg, o.sources, request.Ref, "")
		if err != nil {
			return appwire.SessionFetchResponse{}, err
		}
		remote, ok := source.(*appsource.RemoteHubSource)
		if !ok {
			return appwire.SessionFetchResponse{}, appwire.TranscriptItemCursorStale()
		}
		if _, err := remote.FetchTranscriptItem(ctx, request.Ref, identity, cursor); err != nil {
			return appwire.SessionFetchResponse{}, err
		}
	case appitempaging.FetchCursorOriginSavedIndex:
		if _, err := fetchSavedTranscriptItem(ctx, o.cfg, request.Ref, identity, cursor); err != nil {
			return appwire.SessionFetchResponse{}, err
		}
	case appitempaging.FetchCursorOriginHubCompat:
		if _, err := o.fetchHubCompatibilityTranscriptItem(ctx, request.Ref, identity, cursor); err != nil {
			return appwire.SessionFetchResponse{}, err
		}
	default:
		return appwire.SessionFetchResponse{}, appwire.TranscriptItemCursorStale()
	}
	canonical, ok := o.canonicalItemStore().borrow(requestedCursor.Token)
	if !ok {
		return appwire.SessionFetchResponse{}, appwire.TranscriptItemCursorStale()
	}
	return appitempaging.BuildFetchResponseBytes(canonical, identity, requestedCursor)
}

func (o *sessionOperations) fetchHubCompatibilityTranscriptItem(
	ctx context.Context,
	ref string,
	identity appitempaging.CursorIdentity,
	cursor appitempaging.FetchCursor,
) (appwire.SessionFetchResponse, error) {
	source, err := sourceForThreadWithDeletionFence(ctx, o.cfg, o.sources, ref, "")
	if err != nil {
		return appwire.SessionFetchResponse{}, err
	}
	legacy, ok := source.(appsource.LegacyPageCandidateSource)
	if !ok {
		return appwire.SessionFetchResponse{}, appwire.ItemUnreachableOnCompat()
	}
	params := appwire.ThreadReadParams{
		Ref: ref, IncludeTurns: true, ItemsView: string(appwire.TurnItemsViewFragment), ItemLimit: appwire.TranscriptItemPageLimit,
	}
	read, err := source.ReadThread(ctx, params)
	if err != nil {
		return appwire.SessionFetchResponse{}, err
	}
	result, err := legacy.ItemCandidatesFromLegacyPage(ctx, params, read)
	if err != nil {
		return appwire.SessionFetchResponse{}, err
	}
	sourceIncarnation, ok := appwire.ThreadWindowSourceIncarnation(identity.Incarnation)
	if !ok || result.Identity.ThreadRef != identity.ThreadRef || result.Identity.Incarnation != sourceIncarnation {
		return appwire.SessionFetchResponse{}, appwire.TranscriptItemCursorStale()
	}
	for _, candidate := range result.Candidates.Candidates {
		if candidate.Position == cursor.Position {
			return appitempaging.BuildFetchResponse(candidate.Item, identity, cursor)
		}
	}
	if result.Exhausted || !sourceAdvertisesPageBefore(source, ref) {
		return appwire.SessionFetchResponse{}, appwire.ItemUnreachableOnCompat()
	}
	candidates := result.Candidates.Candidates
	for range hubCompatFetchPageLimit {
		if len(candidates) == 0 || appitempaging.ComparePosition(candidates[0].Position, cursor.Position) <= 0 {
			return appwire.SessionFetchResponse{}, appwire.ItemUnreachableOnCompat()
		}
		before := candidates[0].Position
		page, err := source.ListTurns(ctx, appwire.ThreadTurnsListParams{
			Ref: ref, Before: &before, ItemsView: string(appwire.TurnItemsViewFragment), ItemLimit: appwire.TranscriptItemPageLimit,
		})
		if err != nil {
			return appwire.SessionFetchResponse{}, err
		}
		if err := appwire.ValidateThreadTurnsListItemResponse(page); err != nil {
			return appwire.SessionFetchResponse{}, err
		}
		// The initial read's incarnation was checked above. A Clear or rewrite
		// between pages can reuse a position and version in a new incarnation,
		// so every recovery page must still come from the history the cursor
		// was minted against before its candidates are trusted.
		if !compatRecoveryHistoryMatches(result.History, appsource.PageHistoryIdentity(page)) {
			return appwire.SessionFetchResponse{}, appwire.TranscriptItemCursorStale()
		}
		candidates, err = appitempaging.CandidatesFromTurns(page.Data)
		if err != nil {
			return appwire.SessionFetchResponse{}, err
		}
		if len(candidates) > 0 && appitempaging.ComparePosition(candidates[len(candidates)-1].Position, before) >= 0 {
			return appwire.SessionFetchResponse{}, appwire.TranscriptItemCursorStale()
		}
		for _, candidate := range candidates {
			if candidate.Position == cursor.Position {
				return appitempaging.BuildFetchResponse(candidate.Item, identity, cursor)
			}
		}
	}
	return appwire.SessionFetchResponse{}, appwire.ItemUnreachableOnCompat()
}

// compatRecoveryHistoryMatches reports whether a compatibility recovery page
// still comes from the history the fetch started against. A legacy peer may omit
// the fields it does not stamp, and a missing side is not evidence of movement;
// a present incarnation or epoch that differs is.
func compatRecoveryHistoryMatches(want appsource.HistoryIdentity, got appsource.HistoryIdentity) bool {
	if want.Snapshot != nil && got.Snapshot != nil && want.Snapshot.Incarnation != got.Snapshot.Incarnation {
		return false
	}
	if want.Epoch != 0 && got.Epoch != 0 && want.Epoch != got.Epoch {
		return false
	}
	return true
}

// hubCompatFetchPageLimit bounds compatibility fetch recovery against an old
// peer after newer arrivals move the addressed item out of its legacy page.
const hubCompatFetchPageLimit = 8

func sourceAdvertisesPageBefore(source appsource.Source, ref string) bool {
	cached, ok := source.(appsource.CachedThreadCapabilitySource)
	if !ok {
		return false
	}
	capabilities, known := cached.CachedThreadCapabilities(ref, "")
	return known && capabilities.PageBefore
}

func fetchSavedTranscriptItem(
	ctx context.Context,
	cfg hubcore.WebConfig,
	ref string,
	identity appitempaging.CursorIdentity,
	cursor appitempaging.FetchCursor,
) (appwire.SessionFetchResponse, error) {
	entry, ok := pastEntryForRead(cfg, appwire.ThreadReadParams{Ref: ref})
	if !ok {
		return appwire.SessionFetchResponse{}, appwire.TranscriptItemCursorStale()
	}
	var response appwire.SessionFetchResponse
	err := withPastTranscriptIndex(ctx, entry, func(index *transcriptindex.Index) error {
		incarnation, err := index.Incarnation()
		if err != nil {
			return err
		}
		sourceIncarnation, ok := appwire.ThreadWindowSourceIncarnation(identity.Incarnation)
		if !ok || sourceIncarnation != incarnation {
			return appwire.TranscriptItemCursorStale()
		}
		candidate, err := index.CandidateAt(cursor.Position)
		if err != nil {
			return err
		}
		response, err = appitempaging.BuildFetchResponse(candidate.Item, identity, cursor)
		return err
	})
	return response, err
}

func statusFollowUp(ref string) appwire.SessionStatusFollowUp {
	return appwire.SessionStatusFollowUp{Tool: "job_status", Arguments: appwire.SessionStatusArguments{Hub: appwire.SessionStatusTarget{Ref: ref}}}
}

// liveReadAttempts bounds the live reads a read may make before the hub
// considers the saved transcript. A live source that fails one read is not
// gone: answering with the persisted transcript would hand the caller a
// daemonless projection — no mutation authority, no published provenance —
// that reads exactly like a replaced manager.
const liveReadAttempts = 3

// liveReadRetryDelay paces the second retry. The first retry is immediate: a
// route-publication race clears at once, while a retried read that must
// reconnect gets the relay's own retry floor.
const liveReadRetryDelay = relayRetryMinDelay

// liveReadRetryBudget bounds everything the retry adds to one read. The
// caller's ownership recovery still performs its own live read before the
// saved transcript, and the web client's thread/read deadline is far shorter
// than several slow live reads, so a slow failure must fall through instead of
// tripling it.
const liveReadRetryBudget = 2 * time.Second

// liveThreadRead reads the live source, retrying a failed read before the
// caller decides whether the saved transcript may answer instead. The retry is
// bounded by liveReadAttempts, honors the request's cancellation, and applies
// only where a failure would otherwise be answered with the saved transcript
// silently (see savedFallbackAnswers). Each attempt acquires the relay again,
// so a dropped daemon connection reconnects; the caller's ownership recovery
// still runs once the attempts or the budget are spent.
func liveThreadRead(
	ctx context.Context,
	relays *hubRelay,
	source appsource.Source,
	params appwire.ThreadReadParams,
	capture bool,
	liveOnly bool,
) (*hubThreadReadResult, error) {
	read := func(ctx context.Context) (*hubThreadReadResult, error) {
		return relays.readThreadWithSubscription(ctx, source, params, capture)
	}
	if !savedFallbackAnswers(source, params) {
		return read(ctx)
	}
	started := time.Now()
	var err error
	for attempt := range liveReadAttempts {
		readCtx, cancel := ctx, func() {}
		if attempt > 1 {
			remaining := liveReadRetryBudget - time.Since(started)
			if remaining <= 0 {
				break
			}
			if waitErr := relays.retryClock.Wait(ctx, min(liveReadRetryDelay, remaining)); waitErr != nil {
				return nil, waitErr
			}
		}
		if attempt > 0 {
			remaining := liveReadRetryBudget - time.Since(started)
			if remaining <= 0 {
				break
			}
			readCtx, cancel = context.WithTimeout(ctx, remaining)
		}
		read, readErr := read(readCtx)
		cancel()
		if readErr == nil {
			return read, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if liveOnly && err != nil && errors.Is(readErr, context.DeadlineExceeded) {
			// The retry attempt died on the budget while the previous attempt's
			// failure was retryable. Keep that failure: the caller's context is
			// still live, and replacing it with DeadlineExceeded would turn a
			// transient outage into a terminal one. A terminal verdict that
			// happened to land as the deadline fired is not a budget death and
			// is returned below.
			return nil, err
		}
		err = readErr
		if !retryableLiveReadFailure(err) {
			return nil, readErr
		}
	}
	return nil, err
}

// liveReadDaemonless reports a successful read answered from persisted history
// rather than a live daemon. Every saved response carries the daemonless boot
// generation; the authority fallback covers an older peer that omits it. It
// keeps Manager so a live descendant read, which is never authoritative, is not
// mistaken for saved history.
func liveReadDaemonless(read appwire.ThreadReadResponse) bool {
	return read.BootGeneration == appwire.DaemonlessBootGeneration ||
		(!read.Thread.Evener.MutationStateAuthoritative && read.Thread.Evener.Manager)
}

// savedFallbackAnswers reports whether a failed live read here is answered from
// the persisted transcript instead of surfacing its error: every
// non-subscribing read, and a subscribing read from a source without an atomic
// live handoff.
func savedFallbackAnswers(source appsource.Source, params appwire.ThreadReadParams) bool {
	if !params.Subscribe {
		return true
	}
	_, atomic := source.(appsource.RelaySessionSource)
	return !atomic
}

// retryableLiveReadFailure reports whether another live read could succeed: the
// relay or the daemon link was unavailable, or the source named the failure
// retryable. A dead or deleted session, a protocol refusal, a daemon that
// cannot initialize, and any failure the request context already ended are
// terminal.
func retryableLiveReadFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if isDeadSessionError(err) || isTargetDeletedError(err) {
		return false
	}
	var mismatch appwire.ProtocolVersionMismatchError
	var initialize appsource.DaemonInitializeError
	if errors.As(err, &mismatch) || errors.As(err, &initialize) {
		return false
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return true
	}
	if data, ok := appwire.DecodedErrorData(wire.Data); ok && data.RetryDisposition == appwire.RetryDispositionAutomatic {
		return true
	}
	return isSessionUnavailableError(err)
}

func sourceAdvertisesWindowRead(ctx context.Context, source appsource.Source, ref, threadID string) bool {
	if cached, ok := source.(appsource.CachedThreadCapabilitySource); ok {
		capabilities, known := cached.CachedThreadCapabilities(ref, threadID)
		return known && capabilities.WindowRead
	}
	listed, err := source.ListThreads(ctx, appwire.ThreadListParams{})
	if err != nil {
		return false
	}
	for _, thread := range listed.Data {
		if ref != "" && thread.Evener.Ref != ref {
			continue
		}
		if ref == "" && threadID != "" && thread.ID != threadID && thread.SessionID != threadID {
			continue
		}
		return thread.Evener.Capabilities.WindowRead
	}
	return false
}

func compatibilityItemCandidateResultForRead(
	ctx context.Context,
	source appsource.Source,
	params appwire.ThreadReadParams,
	response appwire.ThreadReadResponse,
) (transcriptItemCandidateResult, error) {
	if legacy, ok := source.(appsource.LegacyPageCandidateSource); ok {
		result, err := legacy.ItemCandidatesFromLegacyPage(ctx, params, response)
		if err != nil {
			return transcriptItemCandidateResult{}, err
		}
		converted := transcriptItemCandidateResultFromSource(result)
		if params.TurnWindow == nil {
			return converted, nil
		}
		return appendReadOverlayCandidates(converted, response)
	}
	return sourceItemCandidateResultForRead(ctx, source, params, response)
}

// resolveReadSource resolves the source a thread/read reads from, refreshing the
// roster and resolving once more when a transient failure would otherwise answer
// from the persisted transcript. That saved projection carries no mutation
// authority and no provenance, so a client reads it as a replaced manager. The
// refresh opens a window for a registration this read raced to land; it does not
// register sources itself. The retry runs under the deletion fence again, so a
// deletion that begins during the refresh is still terminal. A resolution that
// stays broken keeps today's error, and the caller's saved-history fallback is
// unchanged.
func resolveReadSource(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadReadParams) (appsource.Source, error) {
	source, err := sourceForThreadWithDeletionFence(ctx, cfg, sources, params.Ref, params.ThreadID)
	if !readSourceRetryable(cfg, sources, params, err) {
		return source, err
	}
	// The refresh paces the retry: it leaves the window a registration that lost
	// the race with this read needs to land. Its own failure is not reported —
	// this branch's answer is the saved history below, and an unrelated refresh
	// error would replace readable history with an outage.
	_ = hubRosterRefresh(ctx, cfg.Roster)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	retried, retryErr := sourceForThreadWithDeletionFence(ctx, cfg, sources, params.Ref, params.ThreadID)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if retryErr == nil || isTargetDeletedError(retryErr) {
		return retried, retryErr
	}
	return nil, err
}

// readSourceRetryable reports whether the read's failed source resolution is
// worth one more attempt after a roster refresh. A resolved source needs none. A
// target-deleted fence and a pending-ask page are terminal: the page names a
// live ask set that only a resolved source answers, and the fence must not be
// widened by another attempt. A live-only read has no saved answer to improve
// and never takes an unbudgeted ownership refresh. A hub with no roster or no
// source registry has nothing to refresh.
func readSourceRetryable(cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadReadParams, err error) bool {
	if err == nil || cfg.Roster == nil || sources == nil || params.LiveOnly {
		return false
	}
	if isTargetDeletedError(err) || params.IsPendingAskPage() {
		return false
	}
	// The live-read classifier answers the same question for a resolution
	// failure — could another attempt succeed — and its terminal verdicts (a
	// canceled request, a refused protocol, a dead session) are no more
	// retryable here. A resolution that fails because nothing is registered is
	// not a wire error, which is exactly what it treats as transient.
	return retryableLiveReadFailure(err)
}

func (o *sessionOperations) Read(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	// An ordinary thread/read retains its default downstream subscription when
	// the source hands off a live relay. Internal Manager snapshot reads call
	// read(..., false) directly and do not capture.
	//
	// A bridge-origin read is snapshot intent by construction: the remote hub's
	// source strips the subscribe flag before forwarding (a remote
	// subscription is created only by its explicit subscribe path), so
	// capturing here would register a daemon-side feed and a
	// bridge-connection subscription no hub owns a consumer for and no side
	// can retire.
	if hostRoutingOrigin(ctx) == hostRoutingOriginBridge && !params.Subscribe {
		return o.read(ctx, params, false)
	}
	return o.read(ctx, params, true)
}

func (o *sessionOperations) read(ctx context.Context, params appwire.ThreadReadParams, capture bool) (appwire.ThreadReadResponse, error) {
	cfg, sources, relays := o.cfg, o.sources, o.relays
	if err := params.ValidateAnswerContinuation(); err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	if err := appwire.ValidateThreadReadParams(params); err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	itemLimit, err := normalizeTranscriptWindowItemLimit(params.TurnWindow, params.ItemLimit)
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
	source, err := resolveReadSource(ctx, cfg, sources, params)
	if err != nil {
		if isTargetDeletedError(err) || params.IsPendingAskPage() {
			return appwire.ThreadReadResponse{}, err
		}
		resp, ok, pastErr := unavailableThreadReadResponseOwned(ctx, cfg, sources, params, func(options *transcriptWindowPackOptions) {
			o.retainCanonicalWindowOptions(options)
		})
		if pastErr != nil {
			return appwire.ThreadReadResponse{}, pastErr
		}
		if ok {
			return resp, nil
		}
		return appwire.ThreadReadResponse{}, err
	}
	if params.IsPendingAskPage() {
		// Pages name a live ask set. Saved history and relay subscriptions cannot
		// answer that request or establish its capability on an older daemon.
		row, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: params.Ref, ThreadID: params.ThreadID})
		if err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		if err := requireAskFenceSupport(row.Thread.Evener.Capabilities.AskFence); err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		forward := params
		forward.LiveOnly = false
		page, err := source.ReadThread(ctx, forward)
		if err != nil {
			return appwire.ThreadReadResponse{}, err
		}
		if page.PendingAskPage == nil {
			return appwire.ThreadReadResponse{}, appwire.Unavailable("the target returned no pending ask page")
		}
		page.Thread = annotateThreadProject(page.Thread)
		page.Thread = applyHubCapabilities(cfg, page.Thread)
		page.RequestGeneration = params.RequestGeneration
		return page, nil
	}
	// LiveOnly is a hub-internal fallback decision. Never forward it to the
	// source: older daemons and remote hubs decode thread/read params strictly.
	sourceParams := params
	sourceParams.LiveOnly = false
	hubCompatibility := params.TurnWindow != nil && !sourceAdvertisesWindowRead(ctx, source, params.Ref, params.ThreadID)
	remoteSavedWindowAttempt := false
	if hubCompatibility {
		if _, ok := source.(*appsource.RemoteHubSource); ok {
			// A stopped remote session has no live daemon to advertise the window
			// capability, but its owning hub can still serve the saved window.
			// Ask it for every selector form and pass its typed failure through
			// unchanged, instead of rebuilding the window here.
			hubCompatibility = false
			remoteSavedWindowAttempt = true
		} else if params.TurnWindow.Latest <= 0 {
			if daemon, ok := source.(*appsource.LocalDaemonSource); ok {
				_, admissionErr := daemon.ResolveSubscriptionAdmission(params)
				if isDeadSessionError(admissionErr) {
					saved, found, savedErr := unavailableThreadReadResponseOwned(ctx, cfg, sources, params, func(options *transcriptWindowPackOptions) {
						o.retainCanonicalWindowOptions(options)
					})
					if savedErr != nil {
						return appwire.ThreadReadResponse{}, savedErr
					}
					if found {
						return saved, nil
					}
					return appwire.ThreadReadResponse{}, admissionErr
				}
			}
			return appwire.ThreadReadResponse{}, appwire.WindowUnsupportedOnDaemon()
		}
		if hubCompatibility {
			sourceParams.ThreadWindowParams = appwire.ThreadWindowParams{}
			sourceParams.ItemLimit = appwire.TranscriptItemPageLimit
		}
	}
	read, err := liveThreadRead(ctx, relays, source, sourceParams, capture, params.LiveOnly)
	if remoteSavedWindowAttempt && params.TurnWindow.Latest > 0 &&
		((err != nil && isWindowUnsupportedOnDaemonError(err)) || (err == nil && read.response.Window == nil)) {
		// An older remote hub cannot serve window fields for a live session.
		// Rebuild the latest window from its legacy pages, exactly as a local
		// older daemon's latest window is rebuilt; non-latest reads keep the
		// typed failure instead.
		if read != nil {
			read.finish(false)
			read = nil
		}
		err = nil
		remoteSavedWindowAttempt = false
		hubCompatibility = true
		sourceParams.ThreadWindowParams = appwire.ThreadWindowParams{}
		sourceParams.ItemLimit = appwire.TranscriptItemPageLimit
		read, err = relays.readThreadWithSubscription(ctx, source, sourceParams, capture)
	}
	if err == nil && params.LiveOnly && liveReadDaemonless(read.response) {
		// A remote hub that loses its daemon answers a non-subscribing read from
		// its saved transcript. LiveOnly is hub-internal and never crosses the
		// bridge, so that hop cannot refuse the answer; classify the daemonless
		// projection here, before the voice bridge's authority check treats it
		// as a replaced manager and closes. A live-only read against a genuinely
		// retired remote session now holds speech until resync or grant expiry
		// instead of closing immediately.
		err = appwire.RetryableSessionUnavailable("remote hub answered a live-only read with saved history")
	}
	if err != nil && params.TurnWindow != nil && !hubCompatibility && cfg.Roster != nil {
		if _, local := source.(*appsource.LocalDaemonSource); local {
			originalErr := err
			if read != nil {
				read.finish(false)
				read = nil
			}
			if refreshErr := hubRosterRefresh(ctx, cfg.Roster); refreshErr != nil {
				if ctx.Err() != nil {
					return appwire.ThreadReadResponse{}, ctx.Err()
				}
				return appwire.ThreadReadResponse{}, originalErr
			}
			if sourceAdvertisesWindowRead(ctx, source, params.Ref, params.ThreadID) {
				return appwire.ThreadReadResponse{}, originalErr
			}
			if params.TurnWindow.Latest <= 0 {
				return appwire.ThreadReadResponse{}, appwire.WindowUnsupportedOnDaemon()
			}
			hubCompatibility = true
			sourceParams.ThreadWindowParams = appwire.ThreadWindowParams{}
			sourceParams.ItemLimit = appwire.TranscriptItemPageLimit
			read, err = relays.readThreadWithSubscription(ctx, source, sourceParams, capture)
		}
	}
	if err != nil {
		// A live-only read has no saved answer to recover, and its caller
		// classifies the retryable outage. Ownership recovery would run an
		// unbudgeted re-read and can replace that outage with a daemonless
		// Unavailable, which aborts the voice bridge's held speech.
		if params.LiveOnly {
			if read != nil {
				read.finish(false)
			}
			return appwire.ThreadReadResponse{}, err
		}
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
						read, err = relays.readThreadWithSubscription(ctx, source, sourceParams, capture)
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
			saved, ok, pastErr := unavailableThreadReadResponseOwned(ctx, cfg, sources, params, func(options *transcriptWindowPackOptions) {
				o.retainCanonicalWindowOptions(options)
			})
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
	var compatibilityCandidates *transcriptItemCandidateResult
	if remoteSavedWindowAttempt && resp.Window == nil {
		read.finish(false)
		return appwire.ThreadReadResponse{}, appwire.WindowUnsupportedOnDaemon()
	}
	if hubCompatibility {
		loader, loadErr := newCompatibilityTranscriptWindowLoader(ctx, source, params, resp)
		if loadErr != nil {
			read.finish(false)
			return appwire.ThreadReadResponse{}, loadErr
		}
		loaded, loadErr := loader.load(ctx, params.TurnWindow)
		if loadErr != nil {
			read.finish(false)
			return appwire.ThreadReadResponse{}, loadErr
		}
		resp.Thread = loaded.Thread
		resp.OlderCursor = ""
		compatibilityCandidates = &loaded.Result
	}
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
	if !params.LiveOnly && params.IncludeTurns && liveItemCandidatesEmpty {
		past, ok, pastErr := pastThreadItemReadResponseOwned(ctx, cfg, params, func(options *transcriptWindowPackOptions) {
			o.retainCanonicalWindowOptions(options)
		})
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
	wantPastTurns := !params.LiveOnly && params.IncludeTurns && pastPage == nil && len(resp.Thread.Turns) == 0
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
			resp.Window = pastPage.Window
			resp.LatestExecutionTurnID = pastPage.LatestExecutionTurnID
			resp = withReadHistoryIdentity(resp, fallbackHistoryIdentity(read.response, *pastPage))
			if params.TurnWindow == nil {
				resp.Thread = enrichSourcedThreadImages(source, resp.Thread)
			}
		} else {
			latestExecutionTurnID := resp.LatestExecutionTurnID
			remote, remoteWindow := source.(*appsource.RemoteHubSource)
			if remoteWindow && params.TurnWindow != nil && resp.Window != nil && !hubCompatibility {
				// A current remote hub already applied this exact window and
				// RemoteHubSource re-owned every cursor in the controller namespace.
				// Packing its bounded page again loses continuation metadata and can
				// preserve an inner fetch cursor the controller cannot resolve.
				resp.Thread = enrichSourcedThreadImages(source, resp.Thread)
				if err := o.retainRemoteWindowItems(ctx, remote, params.Ref, resp.Thread); err != nil {
					read.finish(false)
					return appwire.ThreadReadResponse{}, err
				}
				resp.LatestExecutionTurnID = latestExecutionTurnID
			} else {
				var candidates transcriptItemCandidateResult
				var candidateErr error
				if compatibilityCandidates != nil {
					candidates = *compatibilityCandidates
				} else if hubCompatibility {
					candidates, candidateErr = compatibilityItemCandidateResultForRead(ctx, source, params, resp)
				} else {
					candidates, candidateErr = sourceItemCandidateResultForRead(ctx, source, params, resp)
				}
				if candidateErr != nil {
					read.finish(false)
					return appwire.ThreadReadResponse{}, candidateErr
				}
				pack := func(candidates transcriptItemCandidateResult, enrich func(appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error)) (appwire.ThreadReadResponse, error) {
					if params.TurnWindow != nil {
						var options transcriptWindowPackOptions
						if hubCompatibility {
							options = o.canonicalWindowOptions(
								source, resp.Thread, candidates, params.ThreadWindowParams, itemLimit,
								transcriptWindowAppliedByHubCompat, appitempaging.FetchCursorOriginHubCompat,
							)
						} else {
							fetchOrigin := appitempaging.FetchCursorOriginServingDaemon
							if _, remote := source.(*appsource.RemoteHubSource); remote {
								fetchOrigin = appitempaging.FetchCursorOriginRemoteSource
							}
							options = o.canonicalWindowOptions(
								source, resp.Thread, candidates, params.ThreadWindowParams, itemLimit,
								transcriptWindowAppliedByServingDaemon, fetchOrigin,
							)
						}
						configureTranscriptWindowResponseFit(&options, transcriptTurnsListParamsFromRead(params))
						return packThreadReadWindowItemCandidates(candidates, enrich, options)
					}
					return packThreadReadItemCandidates(candidates, enrich, itemLimit)
				}
				packed, packErr := pack(candidates, func(response appwire.ThreadReadResponse) (appwire.ThreadReadResponse, error) {
					response.Thread = threadWithPackedTurns(resp.Thread, response.Thread.Turns)
					if params.TurnWindow == nil {
						response.Thread = enrichSourcedThreadImages(source, response.Thread)
					}
					return response, nil
				})
				if packErr != nil {
					read.finish(false)
					return appwire.ThreadReadResponse{}, packErr
				}
				resp = withReadHistoryIdentity(packed, read.response)
				resp.LatestExecutionTurnID = latestExecutionTurnID
				resp.Changes = withDroppedWindowItems(source, resp.Changes, candidates, resp.Thread)
			}
		}
	} else {
		// A live daemon's turns carry sha-addressed tool-result descriptors with
		// no route on them (the daemon does not serve the bytes; this hub does),
		// so the route is stamped here before the file-backed pass adds any
		// /doc/image descriptors of its own.
		resp.Thread = enrichSourcedThreadImages(source, resp.Thread)
	}
	// Canonical project identity belongs to the final response: stamping it
	// once here covers the live item page, the saved item-page merge and the
	// lean read, and stops the packer re-resolving it on every shrink.
	resp.Thread = annotateThreadProject(resp.Thread)
	// Local forks copy persisted history in the hub. A live daemon's
	// own unsupported fork flag does not describe this hub-owned action.
	resp.Thread = applyHubCapabilities(cfg, resp.Thread)
	if daemon, ok := source.(*appsource.LocalDaemonSource); ok {
		if target, parseErr := appwire.ParseRef(resp.Thread.Evener.Ref); parseErr == nil {
			if supported, known := daemon.SessionToolsSupported(target); known {
				resp.TargetSessionToolsSupported = &supported
			}
		}
	}
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
				return appwire.ThreadReadResponse{}, appwire.RetryableSessionUnavailable("thread snapshot is unavailable")
			}
			if !read.finish(true) {
				return appwire.ThreadReadResponse{}, appwire.RetryableSessionUnavailable("thread snapshot is unavailable")
			}
		}
		return resp, nil
	}
	if read.handoff != nil {
		if !relays.captureThreadRead(ctx, params, read) {
			return appwire.ThreadReadResponse{}, appwire.RetryableSessionUnavailable("thread subscription is unavailable")
		}
	} else if params.AnswerContinuation == nil && sourceSupportsThreadRelay(source) && (params.Subscribe || relayOnThreadRead(source)) {
		// A source with no relay fan-out is never relayed, even for a
		// Subscribe read: startRelay calls SubscribeThread, so relaying one
		// would fail the read. Subscribing callers still get the snapshot.
		// An answer-continuation read is a verdict-only query: it must never
		// attach a relay, because SubscribeThread reissues the read with a
		// subscription the query's own validation forbids.
		if err := relays.startRelay(ctx, source, sourceParams, resp.Thread); err != nil {
			return appwire.ThreadReadResponse{}, err
		}
	}
	return resp, nil
}
