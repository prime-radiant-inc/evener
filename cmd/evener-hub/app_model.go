package hub

import (
	"context"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
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
	_, err := withDeletionTargetOwnership(ctx, cfg, params.Ref, "", "", func() (struct{}, error) {
		source, err := sourceForThread(sources, params.Ref, "")
		if err != nil {
			return struct{}{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "model"); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, source.SetThreadModel(ctx, params)
	})
	return err
}
