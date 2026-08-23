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

// Caller executes cheap-model requests for one session.
type Caller struct {
	client *llm.Client

	mu      sync.Mutex
	refused map[route]struct{}
}

// New returns a session-scoped cheap-model caller.
func New(client *llm.Client) *Caller {
	return &Caller{client: client, refused: make(map[route]struct{})}
}

// Complete resolves and executes a cheap-model request, falling back once to
// the session model when the provider refuses the resolved model. It resolves
// through the profile's cheap-model ref, which falls through to the provider's
// default cheap model when the session configured none.
func (c *Caller) Complete(ctx context.Context, profile *provider.Profile, req llm.Request) (llm.Response, error) {
	cheapProvider, cheapModel := profile.CheapModelRef()
	resp, _, err := c.run(ctx, profile, route{provider: cheapProvider, model: cheapModel}, req)
	return resp, err
}

// CompleteConfigured is Complete for work too costly to hand to a model nobody
// chose: it uses only an explicitly configured cheap model and runs on the
// session's own model when none is configured, where Complete would fall
// through to the provider's default cheap model.
//
// It reports whether it ran the session model, so a caller that layers routes
// of its own on top of this one does not re-run a route already tried here.
func (c *Caller) CompleteConfigured(ctx context.Context, profile *provider.Profile, req llm.Request) (resp llm.Response, ranSessionModel bool, err error) {
	cheap := route{provider: profile.CheapProvider(), model: profile.ConfiguredCheapModel()}
	if cheap.model == "" {
		cheap = sessionModel(profile)
	}
	return c.run(ctx, profile, cheap, req)
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

	req.Provider, req.Model = cheap.provider, cheap.model
	resp, err := c.client.Complete(ctx, req)
	if err == nil || cheap == active || !refusesModel(err) {
		return resp, cheap == active, err
	}

	req.Provider, req.Model = active.provider, active.model
	fallbackResp, fallbackErr := c.client.Complete(ctx, req)
	if fallbackErr != nil {
		// Both failures matter to whoever reads this, so join them. The join
		// order does not settle classification: llm.Kind resolves a joined
		// error by a fixed precedence over the whole chain, under which the
		// refusal's KindInvalidRequest outranks a terminal KindServer. What is
		// load-bearing is WrapContextError, which lifts a bare context deadline
		// on the fallback attempt into a KindTimeout — a kind that does outrank
		// KindInvalidRequest — so a session model that ran out of time is not
		// reported as a bad request.
		fallbackErr = llm.WrapContextError(active.provider, fallbackErr)
		return llm.Response{}, true, errors.Join(fallbackErr, err)
	}
	c.remember(cheap)
	return fallbackResp, true, nil
}

func sessionModel(profile *provider.Profile) route {
	return route{provider: profile.ID(), model: profile.Model()}
}

func (c *Caller) serves(r route) bool {
	c.mu.Lock()
	_, refused := c.refused[r]
	c.mu.Unlock()
	return !refused && c.client.ValidateModelCompatibility(r.provider, r.model) == nil
}

func (c *Caller) remember(r route) {
	c.mu.Lock()
	c.refused[r] = struct{}{}
	c.mu.Unlock()
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
// pair, serves(), already skips pairs the client's own model-compatibility
// validator knows about, so a new wording costs the fallback, not the session.
func refusesModel(err error) bool {
	if llm.Classify(err) != llm.ErrorClassPermanent {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "model is not supported when using codex with a chatgpt account") ||
		strings.Contains(message, "provided model identifier is invalid")
}
