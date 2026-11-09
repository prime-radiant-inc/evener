package hub

import (
	"context"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func setThreadModelWithResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, relays *hubRelay, params appwire.ThreadModelSetParams) error {
	_, err := relayWithResumeAfter(ctx, cfg, sources, relays, params.Ref, "", "model",
		func(source appsource.Source) (struct{}, error) {
			return struct{}{}, source.SetThreadModel(ctx, params)
		})
	return err
}
