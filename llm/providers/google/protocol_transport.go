package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/providers/internal/transport"
	"primeradiant.com/evener/llm/registry"
)

func (p *Protocol) call(operation, family, method, u string, body map[string]any, req llm.Request, res registry.Resolved) *protocolhttp.Call {
	return &protocolhttp.Call{Operation: operation, EndpointFamily: family, Method: method, URL: u, Body: body, Req: req, Res: res, Client: p.Client, Reclassify: reclassifyGemini(res.Instance)}
}

func (p *Protocol) completionCall(operation, family, method, u string, body map[string]any, req llm.Request, res registry.Resolved) *protocolhttp.Call {
	call := p.call(operation, family, method, u, body, req, res)
	call.FinalizeBody = func(finalBody map[string]any) error {
		return reconcileOutputField(finalBody, req.MaxTokens, res.Caps.MaxOutputTokens)
	}
	return call
}

// reclassifyGemini applies the gRPC status remap of classifyGeminiError to
// the runner's classified error (RESOURCE_EXHAUSTED on a 400 is a rate
// limit, DEADLINE_EXCEEDED a timeout, UNAVAILABLE/INTERNAL a server error)
// and keeps the instance name on the remapped error.
func reclassifyGemini(instance string) func(status int, body []byte, err error) error {
	return func(status int, body []byte, err error) error {
		var retryAfter *time.Duration
		if le, ok := errors.AsType[llm.Error](err); ok {
			retryAfter = le.RetryAfter()
		}
		return llm.RewriteErrorProvider(classifyGeminiError(status, body, retryAfter, err), instance)
	}
}

// Complete implements llm.Protocol.
func (p *Protocol) Complete(ctx context.Context, req llm.Request, res registry.Resolved) (llm.Response, error) {
	if protocolhttp.RequiresStreamingComplete(res) {
		return protocolhttp.CompleteViaStream(ctx, res.Instance, func(ctx context.Context) (llm.Stream, error) { return p.Stream(ctx, req, res) })
	}
	body, err := p.BuildBody(req, res)
	if err != nil {
		return llm.Response{}, err
	}
	call := p.completionCall("generateContent", "google_generate_content", http.MethodPost, protocolhttp.URL(res, res.Transport.Endpoint), body, req, res)
	return protocolhttp.Complete(ctx, call, func(raw map[string]any) (llm.Response, error) {
		return fromGeminiResponse(raw, req.Model), nil
	})
}

// Stream implements llm.Protocol.
func (p *Protocol) Stream(ctx context.Context, req llm.Request, res registry.Resolved) (llm.Stream, error) {
	body, err := p.BuildBody(req, res)
	if err != nil {
		return nil, err
	}
	call := p.completionCall("streamGenerateContent", "google_stream_generate_content", http.MethodPost, protocolhttp.URL(res, res.Transport.StreamEndpoint), body, req, res)
	return protocolhttp.Stream(ctx, call, func(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, r *protocolhttp.Result, attempt *transport.APIAttemptCapture) {
		decodeGenerateContentStream(sctx, cancel, resp, s, req, res.Instance, r.EndpointURL, r.Material, attempt)
	})
}

// ListModels implements llm.Protocol. On Vertex it pages through the
// publisher-model listing (listVertexModels); on the Gemini API it reads one
// page of up to 1000 models, keeping those that support generateContent.
func (p *Protocol) ListModels(ctx context.Context, res registry.Resolved) ([]registry.Model, error) {
	if res.Transport.ModelsEndpoint == registry.EndpointUnsupported {
		return nil, llm.ErrModelListingUnsupported
	}
	if isVertexTransport(res) {
		return p.listVertexModels(ctx, res)
	}
	u := protocolhttp.URL(res, res.Transport.ModelsEndpoint)
	if strings.Contains(u, "?") {
		u += "&pageSize=1000"
	} else {
		u += "?pageSize=1000"
	}
	var rows []registry.Model
	err := protocolhttp.Do(ctx, p.call("models.list", "google_models", http.MethodGet, u, nil, llm.Request{Model: "*", AdapterTimeout: llm.ModelListingTimeout(ctx)}, res), func(r *protocolhttp.Result) (*llm.Response, error) {
		var payload struct {
			Models []struct {
				Name                       string   `json:"name"`
				InputTokenLimit            int      `json:"inputTokenLimit"`
				OutputTokenLimit           int      `json:"outputTokenLimit"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
		}
		if err := json.Unmarshal(r.Body, &payload); err != nil {
			return nil, fmt.Errorf("models.list: %w", err)
		}
		for _, m := range payload.Models {
			if !supportsGenerateContent(m.SupportedGenerationMethods) {
				continue
			}
			row := registry.Model{ID: strings.TrimPrefix(m.Name, "models/")}
			if m.InputTokenLimit > 0 {
				row.Caps.MaxInputTokens = new(m.InputTokenLimit)
			}
			if m.OutputTokenLimit > 0 {
				row.Caps.MaxOutputTokens = new(m.OutputTokenLimit)
			}
			rows = append(rows, row)
		}
		return nil, nil
	})
	return rows, err
}

// CountTokens implements llm.Protocol: the completion body wrapped as a
// generateContentRequest with the model name, as the countTokens method
// expects. The runner's prune (protocolhttp.Prepare) only sees the outer
// {"generateContentRequest": ...} body, so inner is pruned here first.
func (p *Protocol) CountTokens(ctx context.Context, req llm.Request, res registry.Resolved) (int, error) {
	if res.Transport.CountTokensEndpoint == registry.EndpointUnsupported {
		return 0, llm.ErrInputTokenCountUnsupported
	}
	inner, err := p.BuildBody(req, res)
	if err != nil {
		return 0, err
	}
	registry.Prune(inner, res.Caps)
	inner["model"] = "models/" + res.WireID
	body := map[string]any{"generateContentRequest": inner}
	var count int
	err = protocolhttp.Do(ctx, p.call("countTokens", "google_count_tokens", http.MethodPost, protocolhttp.URL(res, res.Transport.CountTokensEndpoint), body, req, res), func(r *protocolhttp.Result) (*llm.Response, error) {
		count = tokenCountInt(r.Raw["totalTokens"])
		if count <= 0 && r.Raw["totalTokens"] == nil {
			return nil, fmt.Errorf("countTokens: missing totalTokens in %q", r.Body)
		}
		return nil, nil
	})
	return count, err
}
