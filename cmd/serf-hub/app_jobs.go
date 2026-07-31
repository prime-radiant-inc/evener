package main

import (
	"context"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// hubJobsList answers serf/jobs/list. A running daemon's jobstore is
// authoritative, so it is always tried first; only the specific dead-session
// condition (isDeadSessionError, app_tasks.go) falls back to the persisted
// jobs.jsonl through agent.LoadSessionJobList, behind the same past-index
// gate pastTasksListResponse uses.
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

// pastJobsListResponse mirrors pastTasksListResponse's past-index gating
// (app_tasks.go): it resolves ref to a local past thread id and requires that
// id to already be known to the past index before serving anything, so a job
// list is never returned for a session the hub cannot otherwise account for.
// Only then does it read the session's persisted jobs.jsonl.
func pastJobsListResponse(cfg hubcore.WebConfig, params appwire.JobsListParams) (appwire.JobsListResponse, bool, error) {
	if cfg.Past == nil {
		return appwire.JobsListResponse{}, false, nil
	}
	threadID, ok := localPastThreadID(appwire.ThreadReadParams{Ref: params.Ref})
	if !ok {
		return appwire.JobsListResponse{}, false, nil
	}
	entry, ok := cfg.Past.Find(threadID)
	if !ok {
		return appwire.JobsListResponse{}, false, nil
	}
	jobs, err := agent.LoadSessionJobList(entry.StateDir, entry.Meta.ID)
	if err != nil {
		return appwire.JobsListResponse{}, true, err
	}
	return appwire.JobsListResponse{Data: jobs}, true, nil
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
	if cfg.Past == nil {
		return appwire.JobsOutputResponse{}, err
	}
	threadID, ok := localPastThreadID(appwire.ThreadReadParams{Ref: params.Ref})
	if !ok {
		return appwire.JobsOutputResponse{}, err
	}
	entry, ok := cfg.Past.Find(threadID)
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
