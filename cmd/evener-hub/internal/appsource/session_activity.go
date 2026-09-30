package appsource

import (
	"context"
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
	var out appwire.SessionDelegatesResponse
	if err := s.call(ctx, appwire.MethodEvenerThreadDelegatesList, params, &out); err != nil {
		return appwire.SessionDelegatesResponse{}, err
	}
	return out, nil
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
	var out appwire.SessionJobsResponse
	if err := s.call(ctx, appwire.MethodEvenerThreadJobsList, params, &out); err != nil {
		return appwire.SessionJobsResponse{}, err
	}
	return out, nil
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
	var out appwire.SessionWatchesResponse
	if err := s.call(ctx, appwire.MethodEvenerThreadWatchesList, params, &out); err != nil {
		return appwire.SessionWatchesResponse{}, err
	}
	return out, nil
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
