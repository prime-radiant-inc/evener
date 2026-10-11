package hub

import (
	"context"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

func registerSessionActivityHandlers(server *appserver.Server, cfg hubcore.WebConfig, sources *appsource.Registry) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerThreadActivityRead, func(ctx context.Context, params appwire.SessionActivityReadParams) (appwire.SessionActivitySummary, error) {
		return readSessionActivity(ctx, cfg, sources, params.Ref,
			func(source appsource.Source) (appwire.SessionActivitySummary, error) {
				return source.ThreadActivityRead(ctx, params)
			},
			func(entry hubcore.PastEntry) (appwire.SessionActivitySummary, error) {
				params.Ref = appwire.Ref{SourceID: "local", ThreadID: entry.Meta.ID}.String()
				return agent.LoadSessionActivitySummary(ctx, entry.StateDir, entry.Meta.ID, params)
			})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerThreadDelegatesList, func(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionDelegatesResponse, error) {
		return readSessionActivity(ctx, cfg, sources, params.Ref,
			func(source appsource.Source) (appwire.SessionDelegatesResponse, error) {
				return source.ThreadDelegatesList(ctx, params)
			},
			func(entry hubcore.PastEntry) (appwire.SessionDelegatesResponse, error) {
				params.Ref = appwire.Ref{SourceID: "local", ThreadID: entry.Meta.ID}.String()
				return agent.LoadSessionActivityDelegates(ctx, entry.StateDir, entry.Meta.ID, params)
			})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerThreadJobsList, func(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionJobsResponse, error) {
		return readSessionActivity(ctx, cfg, sources, params.Ref,
			func(source appsource.Source) (appwire.SessionJobsResponse, error) {
				return source.ThreadJobsList(ctx, params)
			},
			func(entry hubcore.PastEntry) (appwire.SessionJobsResponse, error) {
				params.Ref = appwire.Ref{SourceID: "local", ThreadID: entry.Meta.ID}.String()
				return agent.LoadSessionActivityJobs(ctx, entry.StateDir, entry.Meta.ID, params)
			})
	})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerThreadWatchesList, func(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionWatchesResponse, error) {
		return readSessionActivity(ctx, cfg, sources, params.Ref,
			func(source appsource.Source) (appwire.SessionWatchesResponse, error) {
				return source.ThreadWatchesList(ctx, params)
			},
			func(entry hubcore.PastEntry) (appwire.SessionWatchesResponse, error) {
				params.Ref = appwire.Ref{SourceID: "local", ThreadID: entry.Meta.ID}.String()
				return agent.LoadSessionActivityWatches(ctx, entry.StateDir, entry.Meta.ID, params)
			})
	})
}

// readSessionActivity uses the source's live view first. Only a known local
// past session can answer after its daemon is unavailable.
func readSessionActivity[R any](ctx context.Context, cfg hubcore.WebConfig, sources *appsource.Registry, ref string, live func(appsource.Source) (R, error), retained func(hubcore.PastEntry) (R, error)) (R, error) {
	var zero R
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if _, err := appwire.ParseRef(ref); err != nil {
		return zero, appwire.InvalidParams(err.Error())
	}
	source, err := sourceForThreadWithDeletionFence(ctx, cfg, sources, ref, "")
	if err == nil {
		var response R
		response, err = live(source)
		if err == nil {
			return response, nil
		}
	}
	if !isDeadSessionError(err) {
		return zero, err
	}
	if cancelErr := ctx.Err(); cancelErr != nil {
		return zero, cancelErr
	}
	entry, ok := pastEntryForRead(cfg, appwire.ThreadReadParams{Ref: ref})
	if !ok {
		return zero, err
	}
	return retained(entry)
}
