package hub

import (
	"context"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func setThreadModelWithResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, params appwire.ThreadModelSetParams) error {
	_, err := relayWithResume(ctx, cfg, sources, params.Ref, "", "model",
		func(source appsource.Source) (struct{}, error) {
			return struct{}{}, source.SetThreadModel(ctx, params)
		})
	return err
}
