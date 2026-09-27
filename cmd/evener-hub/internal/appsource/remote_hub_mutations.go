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
	if err := client.Request(ctx, method, params, out); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return s.remoteHubMutationCallError(clientMutationID, err)
	}
	return s.translateOut(out)
}

// remoteHubMutationCallError mirrors localDaemonMutationCallError in shape: a
// mutation whose response was lost (the transport failure was mapped by
// mapCallError to SessionUnavailable) cannot be known to have been applied, so
// it becomes ErrorMutationOutcomeUnknown with the client's id preserved and
// RetryDispositionAutomatic. Semantic wire errors pass through untouched.
//
// The Data type assertion is deliberately appwire.ErrorData only, not the
// decoded map[string]any a JSON error frame produces. The two shapes mean
// different things here:
//
//   - The typed shape is one WE synthesized in mapCallError/transportUnavailable
//     after a transport failure, i.e. the request crossed the wire and its
//     response was lost. Only that in-doubt case becomes MutationOutcomeUnknown.
//   - The decoded-map shape is an error frame that arrived intact from the
//     remote hub, so the response was NOT lost; it is the hub's semantic
//     verdict on the mutation (its thread is gone, its agent is unavailable
//     before the request was dispatched). The hub already labels a genuinely
//     in-doubt mutation of its own as mutationOutcomeUnknown, so a
//     sessionUnavailable it sends means "not applied". Converting that to
//     MutationOutcomeUnknown+Automatic would re-drive a mutation against a
//     session that is known absent (e.g. an autoretry loop on "thread not
//     found"), and locally the same verdict is NotAccepted
//     (localDaemonMutationEntryError) that the auto-resume gate acts on via
//     isSessionUnavailableError.
func (s *RemoteHubSource) remoteHubMutationCallError(clientMutationID string, err error) error {
	mapped := s.mapCallError(err)
	var wire appwire.WireError
	if !errors.As(mapped, &wire) {
		return mapped
	}
	// Only the locally synthesized typed ErrorData counts; a decoded
	// map[string]any is a delivered semantic verdict, not a lost response.
	data, ok := wire.Data.(appwire.ErrorData)
	if wire.Code != appwire.CodeUnavailable || !ok || data.EvenerErrorInfo != appwire.ErrorSessionUnavailable {
		return mapped
	}
	return appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: "mutation outcome is unknown after remote hub response loss",
		Data: appwire.ErrorData{
			EvenerErrorInfo:  appwire.ErrorMutationOutcomeUnknown,
			ClientMutationID: clientMutationID,
			MutationOutcome:  appwire.MutationOutcomeUnknown,
			RetryDisposition: appwire.RetryDispositionAutomatic,
		},
	}
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
		return appwire.ThreadStartResponse{}, err
	}
	return out, nil
}

func (s *RemoteHubSource) ResumeThread(ctx context.Context, params appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return appwire.ThreadResumeResponse{}, err
	}
	remote := params
	remote.Ref = ref.String()
	var out appwire.ThreadResumeResponse
	if err := s.call(ctx, appwire.MethodThreadResume, remote, &out); err != nil {
		return appwire.ThreadResumeResponse{}, err
	}
	return out, nil
}

func (s *RemoteHubSource) ForkThread(ctx context.Context, params appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return appwire.ThreadForkResponse{}, err
	}
	remote := params
	remote.Ref = ref.String()
	var out appwire.ThreadForkResponse
	if err := s.call(ctx, appwire.MethodThreadFork, remote, &out); err != nil {
		return appwire.ThreadForkResponse{}, err
	}
	return out, nil
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
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return appwire.TurnQueueResponse{}, err
	}
	remote := params
	remote.Ref = ref.String()
	var out appwire.TurnQueueResponse
	if err := s.mutationCall(ctx, appwire.MethodTurnQueue, params.ClientMutationID, remote, &out); err != nil {
		return appwire.TurnQueueResponse{}, err
	}
	return out, nil
}

func (s *RemoteHubSource) DrainAsSteer(ctx context.Context, params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return appwire.TurnDrainAsSteerResponse{}, err
	}
	remote := params
	remote.Ref = ref.String()
	var out appwire.TurnDrainAsSteerResponse
	if err := s.mutationCall(ctx, appwire.MethodTurnDrainAsSteer, params.ClientMutationID, remote, &out); err != nil {
		return appwire.TurnDrainAsSteerResponse{}, err
	}
	return out, nil
}

func (s *RemoteHubSource) PromoteQueuedAsSteer(ctx context.Context, params appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, err
	}
	remote := params
	remote.Ref = ref.String()
	var out appwire.TurnPromoteQueuedAsSteerResponse
	if err := s.mutationCall(ctx, appwire.MethodTurnPromoteQueuedAsSteer, params.ClientMutationID, remote, &out); err != nil {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, err
	}
	return out, nil
}

func (s *RemoteHubSource) CancelQueued(ctx context.Context, params appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return appwire.TurnCancelQueuedResponse{}, err
	}
	remote := params
	remote.Ref = ref.String()
	var out appwire.TurnCancelQueuedResponse
	if err := s.mutationCall(ctx, appwire.MethodTurnCancelQueued, params.ClientMutationID, remote, &out); err != nil {
		return appwire.TurnCancelQueuedResponse{}, err
	}
	return out, nil
}

