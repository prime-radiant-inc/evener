package main

import (
	"context"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// hubJobsList answers serf/jobs/list. A running daemon's recursive activity
// tree is authoritative, so it is always tried first; only the specific
// dead-session condition (isDeadSessionError, app_tasks.go) falls back to the
// persisted jobs.jsonl through agent.LoadSessionJobActivityTree, behind the
// same past-index gate pastTasksListResponse uses.
func hubJobsList(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.JobsListParams) (appwire.JobsListResponse, error) {
	source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, "")
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
	pastResp, ok, pastErr := pastJobsListResponse(cfg, params)
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
// jobs.jsonl.
func pastJobsListResponse(cfg hubcore.WebConfig, params appwire.JobsListParams) (appwire.JobsListResponse, bool, error) {
	entry, ok := pastEntryForRead(cfg, appwire.ThreadReadParams{Ref: params.Ref})
	if !ok {
		return appwire.JobsListResponse{}, false, nil
	}
	tree, err := agent.LoadSessionJobActivityTree(entry.StateDir, entry.Meta.ID, params)
	if err != nil {
		return appwire.JobsListResponse{}, true, err
	}
	return appwire.JobsListResponse{Data: tree}, true, nil
}

// hubJobsOutput answers serf/jobs/output with the same live-first /
// dead-session-fallback split. A job id absent from the persisted store is
// invalid params — the caller guessed.
func hubJobsOutput(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
	source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, "")
	var resp appwire.JobsOutputResponse
	if err == nil {
		resp, err = source.JobOutput(ctx, params)
	}
	if err == nil {
		return resp, nil
	}
	if !isDeadSessionError(err) {
		return appwire.JobsOutputResponse{}, err
	}
	entry, ok := pastEntryForRead(cfg, appwire.ThreadReadParams{Ref: params.Ref})
	if !ok {
		return appwire.JobsOutputResponse{}, err
	}
	tail, found, tailErr := agent.LoadSessionJobOutputTail(entry.StateDir, entry.Meta.ID, params.JobID, params.MaxBytes)
	if tailErr != nil {
		return appwire.JobsOutputResponse{}, tailErr
	}
	if !found {
		return appwire.JobsOutputResponse{}, appwire.InvalidParams("job not found: " + params.JobID)
	}
	return appwire.JobsOutputResponse{Data: tail}, nil
}
