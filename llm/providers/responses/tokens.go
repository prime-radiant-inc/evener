package responses

import (
	"context"
	"fmt"
	"net/http"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/registry"
)

// inputTokenCountOutputFields are the fields /responses/input_tokens
// rejects: everything that shapes the output rather than the input.
var inputTokenCountOutputFields = []string{
	"background", "include", "max_output_tokens", "max_tool_calls", "metadata", "prompt_cache_retention",
	"safety_identifier", "service_tier", "store", "temperature", "top_p",
}

// CountTokens implements llm.Protocol: POST the completion body, minus the
// output fields, to the count-tokens endpoint (spec §8.1).
func (p *Protocol) CountTokens(ctx context.Context, req llm.Request, res registry.Resolved) (int, error) {
	if res.Transport.CountTokensEndpoint == registry.EndpointUnsupported {
		return 0, llm.ErrInputTokenCountUnsupported
	}
	body, err := buildBody(req, res, false)
	if err != nil {
		return 0, err
	}
	for _, f := range inputTokenCountOutputFields {
		delete(body, f)
	}
	var count int
	err = protocolhttp.Do(ctx, p.call("responses.input_tokens", http.MethodPost, protocolhttp.URL(res, res.Transport.CountTokensEndpoint), body, req, res), func(r *protocolhttp.Result) (*llm.Response, error) {
		n, ok := r.Raw["input_tokens"].(float64)
		if !ok {
			return nil, fmt.Errorf("responses.input_tokens: missing input_tokens in %q", r.Body)
		}
		count = int(n)
		return nil, nil
	})
	return count, err
}
