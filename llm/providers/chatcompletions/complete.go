package chatcompletions

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/providers/internal/transport"
	"primeradiant.com/evener/llm/registry"
)

const endpointFamily = "openai_chat_completions"

// completionCall adds Chat's session-affinity headers to the shared HTTP call.
func (p *Protocol) completionCall(operation, url string, req llm.Request, res registry.Resolved, stream bool) *protocolhttp.Call {
	headers := map[string]string{}
	if registry.BoolValue(res.Caps.SessionAffinityHeaders) {
		if sid := strings.TrimSpace(req.SessionID); sid != "" {
			for _, h := range []string{"session_id", "x-client-request-id", "x-session-affinity"} {
				headers[h] = sid
			}
		}
	}
	call := &protocolhttp.Call{Operation: operation, EndpointFamily: endpointFamily, Method: http.MethodPost, URL: url, Headers: headers, Req: req, Res: res, Client: p.Client}
	call.BuildBody = func(view llm.Request) (map[string]any, error) { return buildBody(view, res, stream) }
	call.FinalizeBody = func(_ *http.Request, finalBody map[string]any, view llm.Request) error {
		reconcilePreparedOutput(finalBody, view, res)
		return nil
	}
	return call
}

// Complete implements llm.Protocol.
func (p *Protocol) Complete(ctx context.Context, req llm.Request, res registry.Resolved) (llm.Response, error) {
	if protocolhttp.RequiresStreamingComplete(res) {
		return protocolhttp.CompleteViaStream(ctx, res.Instance, func(ctx context.Context) (llm.Stream, error) { return p.Stream(ctx, req, res) })
	}
	return runImageRecovery(ctx, p, req, res, false, func(call *protocolhttp.Call, recovery *imageRecovery) (llm.Response, error) {
		return protocolhttp.Complete(ctx, call, func(r *protocolhttp.Result) (llm.Response, error) {
			resp, err := fromChatCompletionResponse(r.Raw, res.Caps.FinishReasonMap, r.Body)
			resp.Warnings = append(resp.Warnings, recovery.warnings...)
			return resp, err
		})
	})
}

// Stream implements llm.Protocol.
func (p *Protocol) Stream(ctx context.Context, req llm.Request, res registry.Resolved) (llm.Stream, error) {
	return runImageRecovery(ctx, p, req, res, true, func(call *protocolhttp.Call, recovery *imageRecovery) (llm.Stream, error) {
		return protocolhttp.Stream(ctx, call, func(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, r *protocolhttp.Result, attempt *transport.APIAttemptCapture) {
			decodeStream(sctx, cancel, resp, s, req, res, r, attempt, recovery.warnings)
		})
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
