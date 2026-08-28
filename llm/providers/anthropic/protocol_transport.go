package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/providers/internal/requestutil"
	"primeradiant.com/evener/llm/providers/internal/transport"
	"primeradiant.com/evener/llm/registry"
)

func (p *Protocol) call(operation, method, u string, body map[string]any, req llm.Request, res registry.Resolved) *protocolhttp.Call {
	headers := map[string]string{"anthropic-version": anthropicVersion}
	if beta := betaHeader(res, req); beta != "" {
		headers["anthropic-beta"] = beta
	}
	return &protocolhttp.Call{Operation: operation, EndpointFamily: "anthropic_messages", Method: method, URL: u, Body: body, Headers: headers, Req: req, Res: res, Client: p.Client}
}

func (p *Protocol) completionCall(operation, method, u string, body map[string]any, req llm.Request, res registry.Resolved) *protocolhttp.Call {
	call := p.call(operation, method, u, body, req, res)
	call.FinalizeBody = func(finalBody map[string]any) error {
		if !requestutil.WireFieldEnabled(res.Caps, "max_tokens") {
			delete(finalBody, "max_tokens")
			return nil
		}
		return reconcileThinkingContract(finalBody, req, res)
	}
	return call
}

// Complete implements llm.Protocol.
func (p *Protocol) Complete(ctx context.Context, req llm.Request, res registry.Resolved) (llm.Response, error) {
	if protocolhttp.RequiresStreamingComplete(res) {
		return protocolhttp.CompleteViaStream(ctx, res.Instance, func(ctx context.Context) (llm.Stream, error) { return p.Stream(ctx, req, res) })
	}
	body, err := buildProtocolBody(req, res)
	if err != nil {
		return llm.Response{}, err
	}
	call := p.completionCall("messages.create", http.MethodPost, protocolhttp.URL(res, res.Transport.Endpoint), body, req, res)
	return protocolhttp.Complete(ctx, call, func(raw map[string]any) (llm.Response, error) {
		return fromAnthropicResponse(raw, req.Model), nil
	})
}

// Stream implements llm.Protocol.
func (p *Protocol) Stream(ctx context.Context, req llm.Request, res registry.Resolved) (llm.Stream, error) {
	body, err := buildProtocolBody(req, res)
	if err != nil {
		return nil, err
	}
	body["stream"] = true
	call := p.completionCall("messages.create(stream)", http.MethodPost, protocolhttp.URL(res, res.Transport.StreamEndpoint), body, req, res)
	return protocolhttp.Stream(ctx, call, func(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, r *protocolhttp.Result, attempt *transport.APIAttemptCapture) {
		decodeMessagesStream(sctx, cancel, resp, s, req, res.Instance, r.EndpointURL, r.Material, attempt)
	})
}

// ListModels implements llm.Protocol: GET the models endpoint page by page
// (limit=1000, after_id) and return the ids verbatim. The [1m] long-context
// variants are curated alias rows; this listing synthesizes none of its own.
func (p *Protocol) ListModels(ctx context.Context, res registry.Resolved) ([]registry.Model, error) {
	if res.Transport.ModelsEndpoint == registry.EndpointUnsupported {
		return nil, llm.ErrModelListingUnsupported
	}
	var rows []registry.Model
	afterID := ""
	for {
		u, err := withQuery(protocolhttp.URL(res, res.Transport.ModelsEndpoint), "limit", "1000", "after_id", afterID)
		if err != nil {
			return nil, err
		}
		call := p.call("models.list", http.MethodGet, u, nil, llm.Request{Model: "*"}, res)
		call.EndpointFamily = "anthropic_models"
		var page struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := protocolhttp.Do(ctx, call, func(r *protocolhttp.Result) (*llm.Response, error) {
			return nil, json.Unmarshal(r.Body, &page)
		}); err != nil {
			return nil, err
		}
		for _, m := range page.Data {
			if m.ID != "" {
				rows = append(rows, registry.Model{ID: m.ID})
			}
		}
		if !page.HasMore || page.LastID == "" || page.LastID == afterID {
			break
		}
		afterID = page.LastID
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}

// CountTokens implements llm.Protocol: the completion body minus the
// output-side fields, POSTed to count_tokens.
func (p *Protocol) CountTokens(ctx context.Context, req llm.Request, res registry.Resolved) (int, error) {
	if res.Transport.CountTokensEndpoint == registry.EndpointUnsupported {
		return 0, llm.ErrInputTokenCountUnsupported
	}
	body, err := buildProtocolBodyForOperation(req, res, false)
	if err != nil {
		return 0, err
	}
	for _, k := range []string{"max_tokens", "temperature", "top_p", "stop_sequences", "service_tier", "cache_control"} {
		delete(body, k)
	}
	call := p.call("messages.count_tokens", http.MethodPost, protocolhttp.URL(res, res.Transport.CountTokensEndpoint), body, req, res)
	call.EndpointFamily = "anthropic_count_tokens"
	var count int
	err = protocolhttp.Do(ctx, call, func(r *protocolhttp.Result) (*llm.Response, error) {
		n := intFromAny(r.Raw["input_tokens"])
		if n <= 0 && r.Raw["input_tokens"] == nil {
			return nil, fmt.Errorf("messages.count_tokens: missing input_tokens in %q", r.Body)
		}
		count = n
		return nil, nil
	})
	return count, err
}

// withQuery adds key/value pairs (empty values skipped) to a URL that may
// already carry a query string.
func withQuery(raw string, pairs ...string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			q.Set(pairs[i], pairs[i+1])
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
