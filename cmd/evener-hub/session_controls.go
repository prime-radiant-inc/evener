package hub

import (
	"context"
	"strings"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func (o *sessionOperations) resolveApproval(ctx context.Context, params appwire.SandboxEscalationResolveParams) (appwire.EmptyResponse, error) {
	cfg, sources := o.cfg, o.sources
	return withSessionActionOwnership(ctx, cfg, params.Ref, params.ThreadID, func() (appwire.EmptyResponse, error) {
		if err := refreshDaemonRestartRequiredError(ctx, cfg, params.Ref, params.ThreadID, ""); err != nil {
			return appwire.EmptyResponse{}, err
		}
		source, err := sourceForThread(sources, params.Ref, params.ThreadID)
		if err != nil {
			return appwire.EmptyResponse{}, err
		}
		return appwire.EmptyResponse{}, source.ResolveSandboxEscalation(ctx, params)
	})
}

// Control dispatches a validated closed union into the existing human owners.
// No user-selected RPC method name is ever sent to a dispatcher.
func (o *sessionOperations) Control(ctx context.Context, request appwire.SessionControlRequest) (appwire.SessionControlResponse, error) {
	var out appwire.SessionControlResponse
	if err := request.Validate(); err != nil {
		return out, err
	}
	switch request.Action {
	case "approval":
		result, err := o.resolveApproval(ctx, *request.Approval)
		out.Result = result
		return out, err
	case "resume":
		result, err := hubThreadResume(ctx, o.cfg, o.sources, *request.Resume)
		out.Resume = &result
		return out, err
	case "fork":
		result, err := hubThreadFork(ctx, o.cfg, o.sources, *request.Fork)
		out.Fork = &result
		return out, err
	case "clear":
		result, err := clearThreadControl(ctx, o.cfg, o.sources, *request.Clear)
		out.Clear = &result
		return out, err
	case "model":
		return out, setThreadModelWithResume(ctx, o.cfg, o.sources, *request.Model)
	case "vision_model":
		return out, setThreadVisionModelWithResume(ctx, o.cfg, o.sources, *request.VisionModel)
	case "reasoning_effort":
		_, err := setReasoningEffortControl(ctx, o.cfg, o.sources, *request.ReasoningEffort)
		return out, err
	case "compact":
		return out, compactThreadWithResume(ctx, o.cfg, o.sources, *request.Compact)
	case "drain_queue":
		result, err := drainQueueControl(ctx, o.cfg, o.sources, *request.DrainQueue)
		out.DrainQueue = &result
		return out, err
	case "promote_queued":
		result, err := promoteQueuedControl(ctx, o.cfg, o.sources, *request.PromoteQueued)
		out.PromoteQueued = &result
		return out, err
	case "cancel_queued":
		result, err := cancelQueuedControl(ctx, o.cfg, o.sources, *request.CancelQueued)
		out.CancelQueued = &result
		return out, err
	case "rename":
		result, err := setThreadName(ctx, o.cfg, o.sources, o.navigation, *request.Rename)
		out.Result = result
		return out, err
	case "goal":
		result, err := setGoalWithResume(ctx, o.cfg, o.sources, *request.Goal)
		out.Goal = &result
		return out, err
	case "human_notes":
		result, err := setNotesHumanWithResume(ctx, o.cfg, o.sources, *request.HumanNotes)
		err = adoptCallerMutationResult(&result.Receipt, err, request.HumanNotes.ClientMutationID)
		out.HumanNotes = &result
		return out, err
	case "remove_url":
		result, err := removeURLWithResume(ctx, o.cfg, o.sources, *request.RemoveURL)
		out.RemoveURL = &result
		return out, adoptFailureClientMutationID(err, request.RemoveURL.ClientMutationID)
	case "archive":
		result, err := archiveSet(ctx, o.cfg, o.sources, o.navigation, *request.Archive)
		out.Archive = &result
		return out, err
	case "delete":
		if o.navigation == nil || o.navigation.deleteSession == nil {
			return out, appwire.Unavailable("session deletion unavailable")
		}
		result, err := o.navigation.deleteSession(ctx, *request.Delete)
		out.Delete = &result
		return out, err
	case "pin":
		result, err := pinAssign(ctx, o.cfg, o.navigation, o.resolve, *request.Pin)
		out.Pin = &result
		return out, err
	case "unpin":
		result, err := pinUnpin(ctx, o.cfg, o.navigation, o.resolve, *request.Unpin)
		out.Unpin = &result
		return out, err
	case "seen":
		result, err := sessionSeenSet(ctx, o.cfg, o.navigation, *request.Seen, time.Now())
		out.Seen = &result
		return out, err
	}
	return out, appwire.InvalidParams("unknown session control action")
}

func (o *sessionOperations) Stop(ctx context.Context, request appwire.SessionStopRequest) (appwire.SessionStopResponse, error) {
	var out appwire.SessionStopResponse
	if err := request.Validate(); err != nil {
		return out, err
	}
	switch request.Mode {
	case "interrupt":
		result, err := interruptTurnControl(ctx, o.cfg, o.sources, *request.Interrupt)
		out.Interrupt = &result
		return out, err
	case "delegate":
		params := *request.Delegate
		// Only this owner needs the root alias: the addressed descendant ref
		// remains untouched in every other Manager operation.
		summary, err := readSessionActivity(ctx, o.cfg, o.sources, params.Ref, func(source appsource.Source) (appwire.SessionActivitySummary, error) {
			return source.ThreadActivityRead(ctx, appwire.SessionActivityReadParams{Ref: params.Ref})
		}, func(entry hubcore.PastEntry) (appwire.SessionActivitySummary, error) {
			return appwire.SessionActivitySummary{}, appwire.Unavailable("delegate stop needs live owner ancestry")
		})
		if err != nil {
			return out, err
		}
		if !summary.Context.AncestryKnown || summary.Context.RootRef == "" {
			return out, appwire.Unavailable("delegate owner ancestry unavailable")
		}
		if summary.Context.DelegateID != "" && summary.Context.DelegateID != params.DelegateID {
			return out, appwire.InvalidParams("delegateId does not match addressed descendant")
		}
		params.Ref = summary.Context.RootRef
		params.ThreadID = ""
		result, err := stopDelegateControl(ctx, o.cfg, o.sources, params)
		out.Delegate = &result
		return out, err
	case "job":
		return stopJobControl(ctx, o.cfg, o.sources, request)
	case "owned_delegate":
		return stopJobControl(ctx, o.cfg, o.sources, request)
	case "shutdown":
		return out, shutdownThreadTolerateExited(ctx, o.cfg, o.sources, *request.Shutdown)
	case "force_stop":
		return out, forceStopThread(ctx, o.cfg, *request.ForceStop, o.sources)
	}
	return out, appwire.InvalidParams("unknown session stop mode")
}

func stopJobControl(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, request appwire.SessionStopRequest) (appwire.SessionStopResponse, error) {
	var out appwire.SessionStopResponse
	if err := request.Validate(); err != nil {
		return out, err
	}
	if request.Job == nil && request.OwnedDelegate == nil {
		return out, appwire.InvalidParams("job or owned_delegate mode required")
	}
	ref := ""
	if request.Job != nil {
		ref = request.Job.Ref
	} else {
		ref = request.OwnedDelegate.Ref
	}
	summary, err := readSessionActivity(ctx, cfg, sources, ref, func(source appsource.Source) (appwire.SessionActivitySummary, error) {
		return source.ThreadActivityRead(ctx, appwire.SessionActivityReadParams{Ref: ref})
	}, func(entry hubcore.PastEntry) (appwire.SessionActivitySummary, error) {
		return appwire.SessionActivitySummary{}, appwire.Unavailable("job stop needs a live owner")
	})
	if err != nil {
		return out, err
	}
	if !summary.Context.AncestryKnown || summary.Context.RootRef == "" {
		return out, appwire.Unavailable("job stop owner ancestry unavailable")
	}
	addressed, err := appwire.ParseRef(ref)
	if err != nil {
		return out, appwire.InvalidParams(err.Error())
	}
	root, err := appwire.ParseRef(summary.Context.RootRef)
	if err != nil {
		return out, appwire.Unavailable("job stop root is invalid")
	}
	// A remote source reports local: refs from its host; retain the selected
	// source namespace when forwarding to the owner of this addressed ref.
	root.SourceID = addressed.SourceID
	ownerRef := root.String()
	if request.OwnedDelegate != nil && summary.Context.DelegateID != "" && summary.Context.DelegateID != request.OwnedDelegate.DelegateID {
		return out, appwire.InvalidParams("delegateId does not match addressed descendant")
	}
	return withSessionActionOwnership(ctx, cfg, ownerRef, "", func() (appwire.SessionStopResponse, error) {
		if err := refreshDaemonRestartRequiredError(ctx, cfg, ownerRef, "", ""); err != nil {
			return out, err
		}
		source, err := sourceForThread(sources, ownerRef, "")
		if err != nil {
			return out, err
		}
		stopper, ok := source.(appsource.JobStopSource)
		if !ok {
			return out, appwire.Unavailable("this session's source cannot stop jobs")
		}
		if request.Job != nil {
			job, err := source.JobGet(ctx, appwire.JobsGetParams{Ref: ownerRef, JobID: request.Job.JobID})
			if err != nil {
				return out, err
			}
			jobOwner, ownerErr := appwire.ParseRef(job.Data.OwnerRef)
			if ownerErr != nil || job.Data.JobID != request.Job.JobID || job.Data.OwnerSessionID != summary.Context.SessionID || jobOwner.ThreadID != summary.Context.SessionID || job.Data.Type != "shell" || job.Data.Terminal {
				return out, appwire.InvalidParams("job is not a live shell owned by the addressed session")
			}
			updatedJob := *request.Job
			updatedJob.Ref = ownerRef
			request.Job = &updatedJob
		} else {
			delegate := *request.OwnedDelegate
			delegate.Ref = ownerRef
			request.OwnedDelegate = &delegate
		}
		return stopper.StopJob(ctx, request)
	})
}

func clearThreadControl(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, p appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	if strings.TrimSpace(p.ClientMutationID) == "" {
		return appwire.ThreadClearResponse{}, appwire.InvalidParams("clientMutationId is required")
	}
	if strings.TrimSpace(p.ExpectedInstanceID) == "" {
		return appwire.ThreadClearResponse{}, appwire.InvalidParams("expectedInstanceId is required")
	}
	result, err := clearThreadWithResume(ctx, cfg, sources, p)
	err = adoptCallerMutationResult(&result.Receipt, err, p.ClientMutationID)
	if err == nil {
		result.Thread = applyHubCapabilities(cfg, result.Thread)
	}
	return result, err
}

func setReasoningEffortControl(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadReasoningEffortSetParams) (appwire.EmptyResponse, error) {
	return withSessionActionOwnership(ctx, cfg, params.Ref, "", func() (appwire.EmptyResponse, error) {
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
}

func drainQueueControl(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
	if err := validateAppWireInputItems(params.Input); err != nil {
		return appwire.TurnDrainAsSteerResponse{}, appwire.InvalidParams(err.Error())
	}
	if strings.TrimSpace(params.ClientMutationID) == "" {
		return appwire.TurnDrainAsSteerResponse{}, appwire.InvalidParams("clientMutationId is required")
	}
	resp, err := withDeletionTargetOwnership(ctx, cfg, params.Ref, "", params.ClientMutationID, func() (appwire.TurnDrainAsSteerResponse, error) {
		source, err := sourceForThread(sources, params.Ref, "")
		if err != nil {
			return appwire.TurnDrainAsSteerResponse{}, err
		}
		if err := ensureSkillInputSupported(ctx, source, params.Ref, "", params.Input); err != nil {
			return appwire.TurnDrainAsSteerResponse{}, err
		}
		return source.DrainAsSteer(ctx, params)
	})
	err = adoptCallerMutationResult(&resp.Receipt, err, params.ClientMutationID)
	return resp, err
}

func promoteQueuedControl(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
	if params.Index < 0 {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.InvalidParams("index must be >= 0")
	}
	if strings.TrimSpace(params.ClientMutationID) == "" {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.InvalidParams("clientMutationId is required")
	}
	if strings.TrimSpace(params.ExpectedEntryID) == "" {
		return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.InvalidParams("expectedEntryId is required")
	}
	resp, err := withDeletionTargetOwnership(ctx, cfg, params.Ref, "", params.ClientMutationID, func() (appwire.TurnPromoteQueuedAsSteerResponse, error) {
		source, err := sourceForThread(sources, params.Ref, "")
		if err != nil {
			return appwire.TurnPromoteQueuedAsSteerResponse{}, err
		}
		return source.PromoteQueuedAsSteer(ctx, params)
	})
	err = adoptCallerMutationResult(&resp.Receipt, err, params.ClientMutationID)
	return resp, err
}

func cancelQueuedControl(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
	if params.Index < 0 {
		return appwire.TurnCancelQueuedResponse{}, appwire.InvalidParams("index must be >= 0")
	}
	if strings.TrimSpace(params.ClientMutationID) == "" {
		return appwire.TurnCancelQueuedResponse{}, appwire.InvalidParams("clientMutationId is required")
	}
	if strings.TrimSpace(params.ExpectedEntryID) == "" {
		return appwire.TurnCancelQueuedResponse{}, appwire.InvalidParams("expectedEntryId is required")
	}
	resp, err := withDeletionTargetOwnership(ctx, cfg, params.Ref, "", params.ClientMutationID, func() (appwire.TurnCancelQueuedResponse, error) {
		source, err := sourceForThread(sources, params.Ref, "")
		if err != nil {
			return appwire.TurnCancelQueuedResponse{}, err
		}
		return source.CancelQueued(ctx, params)
	})
	err = adoptCallerMutationResult(&resp.Receipt, err, params.ClientMutationID)
	return resp, err
}

func interruptTurnControl(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
	if strings.TrimSpace(params.ClientMutationID) == "" {
		return appwire.TurnInterruptResponse{}, appwire.InvalidParams("clientMutationId is required")
	}
	resp, err := withDeletionTargetOwnership(ctx, cfg, params.Ref, params.ThreadID, params.ClientMutationID, func() (appwire.TurnInterruptResponse, error) {
		source, err := sourceForThread(sources, params.Ref, params.ThreadID)
		if err != nil {
			return appwire.TurnInterruptResponse{}, err
		}
		return source.InterruptTurn(ctx, params)
	})
	err = adoptCallerMutationResult(&resp.Receipt, err, params.ClientMutationID)
	return resp, err
}

func stopDelegateControl(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.DelegateStopParams) (appwire.DelegateStopResponse, error) {
	return withSessionActionOwnership(ctx, cfg, params.Ref, params.ThreadID, func() (appwire.DelegateStopResponse, error) {
		if err := refreshDaemonRestartRequiredError(ctx, cfg, params.Ref, params.ThreadID, ""); err != nil {
			return appwire.DelegateStopResponse{}, err
		}
		source, err := sourceForThread(sources, params.Ref, params.ThreadID)
		if err != nil {
			return appwire.DelegateStopResponse{}, err
		}
		stopper, ok := source.(appsource.DelegateStopSource)
		if !ok {
			return appwire.DelegateStopResponse{}, appwire.Unavailable("this session's source cannot stop a subagent")
		}
		return stopper.StopDelegate(ctx, params)
	})
}