func (s *RemoteHubSource) CompactThread(ctx context.Context, params appwire.ThreadCompactStartParams) error {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return err
	}
	remote := params
	remote.Ref = ref.String()
	return s.call(ctx, appwire.MethodThreadCompactStart, remote, nil)
}

func (s *RemoteHubSource) ShutdownThread(ctx context.Context, params appwire.ThreadShutdownParams) error {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return err
	}
	remote := params
	remote.Ref = ref.String()
	return s.call(ctx, appwire.MethodThreadShutdown, remote, nil)
}

// ForceStopThread forwards evener/thread/forceStop with the ref translated into
// this remote hub's own "local:" spelling, so the force-stop reaches the hub
// that owns the daemon process (component 07, §"Dedicated ref-translating
// dispatch (evener/thread/forceStop)"). It is deliberately outside the
// evener/host/request allow-list.
func (s *RemoteHubSource) ForceStopThread(ctx context.Context, params appwire.ThreadForceStopParams) error {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return err
	}
	remote := params
	remote.Ref = ref.String()
	return s.call(ctx, appwire.MethodEvenerThreadForceStop, remote, nil)
}

func (s *RemoteHubSource) SetThreadModel(ctx context.Context, params appwire.ThreadModelSetParams) error {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return err
	}
	remote := params
	remote.Ref = ref.String()
	return s.call(ctx, appwire.MethodThreadModelSet, remote, nil)
}

func (s *RemoteHubSource) SetThreadReasoningEffort(ctx context.Context, params appwire.ThreadReasoningEffortSetParams) error {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return err
	}
	remote := params
	remote.Ref = ref.String()
	return s.call(ctx, appwire.MethodThreadReasoningEffortSet, remote, nil)
}

func (s *RemoteHubSource) SetThreadVisionModel(ctx context.Context, params appwire.ThreadVisionModelSetParams) error {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return err
	}
	remote := params
	remote.Ref = ref.String()
	return s.call(ctx, appwire.MethodThreadVisionModelSet, remote, nil)
}

func (s *RemoteHubSource) SetThreadName(ctx context.Context, params appwire.ThreadNameSetParams) error {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return err
	}
	remote := params
	remote.Ref = ref.String()
	return s.call(ctx, appwire.MethodEvenerThreadNameSet, remote, nil)
}

func (s *RemoteHubSource) GoalSet(ctx context.Context, params appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return appwire.GoalSetResponse{}, err
	}
	remote := params
	remote.Ref = ref.String()
	var out appwire.GoalSetResponse
	if err := s.call(ctx, appwire.MethodGoalSet, remote, &out); err != nil {
		return appwire.GoalSetResponse{}, err
	}
	return out, nil
}

func (s *RemoteHubSource) NotesHumanSet(ctx context.Context, params appwire.NotesHumanSetParams) (appwire.NotesHumanSetResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return appwire.NotesHumanSetResponse{}, err
	}
	remote := params
	remote.Ref = ref.String()
	var out appwire.NotesHumanSetResponse
	if err := s.mutationCall(ctx, appwire.MethodNotesHumanSet, params.ClientMutationID, remote, &out); err != nil {
		return appwire.NotesHumanSetResponse{}, err
	}
	return out, nil
}

func (s *RemoteHubSource) UrlsRemove(ctx context.Context, params appwire.UrlsRemoveParams) (appwire.UrlsRemoveResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return appwire.UrlsRemoveResponse{}, err
	}
	remote := params
	remote.Ref = ref.String()
	var out appwire.UrlsRemoveResponse
	if err := s.mutationCall(ctx, appwire.MethodUrlsRemove, params.ClientMutationID, remote, &out); err != nil {
		return appwire.UrlsRemoveResponse{}, err
	}
	return out, nil
}

func (s *RemoteHubSource) ClearThread(ctx context.Context, params appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return appwire.ThreadClearResponse{}, err
	}
	remote := params
	remote.Ref = ref.String()
	var out appwire.ThreadClearResponse
	if err := s.mutationCall(ctx, appwire.MethodThreadClear, params.ClientMutationID, remote, &out); err != nil {
		return appwire.ThreadClearResponse{}, err
	}
	return out, nil
}

func (s *RemoteHubSource) ListTasks(ctx context.Context, params appwire.TaskListParams) (appwire.TaskListResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return appwire.TaskListResponse{}, err
	}
	remote := params
	remote.Ref = ref.String()
	var out appwire.TaskListResponse
	if err := s.call(ctx, appwire.MethodEvenerTasksList, remote, &out); err != nil {
		return appwire.TaskListResponse{}, err
	}
	return out, nil
}

func (s *RemoteHubSource) ListJobs(ctx context.Context, params appwire.JobsListParams) (appwire.JobsListResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return appwire.JobsListResponse{}, err
	}
	remote := params
	remote.Ref = ref.String()
	var out appwire.JobsListResponse
	if err := s.call(ctx, appwire.MethodEvenerJobsList, remote, &out); err != nil {
		return appwire.JobsListResponse{}, err
	}
	return out, nil
}

func (s *RemoteHubSource) JobOutput(ctx context.Context, params appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return appwire.JobsOutputResponse{}, err
	}
	remote := params
	remote.Ref = ref.String()
	var out appwire.JobsOutputResponse
	if err := s.call(ctx, appwire.MethodEvenerJobsOutput, remote, &out); err != nil {
		return appwire.JobsOutputResponse{}, err
	}
	return out, nil
}
