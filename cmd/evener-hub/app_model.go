package hub

import (
	"context"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func setThreadModelWithResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, relays *hubRelay, params appwire.ThreadModelSetParams) (appwire.ThreadModelSetResponse, error) {
	// Validate before the resume wrapper: a request missing an authority field
	// can never be honored, and the wrapper's resume would start a daemon for
	// it. The daemon validates the same fields again at its own ingress.
	if err := params.Validate(); err != nil {
		return appwire.ThreadModelSetResponse{}, err
	}
	return relayWithResumeAfter(ctx, cfg, sources, relays, params.Ref, params.ClientMutationID, "model",
		func(source appsource.Source) (appwire.ThreadModelSetResponse, error) {
			return source.SetThreadModel(ctx, params)
		})
}
