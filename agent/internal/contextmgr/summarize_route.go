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

// summaryRerollAttempts is how many times a route re-rolls a reply its local
// validation rejected, before the resolution moves on to the fallback route
// or fails. A rejected reply was a successful API call, so no other layer
// retries it — the LLM client's retry policy owns provider errors — and one
// more sample is cheap against the fold it saves: weak and abliterated
// models emit rejected replies (empty, malformed, truncated) intermittently.
// Deterministic local failures (uncondensable partner messages, a missing
// model) and API errors are never re-rolled.
const summaryRerollAttempts = 2

// summarizationResult retains the selected route for validation that spans
// multiple otherwise successful model calls, such as partner condensation.
type summarizationResult struct {
	llm.Response
	route summarizationRoute
	role  string
}

// rerollRejectedReply runs one summarization route until its reply is
// accepted by judge, its re-roll budget is spent on rejected replies, or the
// provider errors: an API error is never re-rolled, because the LLM client's
// retry policy has already exhausted the transient ones behind it. A
// cancellation between attempts replaces the rejection, so the failure
// reports what actually stopped the resolution. All attempts share one API
// attempt group — the caller's when it opened one (completeSummarization's
// whole resolution), a fresh one otherwise (the fork summary) — so re-rolls
// settle as one logical group on both summary surfaces.
func rerollRejectedReply(ctx context.Context, call func(context.Context) (llm.Response, error), judge func(llm.Response) error) (resp llm.Response, attempts int, callErr, rejected error) {
	callCtx, scope := llm.BeginAPIAttemptGroupScope(ctx)
	// The settle must read callErr at return time: a deferred call with
	// callErr as an argument would capture it before any call ran.
	defer func() { scope.SettleResult(callErr) }()
	resp, callErr = call(callCtx)
	attempts = 1
	if callErr == nil {
		rejected = judge(resp)
	}
	for rejected != nil && callErr == nil && attempts <= summaryRerollAttempts {
		if err := ctx.Err(); err != nil {
			callErr, rejected = err, nil
			break
		}
		resp, callErr = call(callCtx)
		attempts++
		rejected = nil
		if callErr == nil {
			rejected = judge(resp)
		}
	}
	return
}

// routeAttempt is one route's outcome inside a summarization resolution.
type routeAttempt struct {
	resp     llm.Response
	attempts int
	apiErr   error
	rejected error
}

// completeSummarization runs prompt on the summarizer's routes. The cheap
// caller owns the first rung — it picks the configured cheap model, or the
// session model when none is configured, and handles a refused cheap model
// itself. This adds the second rung: summarization treats a wider class of
// failures than a model refusal as worth another model, and retries on the
// session model — but only when the cheap caller has not already run it.
//
// accept, when non-nil, judges a reply; a rejected reply re-rolls the same
// route up to summaryRerollAttempts times before the resolution falls through
// to the session model, and is returned as an error when that reply is
// rejected too. A rejected reply was still a successful API call, so the
// attempt group settles on the API error alone.
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

	// attemptRoute adapts rerollRejectedReply to this resolution's judge.
	// The whole resolution shares attemptGroupScope: rerollRejectedReply's
	// own scope is a no-op under it, so both routes' re-rolls settle with it.
	attemptRoute := func(run func(context.Context) (llm.Response, error)) routeAttempt {
		resp, attempts, apiErr, rejected := rerollRejectedReply(callCtx, run, judge)
		return routeAttempt{resp: resp, attempts: attempts, apiErr: apiErr, rejected: rejected}
	}

	var cheapRanSession bool
	cheap := attemptRoute(func(callCtx context.Context) (llm.Response, error) {
		resp, ranSession, err := cm.cheap.CompleteConfigured(callCtx, profile, req)
		cheapRanSession = ranSession
		return resp, err
	})
	resp, apiErr, rejected := cheap.resp, cheap.apiErr, cheap.rejected
	route, role := routes[0], "cheap"
	if cheapRanSession {
		route, role = summarizationRoute{provider: profile.ID(), model: profile.Model()}, "session"
	}
	describe := func(attempts int) *summaryFailureError {
		var failure *summaryFailureError
		if apiErr != nil {
			failure = summaryFailure(apiErr, route, role)
		} else {
			failure = summaryFailure(rejected, route, role)
		}
		if failure != nil {
			failure.detail.Terminal.Attempts = attempts
		}
		return failure
	}
	failure := describe(cheap.attempts)
	if !cheapRanSession && len(routes) > 1 && (rejected != nil || apiErr != nil && shouldFallbackSummarizationModel(ctx, apiErr)) {
		// More than one route means the second is the session model, distinct
		// from the configured cheap one; the caller ran only the cheap one.
		sessionRoute := routes[len(routes)-1]
		req.Provider, req.Model = sessionRoute.provider, sessionRoute.model
		session := attemptRoute(func(callCtx context.Context) (llm.Response, error) {
			return cm.client.Complete(callCtx, req)
		})
		resp, apiErr, rejected = session.resp, session.apiErr, session.rejected
		route, role = sessionRoute, "session"
		previous := failure
		failure = describe(session.attempts)
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
