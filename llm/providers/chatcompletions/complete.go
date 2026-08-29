package chatcompletions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/providers/internal/transport"
	"primeradiant.com/evener/llm/registry"
)

const endpointFamily = "openai_chat_completions"

// call assembles the protocolhttp call: the session-affinity headers are
// the only protocol-fixed headers this protocol adds.
func (p *Protocol) call(operation, method, url string, body map[string]any, req llm.Request, res registry.Resolved) *protocolhttp.Call {
	headers := map[string]string{}
	if registry.BoolValue(res.Caps.SessionAffinityHeaders) {
		if sid := strings.TrimSpace(req.SessionID); sid != "" {
			for _, h := range []string{"session_id", "x-client-request-id", "x-session-affinity"} {
				headers[h] = sid
			}
		}
	}
	return &protocolhttp.Call{Operation: operation, EndpointFamily: endpointFamily, Method: method, URL: url, Body: body, Headers: headers, Req: req, Res: res, Client: p.Client}
}

// Complete implements llm.Protocol.
func (p *Protocol) Complete(ctx context.Context, req llm.Request, res registry.Resolved) (llm.Response, error) {
	if protocolhttp.RequiresStreamingComplete(res) {
		return protocolhttp.CompleteViaStream(ctx, res.Instance, func(ctx context.Context) (llm.Stream, error) { return p.Stream(ctx, req, res) })
	}
	body, err := buildBody(req, res, false)
	if err != nil {
		return llm.Response{}, err
	}
	var out llm.Response
	err = protocolhttp.Do(ctx, p.call("chat.completions", http.MethodPost, protocolhttp.URL(res, res.Transport.Endpoint), body, req, res), func(r *protocolhttp.Result) (*llm.Response, error) {
		if r.Raw == nil {
			return nil, errors.New("chat.completions: response is not a JSON object")
		}
		resp, err := fromChatCompletionResponse(r.Raw, res.Caps.FinishReasonMap)
		if err != nil {
			return nil, err
		}
		llm.StampEndpointURL(&resp, r.EndpointURL, r.Material)
		resp.RateLimit = llm.ParseRateLimitHeaders(r.Header)
		out = resp
		return &out, nil
	})
	if err != nil {
		return llm.Response{}, err
	}
	return out, nil
}

// Stream implements llm.Protocol.
func (p *Protocol) Stream(ctx context.Context, req llm.Request, res registry.Resolved) (llm.Stream, error) {
	body, err := buildBody(req, res, true)
	if err != nil {
		return nil, err
	}
	call := p.call("chat.completions(stream)", http.MethodPost, protocolhttp.URL(res, res.Transport.StreamEndpoint), body, req, res)
	return protocolhttp.Stream(ctx, call, func(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, r *protocolhttp.Result, attempt *transport.APIAttemptCapture) {
		decodeStream(sctx, cancel, resp, s, req, res, r, attempt)
	})
}

// CountTokens implements llm.Protocol: Chat Completions has no counting
// endpoint, so the default "-" reports unsupported and any other value is
// a configuration mistake.
func (*Protocol) CountTokens(_ context.Context, _ llm.Request, res registry.Resolved) (int, error) {
	if res.Transport.CountTokensEndpoint == registry.EndpointUnsupported {
		return 0, llm.ErrInputTokenCountUnsupported
	}
	return 0, &llm.ConfigurationError{Message: fmt.Sprintf("instance %q: openai-chat has no token counting endpoint (count_tokens_endpoint = %q)", res.Instance, res.Transport.CountTokensEndpoint)}
}

// mapFinishReason applies the row's FinishReasonMap before normalization.
func mapFinishReason(m map[string]string, raw string) string {
	if v, ok := m[raw]; ok {
		return v
	}
	return raw
}
