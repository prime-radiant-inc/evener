// Package cheapmodel routes a session's auxiliary LLM calls and remembers
// provider/model pairs that the provider has refused to serve.
package cheapmodel

import (
	"context"
	"errors"
	"strings"
	"sync"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
)

type route struct {
	provider string
	model    string
}

type probeCall struct {
	ready   chan struct{}
	err     error
	refused bool
	// fallbacks counts callers that joined this flight and have not yet
	// settled their cheap-to-session resolution.
	fallbacks int
}

// ErrAllModelsRefused marks a request for which both the resolved cheap model
// and the session model returned a permanent model-refusal error. Consumers may
// use this to choose a non-LLM fallback while retaining the underlying errors.
var ErrAllModelsRefused = errors.New("all auxiliary models refused")

// Caller executes cheap-model requests for one session.
type Caller struct {
	client *llm.Client

	mu      sync.Mutex
	refused map[route]struct{}
	probes  map[route]*probeCall
}

// New returns a session-scoped cheap-model caller.
func New(client *llm.Client) *Caller {
	return &Caller{
		client:  client,
		refused: make(map[route]struct{}),
		probes:  make(map[route]*probeCall),
	}
}

// Complete resolves and executes a cheap-model request, falling back once to
// the session model when the provider refuses the resolved model. It resolves
// through the profile's cheap-model ref, which uses the session model when no
// cheap model is configured.
func (c *Caller) Complete(ctx context.Context, profile *provider.Profile, req llm.Request) (llm.Response, error) {
	cheapProvider, cheapModel := profile.CheapModelRef()
	return c.CompleteRouted(ctx, profile, cheapProvider, cheapModel, req)
}

// CompleteRouted is Complete for an explicit route chosen by the caller rather
// than the profile's cheap-model ref — e.g. the vision side-channel's
// configured vision model. It shares Complete's refusal learning and
// session-model fallback; an empty model or a route equal to the session
// route runs on the session model.
func (c *Caller) CompleteRouted(ctx context.Context, profile *provider.Profile, providerName, modelID string, req llm.Request) (llm.Response, error) {
	resp, _, err := c.run(ctx, profile, route{provider: providerName, model: modelID}, req)
	return resp, err
}

// CompleteConfigured resolves the same route as Complete and reports whether it
// ran the session model, so a caller that layers routes does not repeat it.
func (c *Caller) CompleteConfigured(ctx context.Context, profile *provider.Profile, req llm.Request) (resp llm.Response, ranSessionModel bool, err error) {
	providerName, modelID := profile.CheapModelRef()
	return c.run(ctx, profile, route{provider: providerName, model: modelID}, req)
}

// run executes one resolution. Both the cheap and the fallback attempt share
// one API-attempt group so callers see a single logical attempt group covering
// the whole resolution.
func (c *Caller) run(ctx context.Context, profile *provider.Profile, cheap route, req llm.Request) (llm.Response, bool, error) {
	ctx, scope := llm.BeginAPIAttemptGroupScope(ctx)
	resp, ranSessionModel, err := c.complete(ctx, profile, cheap, req)
	scope.SettleResult(err)
	return resp, ranSessionModel, err
}

func (c *Caller) complete(ctx context.Context, profile *provider.Profile, cheap route, req llm.Request) (llm.Response, bool, error) {
	active := sessionModel(profile)
	if cheap != active && !c.serves(cheap) {
		cheap = active
	}

	var refusedProbe *probeCall
	var resp llm.Response
	var err error
	if cheap == active {
		req.Provider, req.Model = active.provider, active.model
		resp, err = c.client.Complete(ctx, req)
	} else {
		req.Provider, req.Model = cheap.provider, cheap.model
		var skipped bool
		resp, refusedProbe, skipped, err = c.probe(ctx, cheap, req)
		if skipped {
			// The route was latched after the first serves check but before the
			// probe acquired c.mu. Re-run this request on the active model.
			cheap = active
			req.Provider, req.Model = active.provider, active.model
			resp, err = c.client.Complete(ctx, req)
		}
	}
	if err == nil || cheap == active || !refusesModel(err) {
		return resp, cheap == active, err
	}

	req.Provider, req.Model = active.provider, active.model
	fallbackResp, fallbackErr := c.client.Complete(ctx, req)
	if fallbackErr != nil {
		allModelsRefused := refusesModel(fallbackErr)
		// Both failures matter to whoever reads this, so join them. The join
		// order does not settle classification: llm.Kind resolves a joined
		// error by a fixed precedence over the whole chain, under which the
		// refusal's KindInvalidRequest outranks a terminal KindServer. What is
		// load-bearing is WrapContextError, which lifts a bare context deadline
		// on the fallback attempt into a KindTimeout — a kind that does outrank
		// KindInvalidRequest — so a session model that ran out of time is not
		// reported as a bad request.
		fallbackErr = llm.WrapContextError(active.provider, fallbackErr)
		joinedErr := errors.Join(fallbackErr, err)
		if allModelsRefused {
			c.finishProbe(cheap, refusedProbe, false)
			return llm.Response{}, true, errors.Join(ErrAllModelsRefused, joinedErr)
		}
		c.finishProbe(cheap, refusedProbe, false)
		return llm.Response{}, true, joinedErr
	}
	c.finishProbe(cheap, refusedProbe, true)
	return fallbackResp, true, nil
}

