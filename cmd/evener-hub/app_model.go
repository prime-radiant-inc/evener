package hub

import (
	"context"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func setThreadModelWithResume(ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, relays *hubRelay, params appwire.ThreadModelSetParams) (appwire.ThreadModelSetResponse, error) {
	// A request missing an authority field can never be honored. Validating it
	// as a pre-dispatch preflight keeps it from resuming a session just to have
	// the daemon reject it, while leaving deletion ownership and recovery
	// admission in front of it, so an action captured before a recovery is
	// still refused as admission-blocked. The daemon validates the same fields
	// again at its own ingress.
	return relayWithResumeAfter(ctx, cfg, sources, relays, params.Ref, params.ClientMutationID, "model", params.Validate,
		func(source appsource.Source) (appwire.ThreadModelSetResponse, error) {
			return source.SetThreadModel(ctx, params)
		})
}
