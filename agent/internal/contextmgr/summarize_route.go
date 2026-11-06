package contextmgr

import (
	"context"
	"errors"
	"strings"

	"primeradiant.com/evener/agent/diagnostic"
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

// summarizerRouteAttempts is how many samples one route may contribute to a
// single summarization when local validation keeps rejecting its replies: one
// sample plus two same-route retries. A rejected reply is a successful API
// call whose content failed validation — usually one bad sample from a small
// or lightly aligned model — so a fresh sample on the same route is worth
// more than the next route. The session-log fork summary shares this count
// deliberately, so both summary surfaces resample in step.
const summarizerRouteAttempts = 3

// retryableSummaryRejection reports whether a local validation rejection is a
// property of one sample, so a fresh sample plausibly passes: an empty reply, a
// reply with no summary section, or an empty condensation batch. Rejections
// that are properties of the route or of the request — an output-limit cut,
// partner messages that will not condense, no configured model — get no
// same-route retry.
func retryableSummaryRejection(err error) bool {
	rejection, ok := errors.AsType[summaryRejectionError](err)
	if !ok {
		return false
	}
	switch rejection {
	case errSummaryEmptyReply, errSummaryMissingSection, errSummaryEmptyBatch:
		return true
	default:
		return false
	}
}

// priorSummaryAttempts keeps the last failed sample of each route, in the
// order the routes were first tried, capped at two entries: a total failure
// keeps one attempt per route instead of letting the terminal route's own
// retry sequence crowd the other route's evidence out.
func priorSummaryAttempts(attempts []diagnostic.SummaryAttempt) []diagnostic.SummaryAttempt {
	var kept []diagnostic.SummaryAttempt
	for _, attempt := range attempts {
		route := summarizationRoute{provider: attempt.Provider, model: attempt.Model}
		replaced := false
		for i := range kept {
			if (summarizationRoute{provider: kept[i].Provider, model: kept[i].Model}) == route {
				kept[i] = attempt
				replaced = true
				break
			}
		}
		if !replaced {
			kept = append(kept, attempt)
		}
	}
	if len(kept) > 2 {
		kept = kept[len(kept)-2:]
	}
	return kept
}

// completeSummarization runs prompt on the summarizer's routes. The cheap
// caller owns the first rung — it picks the configured cheap model, or the
// session model when none is configured, and handles a refused cheap model
// itself. This adds the second rung: summarization treats a wider class of
// failures than a model refusal as worth another model, and retries on the
// session model — but only when the cheap caller has not already run it.
//
// Within either rung, a locally rejected reply is resampled on the route that
// produced it, within that route's sample budget, before the rung's other
// recovery runs; the budget is per route, not per rung, so a refusal latch
// landing between cheap-rung retries moves the remaining budget onto the
// session route exactly as a refusal on the first sample would. A cancelled
// run stops resampling and skips the session rung, unwinding on the attempt
// already in flight.
//
// accept, when non-nil, judges a reply; a rejected reply also falls through to
// the session model — any rejected cheap-rung sample keeps the session rung
// eligible, even when a later resample fails at the API level — and is
// returned as an error if that reply is rejected too. A rejected reply was
// still a successful API call, so the attempt group settles on the API error
// alone.
func (cm *Manager) completeSummarization(ctx context.Context, profile *provider.Profile, prompt string, accept func(llm.Response) error) (summarizationResult, error) {
	routes := summarizationModels(profile)
	if len(routes) == 0 {
		return summarizationResult{}, errSummaryMissingModel
	}
	// sessionRoute is the active session model's route: the second route when
	// a cheap model is configured, the only one otherwise.
	sessionRoute := routes[len(routes)-1]
	judge := func(resp llm.Response) error {
		if accept == nil {
			return nil
		}
		return accept(resp)
	}
	req := llm.Request{Messages: []llm.Message{llm.User(prompt)}, AdapterTimeout: cm.AdapterTimeout}
	callCtx, attemptGroupScope := llm.BeginAPIAttemptGroupScope(ctx)

	// samplesByRoute budgets each route's samples across the whole resolution.
	samplesByRoute := make(map[summarizationRoute]int)
	// samples holds every failed sample's route evidence in order, including
	// the cheap caller's resolution-internal failures; the final failure keeps
	// one entry per route from it.
	var samples []diagnostic.SummaryAttempt
	var lastFailure *summaryFailureError

	var resp llm.Response
	var apiErr error
	var rejected error
	route, role := routes[0], "cheap"

	// recordSample records one sample: the route it answered on, its
	// rejection, and its failure evidence. err is the sample's API error, or
	// nil when the call succeeded and only local validation rejected the reply.
	recordSample := func(answered summarizationRoute, answeredRole string, callResp llm.Response, err error) {
		route, role = answered, answeredRole
		samplesByRoute[answered]++
		resp, apiErr = callResp, err
		rejected = nil
		if apiErr == nil {
			rejected = judge(resp)
		}
		if apiErr == nil && rejected == nil {
			lastFailure = nil
			return
		}
		sampleErr := apiErr
		if sampleErr == nil {
			sampleErr = rejected
		}
		failure := summaryFailure(sampleErr, answered, answeredRole)
		// The sample's attempt count is its route's own sample count at this
		// moment: one plus every same-route resample before it.
		failure.detail.Terminal.Attempts = samplesByRoute[answered]
		samples = append(samples, failure.detail.Previous...)
		samples = append(samples, failure.detail.Terminal)
		lastFailure = failure
	}

	// resample reports whether the route that just answered should produce
	// another sample: only a rejected sample of the retryable kind, within the
	// route's budget, on a run that is still live.
	resample := func(answered summarizationRoute, rejection error) bool {
		return rejection != nil && retryableSummaryRejection(rejection) &&
			samplesByRoute[answered] < summarizerRouteAttempts && callCtx.Err() == nil
	}

	ranSessionModel := false
	// cheapRejected remembers the cheap rung produced a rejected sample, so a
	// resample that later fails at the API level — transient or not — cannot
	// strand the fallback that rejection had earned. It is read only when the
	// rung ends without a success.
	cheapRejected := false
	for {
		resp, ranSessionModel, apiErr = cm.cheap.CompleteConfigured(callCtx, profile, req)
		if ranSessionModel {
			recordSample(sessionRoute, "session", resp, apiErr)
		} else {
			recordSample(routes[0], "cheap", resp, apiErr)
			cheapRejected = cheapRejected || rejected != nil
		}
		if !resample(route, rejected) {
			break
		}
	}
	if !ranSessionModel && len(routes) > 1 && callCtx.Err() == nil &&
		(rejected != nil || apiErr != nil && (shouldFallbackSummarizationModel(ctx, apiErr) || cheapRejected)) {
		// More than one route means the second is the session model, distinct
		// from the configured cheap one; the caller ran only the cheap one.
		for {
			req.Provider, req.Model = sessionRoute.provider, sessionRoute.model
			resp, apiErr = cm.client.Complete(callCtx, req)
			recordSample(sessionRoute, "session", resp, apiErr)
			if !resample(route, rejected) {
				break
			}
		}
	}
	attemptGroupScope.SettleResult(apiErr)
	result := summarizationResult{Response: resp, route: route, role: role}
	if lastFailure != nil {
		// The last recorded sample is the terminal attempt itself; the rest
		// reduce to one entry per route.
		lastFailure.detail.Previous = priorSummaryAttempts(samples[:len(samples)-1])
		return result, lastFailure
	}
	return result, nil
}
