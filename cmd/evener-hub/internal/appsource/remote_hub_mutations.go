package appsource

import (
	"context"
	"errors"

	"primeradiant.com/evener/appwire"
)

// mutationCall forwards one mutation over the current remote client, translates
// response refs, and maps a lost response to a mutation-outcome-unknown error.
//
// It mirrors LocalDaemonSource.withMutationClient for the shared-client case:
// a RemoteHubSource reuses one long-lived client per host, so there is no
// per-call dial or Initialize to run, and only the error mapping differs.
func (s *RemoteHubSource) mutationCall(ctx context.Context, method string, clientMutationID string, params any, out any) error {
	// A caller cancellation or deadline stays raw at every step, exactly as
	// LocalDaemonSource.withClientCallMapper leaves ctx.Err(): the caller's own
	// context ending is not host unavailability. On the request path this is
	// load-bearing — mapping it through would become MutationOutcomeUnknown
	// with an automatic retry, re-driving a mutation the caller abandoned.
	if err := ctx.Err(); err != nil {
		return err
	}
	client, err := s.resolveClient(ctx)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		// Resolving the client never dials; a failure here means the host has no
		// live channel and is reported as a SessionUnavailable the auto-resume
		// gate can act on, or the caller's own context ended. No request crossed
		// the wire, so the outcome is known. This is the connect mapper, matching
		// call (and it keeps a genuine caller cancellation raw).
		return s.mapConnectError(err)
	}
	if err := s.requestOn(ctx, client, method, params, out); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return s.remoteHubMutationCallError(clientMutationID, err)
	}
	return s.translateOut(client, out)
}

// remoteHubMutationCallError reports a lost mutation response as outcome
// unknown with automatic replay. A delivered WireError is a semantic verdict,
// regardless of whether its Data is typed or decoded JSON. Classify that
// provenance before mapCallError replaces a transport failure with an ordinary
// SessionUnavailable WireError.
func (s *RemoteHubSource) remoteHubMutationCallError(clientMutationID string, err error) error {
	if _, delivered := errors.AsType[appwire.WireError](err); delivered && !appwire.IsTransportFailure(err) {
		return err
	}
	mapped := s.mapCallError(err)
	wire, ok := errors.AsType[appwire.WireError](mapped)
	if !ok || wire.Code != appwire.CodeUnavailable {
		return mapped
	}
	return mutationOutcomeUnknown("mutation outcome is unknown after remote hub response loss", clientMutationID, appwire.RetryDispositionAutomatic)
}

// remoteForwardVoid sends one non-mutation RPC to the remote hub. remote is the
// caller's copy of its params and ref points at that copy's Ref field: the ref
// is rewritten in place to the remote hub's own spelling before the call.
func remoteForwardVoid[P any](ctx context.Context, s *RemoteHubSource, method string, remote *P, ref *string) error {
	translated, err := s.toRemoteRef(*ref, "")
	if err != nil {
		return err
	}
	*ref = translated.String()
	return s.call(ctx, method, *remote, nil)
}

// remoteForward is remoteForwardVoid for an RPC that returns a result.
func remoteForward[R, P any](ctx context.Context, s *RemoteHubSource, method string, remote *P, ref *string) (R, error) {
	var out, zero R
	translated, err := s.toRemoteRef(*ref, "")
	if err != nil {
		return out, err
	}
	*ref = translated.String()
	if err := s.call(ctx, method, *remote, &out); err != nil {
		return zero, err
	}
	return out, nil
}

// remoteForwardMutation is remoteForward for a mutation: a lost response is
// reported against clientMutationID.
func remoteForwardMutation[R, P any](ctx context.Context, s *RemoteHubSource, method, clientMutationID string, remote *P, ref *string) (R, error) {
	var out, zero R
	translated, err := s.toRemoteRef(*ref, "")
	if err != nil {
		return out, err
	}
	*ref = translated.String()
	if err := s.mutationCall(ctx, method, clientMutationID, *remote, &out); err != nil {
		return zero, err
	}
	return out, nil
}

// StartThread forwards thread/start, which the remote hub serves hub-scoped and
// spawns on its own host. ThreadStartParams carries no ref, so its Harness field
// is the controller's source selector and must not be forwarded verbatim: the
// remote hub would look for a source named after this host and fail with
// "spawn source is not available". It is rewritten to the remote hub's own
// harness ("evener" resolves to the remote's "local" source, hubThreadStart),
// mirroring hubModelListInner, which clears the selector before ListModels.
func (s *RemoteHubSource) StartThread(ctx context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	remote := params
	remote.Harness = "evener"
	// Source and a non-"evener" Harness are both controller-side routing hints:
	// Source names this source in the controller's registry, and the remote
	// hub's launchSourceID reads any non-"evener" Harness as a source id too.
	// The remote hub would resolve either against its own registry, so neither
	// may be forwarded: Source is cleared, and Harness is neutralized to the
	// remote hub's own default. Leaving Harness set would re-route a start the
	// controller already targeted at this host (Source=remote-a, Harness=remote-b
	// would spawn on remote-b on the remote hub); the remote hub's own local
	// spawn is the intended target.
	remote.Source = ""
	var out appwire.ThreadStartResponse
	if err := s.call(ctx, appwire.MethodThreadStart, remote, &out); err != nil {
		return appwire.ThreadStartResponse{}, s.fromRemoteThreadStartError(err)
	}
	return out, nil
}

