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
func ApplyTokenBudget(req Request, res registry.Resolved) (Request, TokenBudget, error) {
	input := max(0, EstimateInputTokens(req).Tokens, req.InputTokensEstimate, req.FullHistoryInputTokensEstimate)
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

func tokenSafetyReserve(caps registry.Caps) int {
	smallest := 0
	if positiveLimit(caps.MaxInputTokens) {
		smallest = *caps.MaxInputTokens
	}
	if positiveLimit(caps.ContextWindow) && (smallest == 0 || *caps.ContextWindow < smallest) {
		smallest = *caps.ContextWindow
	}
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
