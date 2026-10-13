package hub

import (
	"context"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// hubJobsList answers evener/jobs/list. A running daemon's recursive activity
// tree is authoritative, so it is always tried first; only the specific
// dead-session condition (isDeadSessionError, app_tasks.go) falls back to the
// persisted jobs.jsonl through agent.LoadSessionJobActivityTree, behind the
// same past-index gate pastTasksListResponse uses.
func hubJobsList(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.JobsListParams) (appwire.JobsListResponse, error) {
	source, err := sourceForThreadWithDeletionFence(ctx, cfg, sources, params.Ref, "")
	var resp appwire.JobsListResponse
	if err == nil {
		resp, err = source.ListJobs(ctx, params)
	}
	if err == nil {
		return resp, nil
	}
	if !isDeadSessionError(err) {
		return appwire.JobsListResponse{}, err
	}
	pastResp, ok, pastErr := pastJobsListResponse(ctx, cfg, params)
	if pastErr != nil {
		return appwire.JobsListResponse{}, pastErr
	}
	if ok {
		return pastResp, nil
	}
	return appwire.JobsListResponse{}, err
}

// pastJobsListResponse gates on pastEntryForRead (app_threadread.go), the
// same gate hubJobsOutput's fallback uses: the ref must resolve to a LOCAL
// past thread id the index already knows, so a job tree is never returned for
// a session the hub cannot otherwise account for — and never from local state
// for another source's ref. Only then does it read the session's persisted
// jobs.jsonl. ctx flows through to LoadSessionJobActivityTree so a canceled
// or timed-out hub request stops the persisted-tree walk instead of reading
// every visited session's journal regardless.
func pastJobsListResponse(ctx context.Context, cfg hubcore.WebConfig, params appwire.JobsListParams) (appwire.JobsListResponse, bool, error) {
	entry, ok := pastEntryForRead(cfg, appwire.ThreadReadParams{Ref: params.Ref})
	if !ok {
		return appwire.JobsListResponse{}, false, nil
	}
	tree, err := agent.LoadSessionJobActivityTree(ctx, entry.StateDir, entry.Meta.ID, params)
	if err != nil {
		return appwire.JobsListResponse{}, true, err
	}
	return appwire.JobsListResponse{Data: tree}, true, nil
}

// hubJobRead runs one single-job read live-first: the owning source is
// authoritative, and only the specific dead-session condition
// (isDeadSessionError, app_tasks.go) falls back to the persisted jobs.jsonl
// behind the past-index gate (pastEntryForRead). A job id the persisted store
// has never heard of is invalid params — the caller guessed — not the
// dead-session error that triggered the fallback. Both job reads share this
// one policy; live is the source's own read and past loads the same job from
// the journal.
func hubJobRead[T any](
	ctx context.Context,
	cfg hubcore.WebConfig,
	sources *appsource.Registry,
	ref, jobID string,
	live func(source appsource.Source) (T, error),
	past func(stateDir, sessionID, jobID string) (T, bool, error),
) (T, error) {
	var zero T
	source, err := sourceForThreadWithDeletionFence(ctx, cfg, sources, ref, "")
	if err == nil {
		out, liveErr := live(source)
		if liveErr == nil {
			return out, nil
		}
		err = liveErr
	}
	if !isDeadSessionError(err) {
		return zero, err
	}
	entry, ok := pastEntryForRead(cfg, appwire.ThreadReadParams{Ref: ref})
	if !ok {
		return zero, err
	}
	out, found, pastErr := past(entry.StateDir, entry.Meta.ID, jobID)
	if pastErr != nil {
		return zero, pastErr
	}
	if !found {
		return zero, appwire.InvalidParams("job not found: " + jobID)
	}
	return out, nil
}

// hubJobsOutput answers evener/jobs/output through hubJobRead.
func hubJobsOutput(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
	return hubJobRead(ctx, cfg, sources, params.Ref, params.JobID,
		func(source appsource.Source) (appwire.JobsOutputResponse, error) {
			return source.JobOutput(ctx, params)
		},
		func(stateDir, sessionID, jobID string) (appwire.JobsOutputResponse, bool, error) {
			tail, found, err := agent.LoadSessionJobOutputPage(stateDir, sessionID, jobID, params.BeforeBytes, params.MaxBytes)
			return appwire.JobsOutputResponse{Data: tail}, found, err
		})
}

// hubJobsGet answers evener/jobs/get through the same hubJobRead policy.
func hubJobsGet(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.JobsGetParams) (appwire.JobsGetResponse, error) {
	return hubJobRead(ctx, cfg, sources, params.Ref, params.JobID,
		func(source appsource.Source) (appwire.JobsGetResponse, error) {
			return source.JobGet(ctx, params)
		},
		func(stateDir, sessionID, jobID string) (appwire.JobsGetResponse, bool, error) {
			job, found, err := agent.LoadSessionJobGet(stateDir, sessionID, jobID)
			return appwire.JobsGetResponse{Data: job}, found, err
		})
}
