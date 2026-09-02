package responses

import (
	"context"
	"net/http"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/providers/internal/transport"
	"primeradiant.com/evener/llm/registry"
)

func (p *Protocol) call(operation, method, url string, body map[string]any, req llm.Request, res registry.Resolved) *protocolhttp.Call {
	return &protocolhttp.Call{Operation: operation, EndpointFamily: string(llm.ResponsesEndpointFamilyFor(res)), Method: method, URL: url, Body: body, Req: req, Res: res, Client: p.Client}
}

func (p *Protocol) completionCall(operation, method, url string, body map[string]any, req llm.Request, res registry.Resolved) *protocolhttp.Call {
	call := p.call(operation, method, url, body, req, res)
	call.FinalizeBody = func(finalBody map[string]any) {
		reconcilePreparedOutput(finalBody, req, res.Caps)
	}
	return call
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
	call := p.completionCall("responses.create", http.MethodPost, protocolhttp.URL(res, res.Transport.Endpoint), body, req, res)
	return protocolhttp.Complete(ctx, call, func(raw map[string]any) (llm.Response, error) {
		resp := fromResponses(raw, req.Model)
		p.stampResponseIDHash(ctx, &resp)
		return resp, nil
	})
}

// Stream implements llm.Protocol.
func (p *Protocol) Stream(ctx context.Context, req llm.Request, res registry.Resolved) (llm.Stream, error) {
	body, err := buildBody(req, res, true)
	if err != nil {
		return nil, err
	}
	call := p.completionCall("responses.create(stream)", http.MethodPost, protocolhttp.URL(res, res.Transport.StreamEndpoint), body, req, res)
	return protocolhttp.Stream(ctx, call, func(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, r *protocolhttp.Result, attempt *transport.APIAttemptCapture) {
		p.decodeStream(sctx, cancel, resp, s, req, res, r, attempt)
	})
}

// stampResponseIDHash records the redaction-keyed hash of the response id
// for the session's continuation bookkeeping when a hasher is available.
// The dispatching client attaches its own hasher to the context, which the
// process-wide p.Hasher only stands in for.
func (p *Protocol) stampResponseIDHash(ctx context.Context, resp *llm.Response) {
	hasher := llm.ContinuationHasherFromContext(ctx)
	if hasher == nil {
		hasher = p.Hasher
	}
	if hasher == nil || resp == nil || resp.ID == "" {
		return
	}
	hash, err := hasher.HashContinuationHandle("response_id", resp.ID)
	if err != nil {
		return
	}
	if resp.Raw == nil {
		resp.Raw = map[string]any{}
	}
	resp.Raw["id_hash"] = hash
}
