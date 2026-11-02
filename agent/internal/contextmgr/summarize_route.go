package contextmgr

import (
	"context"
	"errors"
	"strings"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
)

// summarizationRoute is one (provider, model) the summarizer tries in order: the
// configured cheap model on its (possibly cross-provider) cheap provider first,
// then the active model on the main provider as a fallback.
type summarizationRoute struct {
	provider string
	model    string
}

func summarizationModels(profile *provider.Profile) []summarizationRoute {
	if profile == nil {
		return nil
	}
	active := strings.TrimSpace(profile.Model())
	configured := strings.TrimSpace(profile.ConfiguredCheapModel())

	var routes []summarizationRoute
	if configured != "" {
		routes = append(routes, summarizationRoute{provider: profile.CheapProvider(), model: configured})
	}
	if active != "" {
		activeRoute := summarizationRoute{provider: profile.ID(), model: active}
		// Skip a fallback identical to the cheap route (same provider + model).
		if len(routes) == 0 || routes[0] != activeRoute {
			routes = append(routes, activeRoute)
		}
	}
	return routes
}

func shouldFallbackSummarizationModel(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if llm.Classify(err) != llm.ErrorClassPermanent {
		return false
	}
	switch llm.Kind(err) {
	case llm.KindInvalidRequest, llm.KindNotFound, llm.KindAccessDenied:
		return true
	default:
		return false
	}
}

// summarizationResult retains the selected route for validation that spans
// multiple otherwise successful model calls, such as partner condensation.
type summarizationResult struct {
	llm.Response
	route summarizationRoute
	role  string
}

// completeSummarization runs prompt on the summarizer's routes. The cheap
// caller owns the first rung — it picks the configured cheap model, or the
// session model when none is configured, and handles a refused cheap model
// itself. This adds the second rung: summarization treats a wider class of
// failures than a model refusal as worth another model, and retries on the
// session model — but only when the cheap caller has not already run it.
//
// accept, when non-nil, judges a reply; a rejected reply also falls through to
// the session model, and is returned as an error if that reply is rejected
// too. A rejected reply was still a successful API call, so the attempt group
// settles on the API error alone.
func (cm *Manager) completeSummarization(ctx context.Context, profile *provider.Profile, prompt string, accept func(llm.Response) error) (summarizationResult, error) {
	routes := summarizationModels(profile)
	if len(routes) == 0 {
		return summarizationResult{}, errSummaryMissingModel
	}
	judge := func(resp llm.Response) error {
		if accept == nil {
			return nil
		}
		return accept(resp)
	}
	req := llm.Request{Messages: []llm.Message{llm.User(prompt)}, AdapterTimeout: cm.AdapterTimeout}
	callCtx, attemptGroupScope := llm.BeginAPIAttemptGroupScope(ctx)
	resp, ranSessionModel, apiErr := cm.cheap.CompleteConfigured(callCtx, profile, req)
	var rejected error
	if apiErr == nil {
		rejected = judge(resp)
	}
	route, role := routes[0], "cheap"
	if ranSessionModel {
		route, role = summarizationRoute{provider: profile.ID(), model: profile.Model()}, "session"
	}
	describe := func() *summaryFailureError {
		if apiErr != nil {
			return summaryFailure(apiErr, route, role)
		}
		return summaryFailure(rejected, route, role)
	}
	failure := describe()
	if !ranSessionModel && len(routes) > 1 && (rejected != nil || apiErr != nil && shouldFallbackSummarizationModel(ctx, apiErr)) {
		// More than one route means the second is the session model, distinct
		// from the configured cheap one; the caller ran only the cheap one.
		sessionRoute := routes[len(routes)-1]
		req.Provider, req.Model = sessionRoute.provider, sessionRoute.model
		resp, apiErr = cm.client.Complete(callCtx, req)
		rejected = nil
		if apiErr == nil {
			rejected = judge(resp)
		}
		route, role = sessionRoute, "session"
		previous := failure
		failure = describe()
		if failure != nil && previous != nil {
			failure.detail.Previous = previous.detail.Previous
			failure.detail.Previous = append(failure.detail.Previous, previous.detail.Terminal)
			if len(failure.detail.Previous) > 2 {
				failure.detail.Previous = failure.detail.Previous[len(failure.detail.Previous)-2:]
			}
		}
	}
	attemptGroupScope.SettleResult(apiErr)
	result := summarizationResult{Response: resp, route: route, role: role}
	if failure != nil {
		return result, failure
	}
	return result, nil
}