func (s *RemoteHubSource) ResumeThread(ctx context.Context, params appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	remote := params
	return remoteForward[appwire.ThreadResumeResponse](ctx, s, appwire.MethodThreadResume, &remote, &remote.Ref)
}

func (s *RemoteHubSource) ForkThread(ctx context.Context, params appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	remote := params
	return remoteForward[appwire.ThreadForkResponse](ctx, s, appwire.MethodThreadFork, &remote, &remote.Ref)
}

func (s *RemoteHubSource) StartTurn(ctx context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, params.ThreadID)
	if err != nil {
		return appwire.TurnStartResponse{}, err
	}
	remote := params
	remote.Ref = ref.String()
	remote.ThreadID = ref.ThreadID
	var out appwire.TurnStartResponse
	if err := s.mutationCall(ctx, appwire.MethodTurnStart, params.ClientMutationID, remote, &out); err != nil {
		return appwire.TurnStartResponse{}, err
	}
	return out, nil
}

func (s *RemoteHubSource) SteerTurn(ctx context.Context, params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, params.ThreadID)
	if err != nil {
		return appwire.TurnSteerResponse{}, err
	}
	remote := params
	remote.Ref = ref.String()
	remote.ThreadID = ref.ThreadID
	var out appwire.TurnSteerResponse
	if err := s.mutationCall(ctx, appwire.MethodTurnSteer, params.ClientMutationID, remote, &out); err != nil {
		return appwire.TurnSteerResponse{}, err
	}
	return out, nil
}

func (s *RemoteHubSource) ResolveSandboxEscalation(ctx context.Context, params appwire.SandboxEscalationResolveParams) error {
	ref, err := s.toRemoteRef(params.Ref, params.ThreadID)
	if err != nil {
		return err
	}
	remote := params
	remote.Ref = ref.String()
	remote.ThreadID = ref.ThreadID
	return s.call(ctx, appwire.MethodEvenerSandboxEscalationResolve, remote, nil)
}

// StopDelegate forwards evener/delegate/stop to the host hub that owns the
// root, which relays it to the root's daemon.
func (s *RemoteHubSource) StopDelegate(ctx context.Context, params appwire.DelegateStopParams) (appwire.DelegateStopResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, params.ThreadID)
	if err != nil {
		return appwire.DelegateStopResponse{}, err
	}
	remote := params
	remote.Ref = ref.String()
	remote.ThreadID = ref.ThreadID
	var out appwire.DelegateStopResponse
	err = s.call(ctx, appwire.MethodEvenerDelegateStop, remote, &out)
	return out, err
}

func (s *RemoteHubSource) StopJob(ctx context.Context, request appwire.SessionStopRequest) (appwire.SessionStopResponse, error) {
	ref := ""
	if request.Job != nil {
		ref = request.Job.Ref
	} else if request.OwnedDelegate != nil {
		ref = request.OwnedDelegate.Ref
	}
	remoteRef, err := s.toRemoteRef(ref, "")
	if err != nil {
		return appwire.SessionStopResponse{}, err
	}
	if request.Job != nil {
		copy := *request.Job
		copy.Ref = remoteRef.String()
		request.Job = &copy
	} else if request.OwnedDelegate != nil {
		copy := *request.OwnedDelegate
		copy.Ref = remoteRef.String()
		request.OwnedDelegate = &copy
	}
	var out appwire.SessionStopResponse
	err = s.call(ctx, appwire.MethodEvenerJobsStop, request, &out)
	return out, err
}

func (s *RemoteHubSource) InterruptTurn(ctx context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, params.ThreadID)
	if err != nil {
		return appwire.TurnInterruptResponse{}, err
	}
	remote := params
	remote.Ref = ref.String()
	remote.ThreadID = ref.ThreadID
	var out appwire.TurnInterruptResponse
	if err := s.mutationCall(ctx, appwire.MethodTurnInterrupt, params.ClientMutationID, remote, &out); err != nil {
		return appwire.TurnInterruptResponse{}, err
	}
	return out, nil
}

func (s *RemoteHubSource) QueueTurn(ctx context.Context, params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
	remote := params
	return remoteForwardMutation[appwire.TurnQueueResponse](ctx, s, appwire.MethodTurnQueue, params.ClientMutationID, &remote, &remote.Ref)
}

func (s *RemoteHubSource) DrainAsSteer(ctx context.Context, params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
	remote := params
	return remoteForwardMutation[appwire.TurnDrainAsSteerResponse](ctx, s, appwire.MethodTurnDrainAsSteer, params.ClientMutationID, &remote, &remote.Ref)
}

