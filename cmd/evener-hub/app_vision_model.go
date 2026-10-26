package hub

import (
	"context"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func setThreadVisionModelWithResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadVisionModelSetParams) error {
	_, err := relayWithResume(ctx, cfg, sources, params.Ref, "", "vision-model",
		func(source appsource.Source) (struct{}, error) {
			return struct{}{}, source.SetThreadVisionModel(ctx, params)
		})
	return err
}
