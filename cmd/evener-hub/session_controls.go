package hub

import (
	"context"
	"primeradiant.com/evener/appwire"
)

func (o *sessionOperations) resolveApproval(ctx context.Context, params appwire.SandboxEscalationResolveParams) (appwire.EmptyResponse, error) {
	cfg, sources := o.cfg, o.sources
	return withSessionActionOwnership(ctx, cfg, params.Ref, params.ThreadID, func() (appwire.EmptyResponse, error) {
		if err := refreshDaemonRestartRequiredError(ctx, cfg, params.Ref, params.ThreadID, ""); err != nil {
			return appwire.EmptyResponse{}, err
		}
		source, err := sourceForThread(sources, params.Ref, params.ThreadID)
		if err != nil {
			return appwire.EmptyResponse{}, err
		}
		return appwire.EmptyResponse{}, source.ResolveSandboxEscalation(ctx, params)
	})
}

func (o *sessionOperations) Control(ctx context.Context, request appwire.SessionControlRequest) (appwire.SessionControlResponse, error) {
	if err := request.Validate(); err != nil {
		return appwire.SessionControlResponse{}, err
	}
	result, err := o.resolveApproval(ctx, *request.Approval)
	return appwire.SessionControlResponse{Result: result}, err
}
