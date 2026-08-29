package responses

import (
	"context"
	"errors"
	"net/http"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/providers/internal/transport"
	"primeradiant.com/evener/llm/registry"
)

func (p *Protocol) call(operation, method, url string, body map[string]any, req llm.Request, res registry.Resolved) *protocolhttp.Call {
	return &protocolhttp.Call{Operation: operation, EndpointFamily: string(EndpointFamily(res)), Method: method, URL: url, Body: body, Req: req, Res: res, Client: p.Client}
}

// Complete implements llm.Protocol. Transports that answer only streams
// (Codex) are driven through Stream and accumulated.
func (p *Protocol) Complete(ctx context.Context, req llm.Request, res registry.Resolved) (llm.Response, error) {
	if protocolhttp.RequiresStreamingComplete(res) {
		return protocolhttp.CompleteViaStream(ctx, res.Instance, func(ctx context.Context) (llm.Stream, error) { return p.Stream(ctx, req, res) })
	}
	body, err := buildBody(req, res, false)
	if err != nil {
		return llm.Response{}, err
	}
	var out llm.Response
	err = protocolhttp.Do(ctx, p.call("responses.create", http.MethodPost, protocolhttp.URL(res, res.Transport.Endpoint), body, req, res), func(r *protocolhttp.Result) (*llm.Response, error) {
		if r.Raw == nil {
			return nil, errors.New("responses.create: response is not a JSON object")
		}
		resp := fromResponses(r.Raw, req.Model)
		p.stampResponseIDHash(&resp)
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
	call := p.call("responses.create(stream)", http.MethodPost, protocolhttp.URL(res, res.Transport.StreamEndpoint), body, req, res)
	return protocolhttp.Stream(ctx, call, func(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, r *protocolhttp.Result, attempt *transport.APIAttemptCapture) {
		p.decodeStream(sctx, cancel, resp, s, req, res, r, attempt)
	})
}

// stampResponseIDHash records the redaction-keyed hash of the response id
// for the session's continuation bookkeeping when a hasher is configured.
func (p *Protocol) stampResponseIDHash(resp *llm.Response) {
	if p.Hasher == nil || resp == nil || resp.ID == "" {
		return
	}
	hash, err := p.Hasher.HashContinuationHandle("response_id", resp.ID)
	if err != nil {
		return
	}
	if resp.Raw == nil {
		resp.Raw = map[string]any{}
	}
	resp.Raw["id_hash"] = hash
}
