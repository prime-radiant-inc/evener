package hub

import (
	"context"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func setThreadModelWithResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, relays *hubRelay, params appwire.ThreadModelSetParams) (appwire.ThreadModelSetResponse, error) {
	return relayWithResumeAfter(ctx, cfg, sources, relays, params.Ref, "", "model",
		func(source appsource.Source) (appwire.ThreadModelSetResponse, error) {
			return source.SetThreadModel(ctx, params)
		})
}