// probe runs one request on a cheap route. Successful and ordinary failed
// requests are never shared: callers need their own response and error. Only a
// model refusal is shared, because that result is a route property and is safe
// to reuse for different auxiliary prompts. A refusal call stays registered
// until its first fallback succeeds or every fallback participant settles
// without success, so a caller arriving during the fallback wave cannot send a
// second cheap probe.
func (c *Caller) probe(ctx context.Context, r route, req llm.Request) (llm.Response, *probeCall, bool, error) {
	c.mu.Lock()
	if _, refused := c.refused[r]; refused {
		c.mu.Unlock()
		return llm.Response{}, nil, true, nil
	}
	call, waiting := c.probes[r]
	if !waiting {
		call = &probeCall{ready: make(chan struct{}), fallbacks: 1}
		c.probes[r] = call
	} else {
		call.fallbacks++
	}
	c.mu.Unlock()

	if waiting {
		if err := ctx.Err(); err != nil {
			c.releaseProbeWaiter(r, call)
			return llm.Response{}, nil, false, err
		}
		select {
		case <-call.ready:
			if err := ctx.Err(); err != nil {
				c.releaseProbeWaiter(r, call)
				return llm.Response{}, nil, false, err
			}
			c.mu.Lock()
			refused := call.refused
			callErr := call.err
			if !refused {
				c.releaseProbeWaiterLocked(r, call)
			}
			c.mu.Unlock()
			if refused {
				return llm.Response{}, call, false, callErr
			}
			// The leader's response is not valid for this request. Execute
			// this request independently instead of electing another
			// serialized probe leader.
			resp, err := c.client.Complete(ctx, req)
			return resp, nil, false, err
		case <-ctx.Done():
			c.releaseProbeWaiter(r, call)
			return llm.Response{}, nil, false, ctx.Err()
		}
	}

	resp, err := c.client.Complete(ctx, req)
	refused := err != nil && refusesModel(err)
	c.mu.Lock()
	call.err = err
	call.refused = refused
	close(call.ready)
	if !refused {
		delete(c.probes, r)
	}
	c.mu.Unlock()
	if refused {
		return resp, call, false, err
	}
	return resp, nil, false, err
}

func (c *Caller) releaseProbeWaiter(r route, call *probeCall) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.releaseProbeWaiterLocked(r, call)
}

func (c *Caller) releaseProbeWaiterLocked(r route, call *probeCall) {
	if c.probes[r] != call {
		return
	}
	call.fallbacks--
	if call.fallbacks == 0 {
		delete(c.probes, r)
	}
}

func (c *Caller) finishProbe(r route, call *probeCall, succeeded bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if succeeded {
		c.refused[r] = struct{}{}
		if call != nil && c.probes[r] == call {
			delete(c.probes, r)
		}
		return
	}
	if call != nil && c.probes[r] == call {
		call.fallbacks--
		if call.fallbacks != 0 {
			return
		}
		delete(c.probes, r)
	}
}

func sessionModel(profile *provider.Profile) route {
	return route{provider: profile.ID(), model: profile.Model()}
}

func (c *Caller) serves(r route) bool {
	c.mu.Lock()
	_, refused := c.refused[r]
	c.mu.Unlock()
	return !refused && c.client.CanServe(r.provider, r.model)
}

// refusesModel reports whether err is the provider saying it will not serve the
// requested model at all, rather than rejecting this particular request.
//
// The two substrings are wordings observed in the field (Codex on a ChatGPT
// account, and Bedrock). Matching them conservatively is deliberate: a broader
// match would cost a session its cheap model over an ordinary bad request. The
// price of that conservatism is maintenance — if a provider rewords its refusal
// this reactive path silently stops firing and auxiliary calls fail instead of
// falling back. That degradation is bounded because the proactive half of the
// pair, serves(), already skips routes the client says it cannot serve
// (CanServe), so a new wording costs the fallback, not the session.
func refusesModel(err error) bool {
	if llm.Classify(err) != llm.ErrorClassPermanent {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "model is not supported when using codex with a chatgpt account") ||
		strings.Contains(message, "provided model identifier is invalid")
}