func (s *RemoteHubSource) PromoteQueuedAsSteer(ctx context.Context, params appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
	remote := params
	return remoteForwardMutation[appwire.TurnPromoteQueuedAsSteerResponse](ctx, s, appwire.MethodTurnPromoteQueuedAsSteer, params.ClientMutationID, &remote, &remote.Ref)
}

func (s *RemoteHubSource) CancelQueued(ctx context.Context, params appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
	remote := params
	return remoteForwardMutation[appwire.TurnCancelQueuedResponse](ctx, s, appwire.MethodTurnCancelQueued, params.ClientMutationID, &remote, &remote.Ref)
}

func (s *RemoteHubSource) CompactThread(ctx context.Context, params appwire.ThreadCompactStartParams) error {
	remote := params
	return remoteForwardVoid(ctx, s, appwire.MethodThreadCompactStart, &remote, &remote.Ref)
}

func (s *RemoteHubSource) ShutdownThread(ctx context.Context, params appwire.ThreadShutdownParams) error {
	remote := params
	return remoteForwardVoid(ctx, s, appwire.MethodThreadShutdown, &remote, &remote.Ref)
}

// ForceStopThread forwards evener/thread/forceStop with the ref translated into
// this remote hub's own "local:" spelling, so the force-stop reaches the hub
// that owns the daemon process (component 07, §"Dedicated ref-translating
// dispatch (evener/thread/forceStop)"). It is deliberately outside the
// evener/host/request allow-list.
func (s *RemoteHubSource) ForceStopThread(ctx context.Context, params appwire.ThreadForceStopParams) error {
	remote := params
	return remoteForwardVoid(ctx, s, appwire.MethodEvenerThreadForceStop, &remote, &remote.Ref)
}

func (s *RemoteHubSource) SetThreadModel(ctx context.Context, params appwire.ThreadModelSetParams) error {
	remote := params
	return remoteForwardVoid(ctx, s, appwire.MethodThreadModelSet, &remote, &remote.Ref)
}

func (s *RemoteHubSource) SetThreadReasoningEffort(ctx context.Context, params appwire.ThreadReasoningEffortSetParams) error {
	remote := params
	return remoteForwardVoid(ctx, s, appwire.MethodThreadReasoningEffortSet, &remote, &remote.Ref)
}

func (s *RemoteHubSource) SetThreadVisionModel(ctx context.Context, params appwire.ThreadVisionModelSetParams) error {
	remote := params
	return remoteForwardVoid(ctx, s, appwire.MethodThreadVisionModelSet, &remote, &remote.Ref)
}

func (s *RemoteHubSource) SetThreadName(ctx context.Context, params appwire.ThreadNameSetParams) error {
	remote := params
	return remoteForwardVoid(ctx, s, appwire.MethodEvenerThreadNameSet, &remote, &remote.Ref)
}

func (s *RemoteHubSource) GoalSet(ctx context.Context, params appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
	remote := params
	return remoteForward[appwire.GoalSetResponse](ctx, s, appwire.MethodGoalSet, &remote, &remote.Ref)
}

func (s *RemoteHubSource) NotesHumanSet(ctx context.Context, params appwire.NotesHumanSetParams) (appwire.NotesHumanSetResponse, error) {
	remote := params
	return remoteForwardMutation[appwire.NotesHumanSetResponse](ctx, s, appwire.MethodNotesHumanSet, params.ClientMutationID, &remote, &remote.Ref)
}

func (s *RemoteHubSource) UrlsRemove(ctx context.Context, params appwire.UrlsRemoveParams) (appwire.UrlsRemoveResponse, error) {
	remote := params
	return remoteForwardMutation[appwire.UrlsRemoveResponse](ctx, s, appwire.MethodUrlsRemove, params.ClientMutationID, &remote, &remote.Ref)
}

func (s *RemoteHubSource) ClearThread(ctx context.Context, params appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	remote := params
	return remoteForwardMutation[appwire.ThreadClearResponse](ctx, s, appwire.MethodThreadClear, params.ClientMutationID, &remote, &remote.Ref)
}

func (s *RemoteHubSource) ListTasks(ctx context.Context, params appwire.TaskListParams) (appwire.TaskListResponse, error) {
	remote := params
	return remoteForward[appwire.TaskListResponse](ctx, s, appwire.MethodEvenerTasksList, &remote, &remote.Ref)
}

func (s *RemoteHubSource) ListJobs(ctx context.Context, params appwire.JobsListParams) (appwire.JobsListResponse, error) {
	remote := params
	return remoteForward[appwire.JobsListResponse](ctx, s, appwire.MethodEvenerJobsList, &remote, &remote.Ref)
}

func (s *RemoteHubSource) JobOutput(ctx context.Context, params appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
	remote := params
	return remoteForward[appwire.JobsOutputResponse](ctx, s, appwire.MethodEvenerJobsOutput, &remote, &remote.Ref)
}

func (s *RemoteHubSource) JobGet(ctx context.Context, params appwire.JobsGetParams) (appwire.JobsGetResponse, error) {
	remote := params
	return remoteForward[appwire.JobsGetResponse](ctx, s, appwire.MethodEvenerJobsGet, &remote, &remote.Ref)
}
