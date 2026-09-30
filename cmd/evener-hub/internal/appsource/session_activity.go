package appsource

import (
	"context"
	"encoding/json"
	"slices"

	"primeradiant.com/evener/appwire"
)

func (s *LocalDaemonSource) ThreadActivityRead(ctx context.Context, params appwire.SessionActivityReadParams) (appwire.SessionActivitySummary, error) {
	entry, err := s.entryForReadRef(params.Ref, "")
	if err != nil {
		return appwire.SessionActivitySummary{}, err
	}
	var out appwire.SessionActivitySummary
	err = s.withClient(ctx, entry, func(ctx context.Context, client *appwire.Client) error {
		var callErr error
		out, callErr = client.ThreadActivityRead(ctx, params)
		return callErr
	})
	return out, err
}

func (s *RemoteHubSource) ThreadActivityRead(ctx context.Context, params appwire.SessionActivityReadParams) (appwire.SessionActivitySummary, error) {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return appwire.SessionActivitySummary{}, err
	}
	params.Ref = ref.String()
	var out appwire.SessionActivitySummary
	if err := s.call(ctx, appwire.MethodEvenerThreadActivityRead, params, &out); err != nil {
		return appwire.SessionActivitySummary{}, err
	}
	return out, nil
}

func (s *LocalDaemonSource) ThreadDelegatesList(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionDelegatesResponse, error) {
	entry, err := s.entryForReadRef(params.Ref, "")
	if err != nil {
		return appwire.SessionDelegatesResponse{}, err
	}
	var out appwire.SessionDelegatesResponse
	err = s.withClient(ctx, entry, func(ctx context.Context, client *appwire.Client) error {
		var callErr error
		out, callErr = client.ThreadDelegatesList(ctx, params)
		return callErr
	})
	return out, err
}

func (s *RemoteHubSource) ThreadDelegatesList(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionDelegatesResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return appwire.SessionDelegatesResponse{}, err
	}
	params.Ref = ref.String()
	return readTranslatedActivityPage[appwire.SessionDelegatesResponse](ctx, s, appwire.MethodEvenerThreadDelegatesList, params)
}

func (s *LocalDaemonSource) ThreadJobsList(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionJobsResponse, error) {
	entry, err := s.entryForReadRef(params.Ref, "")
	if err != nil {
		return appwire.SessionJobsResponse{}, err
	}
	var out appwire.SessionJobsResponse
	err = s.withClient(ctx, entry, func(ctx context.Context, client *appwire.Client) error {
		var callErr error
		out, callErr = client.ThreadJobsList(ctx, params)
		return callErr
	})
	return out, err
}

func (s *RemoteHubSource) ThreadJobsList(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionJobsResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return appwire.SessionJobsResponse{}, err
	}
	params.Ref = ref.String()
	return readTranslatedActivityPage[appwire.SessionJobsResponse](ctx, s, appwire.MethodEvenerThreadJobsList, params)
}

func (s *LocalDaemonSource) ThreadWatchesList(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionWatchesResponse, error) {
	entry, err := s.entryForReadRef(params.Ref, "")
	if err != nil {
		return appwire.SessionWatchesResponse{}, err
	}
	var out appwire.SessionWatchesResponse
	err = s.withClient(ctx, entry, func(ctx context.Context, client *appwire.Client) error {
		var callErr error
		out, callErr = client.ThreadWatchesList(ctx, params)
		return callErr
	})
	return out, err
}

func (s *RemoteHubSource) ThreadWatchesList(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionWatchesResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return appwire.SessionWatchesResponse{}, err
	}
	params.Ref = ref.String()
	return readTranslatedActivityPage[appwire.SessionWatchesResponse](ctx, s, appwire.MethodEvenerThreadWatchesList, params)
}

func (s *RemoteHubSource) translateSessionActivity(context *appwire.SessionActivityContext, page *appwire.SessionActivityPage, refs ...*string) error {
	context.Ancestors = slices.Clone(context.Ancestors)
	refs = append(refs, &context.Ref, &context.RootRef, &context.ParentRef)
	for i := range context.Ancestors {
		refs = append(refs, &context.Ancestors[i].Ref)
	}
	if page != nil {
		page.Issues = slices.Clone(page.Issues)
		for i := range page.Issues {
			refs = append(refs, &page.Issues[i].Ref)
		}
	}
	for _, target := range refs {
		translated, err := s.fromRemoteRefString(*target)
		if err != nil {
			return err
		}
		*target = translated
	}
	return nil
}

// readTranslatedActivityPage fits the qualified response by rereading the same
// opaque input cursor. Dropping rows from a returned page would skip identities
// because its next cursor already advances past those rows.
func readTranslatedActivityPage[R any](ctx context.Context, source *RemoteHubSource, method string, params appwire.SessionActivityListParams) (R, error) {
	var zero R
	limit := params.Limit
	if limit == 0 {
		limit = 50
	}
	limit = min(limit, 200)
	for {
		var out R
		if err := source.call(ctx, method, params, &out); err != nil {
			return zero, err
		}
		encoded, err := json.Marshal(out)
		if err != nil {
			return zero, err
		}
		if len(encoded) <= 256<<10 {
			return out, nil
		}
		if limit <= 1 {
			return zero, appwire.InternalError("qualified session activity response exceeds 256 KiB")
		}
		limit = max(1, limit/2)
		params.Limit = limit
	}
}
