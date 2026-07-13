package main

import (
	"context"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

func setThreadModelWithResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadModelSetParams) error {
	err := setThreadModelOnce(ctx, cfg, sources, params)
	if err == nil {
		return nil
	}
	if params.Ref != "" && !hubKnowsRef(cfg, params.Ref) {
		return err
	}
	if !shouldResumeAfterSessionUnavailable(err) {
		return err
	}
	if _, resumeErr := hubThreadResume(ctx, cfg, sources, appwire.ThreadResumeParams{Ref: params.Ref}); resumeErr != nil {
		return resumeErr
	}
	return setThreadModelOnce(ctx, cfg, sources, params)
}

func setThreadModelOnce(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadModelSetParams) error {
	source, err := sourceForThreadWithManagedLaunch(ctx, cfg, sources, params.Ref, "")
	if err != nil {
		return err
	}
	if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "model"); err != nil {
		return err
	}
	return source.SetThreadModel(ctx, params)
}
