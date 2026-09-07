package hub

import (
	"context"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func setThreadVisionModelWithResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadVisionModelSetParams) error {
	err := setThreadVisionModelOnce(ctx, cfg, sources, params)
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
	return setThreadVisionModelOnce(ctx, cfg, sources, params)
}

func setThreadVisionModelOnce(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadVisionModelSetParams) error {
	_, err := withDeletionTargetOwnership(ctx, cfg, params.Ref, "", "", func() (struct{}, error) {
		source, err := sourceForThread(sources, params.Ref, "")
		if err != nil {
			return struct{}{}, err
		}
		if err := ensureThreadActionAvailable(ctx, source, params.Ref, "", "vision-model"); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, source.SetThreadVisionModel(ctx, params)
	})
	return err
}
