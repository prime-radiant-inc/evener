package llm

import (
	"math"

	"primeradiant.com/evener/llm/registry"
)

const minimumTokenSafetyReserve = 1_024

// TokenBudget describes the conservative input and output allocation selected
// for one request.
type TokenBudget struct {
	InputTokens     int
	SafetyTokens    int
	RequestedOutput int
	AdmittedOutput  int
	LimitedOutput   bool
}

// ApplyTokenBudget constrains a shaped request to every known input, output,
// and total-context limit. Unknown and non-positive limits add no constraint.
// It bills the tool catalog the request carries; a caller that knows the serving
// route bills that route's emitted list with ApplyTokenBudgetWithToolBytes.
func ApplyTokenBudget(req Request, res registry.Resolved) (Request, TokenBudget, error) {
	return ApplyTokenBudgetWithToolBytes(req, res, declaredToolJSONBytes(req))
}

// ApplyTokenBudgetWithToolBytes is ApplyTokenBudget over a caller-supplied byte
// count for the tool list that dispatches. Where a route serves the request that
// list is the route's emission rather than the catalog the request carries
// (EffectiveToolsJSONBytes), and only the caller knows which route that is: a
// caller that admits many requests over one configured route request measures
// the list once and passes it here.
func ApplyTokenBudgetWithToolBytes(req Request, res registry.Resolved, toolBytes int) (Request, TokenBudget, error) {
	estimate := estimateInputTokensWithToolBytes(targetFromResolved(res, req.Provider, req.Model), req, toolBytes)
	input := max(0, estimate.Tokens, req.InputTokensEstimate, req.FullHistoryInputTokensEstimate)
	safety := tokenSafetyReserve(res.Caps)
	input = saturatingTokenAdd(input, safety)

	requestedOutput := 0
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		requestedOutput = *req.MaxTokens
	} else if positiveLimit(res.Caps.MaxOutputTokens) {
		requestedOutput = *res.Caps.MaxOutputTokens
	}
	admittedOutput := requestedOutput
	if positiveLimit(res.Caps.MaxOutputTokens) && admittedOutput > *res.Caps.MaxOutputTokens {
		admittedOutput = *res.Caps.MaxOutputTokens
	}

	budget := TokenBudget{
		InputTokens:     input,
		SafetyTokens:    safety,
		RequestedOutput: requestedOutput,
		AdmittedOutput:  admittedOutput,
		LimitedOutput:   requestedOutput > 0 && admittedOutput < requestedOutput,
	}

	if positiveLimit(res.Caps.MaxInputTokens) && input > *res.Caps.MaxInputTokens {
		return req, budget, newContextBudgetError(req, res, "max_input", input, requestedOutput, *res.Caps.MaxInputTokens)
	}

	if positiveLimit(res.Caps.ContextWindow) {
		window := *res.Caps.ContextWindow
		if input >= window {
			budget.AdmittedOutput = 0
			budget.LimitedOutput = requestedOutput > 0
			return req, budget, newContextBudgetError(req, res, "context_window", input, requestedOutput, window)
		}
		headroom := window - input
		if admittedOutput <= 0 {
			admittedOutput = headroom
		} else if admittedOutput > headroom {
			admittedOutput = headroom
		}
		budget.AdmittedOutput = admittedOutput
		budget.LimitedOutput = requestedOutput > 0 && admittedOutput < requestedOutput
	}

	if admittedOutput > 0 {
		req.MaxTokens = new(admittedOutput)
	}
	return req, budget, nil
}

// SafetyReserveTokens is the input headroom ApplyTokenBudget reserves for a
// model with the given context window, so a caller sizing a request to fit
// that window can leave the same room.
func SafetyReserveTokens(contextWindow int) int {
	return tokenSafetyReserve(registry.Caps{ContextWindow: &contextWindow})
}

func tokenSafetyReserve(caps registry.Caps) int {
	smallest := caps.InputCeiling()
	if smallest == 0 {
		return minimumTokenSafetyReserve
	}
	percent := smallest / 100
	if smallest%100 != 0 {
		percent++
	}
	return max(minimumTokenSafetyReserve, percent)
}

func positiveLimit(limit *int) bool { return limit != nil && *limit > 0 }

func saturatingTokenAdd(a, b int) int {
	if a > math.MaxInt-b {
		return math.MaxInt
	}
	return a + b
}
