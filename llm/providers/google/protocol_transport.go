package google

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/providers/internal/requestutil"
	"primeradiant.com/evener/llm/providers/internal/transport"
	"primeradiant.com/evener/llm/registry"
)

func (p *Protocol) call(operation, family, method, u string, body map[string]any, req llm.Request, res registry.Resolved) *protocolhttp.Call {
	return &protocolhttp.Call{Operation: operation, EndpointFamily: family, Method: method, URL: u, Body: body, Req: req, Res: res, Client: p.Client, Reclassify: reclassifyGemini(res)}
}

type protectedSnapshot struct {
	present bool
	raw     []byte
}

var protectedInteractionFields = [...]string{"input", "tools", "response_format"}

func snapshotProtectedFields(body map[string]any) (map[string]protectedSnapshot, error) {
	out := make(map[string]protectedSnapshot, len(protectedInteractionFields))
	for _, k := range protectedInteractionFields {
		v, exists := body[k]
		if !exists {
			out[k] = protectedSnapshot{}
			continue
		}
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		out[k] = protectedSnapshot{present: true, raw: b}
	}
	return out, nil
}

func (p *Protocol) completionCall(
	ctx context.Context,
	operation, family, method, u string,
	body map[string]any,
	policy interactionBodyPolicy,
	req llm.Request,
	res registry.Resolved,
	resolvedSummariesDisabled *bool,
) (*protocolhttp.Call, error) {
	call := p.call(operation, family, method, u, body, req, res)

	var preSnapshots map[string]protectedSnapshot
	auth := llm.AuthenticatorForContext(ctx, res.Transport.Auth)
	if _, hasPreparer := auth.(llm.RequestPreparer); hasPreparer {
		snap, err := snapshotProtectedFields(body)
		if err != nil {
			return nil, err
		}
		preSnapshots = snap
	}

	call.FinalizeBody = func(_ context.Context, finalBody map[string]any) error {
		// 1. stream authority.
		if policy.stream {
			finalBody["stream"] = true
		} else {
			delete(finalBody, "stream")
		}

		// 2. Unsupported execution modes, caching, deferred tier, and retired keys.
		if err := validateInteractionBodyGuards(finalBody, res.Instance, true); err != nil {
			return err
		}

		// 3. Anchor lock.
		if policy.anchorID == "" {
			if _, has := finalBody["previous_interaction_id"]; has {
				return &llm.ConfigurationError{ProviderInstance: res.Instance, Message: "transport constants cannot inject previous_interaction_id after input assembly"}
			}
		} else if s, ok := finalBody["previous_interaction_id"].(string); !ok || s != policy.anchorID {
			return &llm.ConfigurationError{ProviderInstance: res.Instance, Message: "transport constants cannot modify previous_interaction_id after input assembly"}
		}

		// 4. Storage policy lock.
		if policy.storeOmitted {
			if _, has := finalBody["store"]; has {
				return &llm.ConfigurationError{ProviderInstance: res.Instance, Message: "google provider disables sending store; transport constants cannot inject store"}
			}
		} else if b, ok := finalBody["store"].(bool); !ok || b != policy.effectiveStore {
			return &llm.ConfigurationError{ProviderInstance: res.Instance, Message: "transport constants cannot override Interactions store policy"}
		}

		// 5. Unconditional model authority and RequestPreparer snapshot lock.
		if s, ok := finalBody["model"].(string); !ok || s != policy.wireModel {
			return &llm.ConfigurationError{ProviderInstance: res.Instance, Message: "Interactions model field cannot be modified or removed"}
		}
		if preSnapshots != nil {
			postSnapshots, err := snapshotProtectedFields(finalBody)
			if err != nil {
				return err
			}
			for _, k := range protectedInteractionFields {
				pre, post := preSnapshots[k], postSnapshots[k]
				if pre.present != post.present || !bytes.Equal(pre.raw, post.raw) {
					return &llm.ConfigurationError{ProviderInstance: res.Instance, Message: fmt.Sprintf("instance %q: request preparer modified protected Interactions field %q", res.Instance, k)}
				}
			}
		}

		// Validate any Transport.Body labels.* additions.
		if rawLabels, has := finalBody["labels"]; has && rawLabels != nil {
			validated, err := validateAndCloneGoogleLabels(rawLabels)
			if err != nil {
				return err
			}
			finalBody["labels"] = validated
		}

		// 6. Authoritative reasoning=false and generation_config.tool_choice enforcement.
		if res.Caps.ReasoningDisabled() {
			if genCfg, ok := finalBody["generation_config"].(map[string]any); ok {
				delete(genCfg, "thinking_level")
				delete(genCfg, "thinking_summaries")
				if len(genCfg) == 0 {
					delete(finalBody, "generation_config")
				}
			}
		}
		toolChoiceEnabled := requestutil.WireFieldEnabled(res.Caps, "generation_config.tool_choice", res.Protocol)
		if policy.combinedTools {
			if !toolChoiceEnabled {
				return &llm.ConfigurationError{ProviderInstance: res.Instance, Message: "combined google_search and function tools require generation_config.tool_choice enabled"}
			}
			genCfg, _ := finalBody["generation_config"].(map[string]any)
			mode := effectiveToolChoiceMode(nil)
			if genCfg != nil {
				mode = effectiveToolChoiceMode(genCfg["tool_choice"])
			}
			switch mode {
			case "validated", "any", "none":
			default:
				return &llm.ConfigurationError{ProviderInstance: res.Instance, Message: fmt.Sprintf("combined google_search and function tools require generation_config.tool_choice mode in {validated, any, none}, got %q", mode)}
			}
		} else if !toolChoiceEnabled {
			if genCfg, ok := finalBody["generation_config"].(map[string]any); ok {
				delete(genCfg, "tool_choice")
				if len(genCfg) == 0 {
					delete(finalBody, "generation_config")
				}
			}
		}

		// 7. Output-limit pruning and minimum-cap precedence.
		if err := reconcileOutputField(finalBody, req.MaxTokens, res.Caps.MaxOutputTokens, res.Caps, res.Protocol); err != nil {
			return err
		}
		if resolvedSummariesDisabled != nil {
			genCfg, _ := finalBody["generation_config"].(map[string]any)
			*resolvedSummariesDisabled = res.Caps.ReasoningDisabled() || (genCfg != nil && genCfg["thinking_summaries"] == "none")
		}
		return nil
	}
	return call, nil
}

func effectiveToolChoiceMode(v any) string {
	switch x := v.(type) {
	case string:
		return strings.ToLower(strings.TrimSpace(x))
	case map[string]any:
		if allowed, ok := x["allowed_tools"].(map[string]any); ok {
			if s, ok := allowed["mode"].(string); ok {
				return strings.ToLower(strings.TrimSpace(s))
			}
		}
	}
	return ""
}

func reclassifyGemini(res registry.Resolved) func(status int, body []byte, err error) error {
	return func(status int, body []byte, err error) error {
		var retryAfter *time.Duration
		if le, ok := errors.AsType[llm.Error](err); ok {
			retryAfter = le.RetryAfter()
		}
		out := llm.RewriteErrorProvider(classifyGeminiError(status, body, retryAfter, err), res.Instance)
		if remedy, ok := regionalVertexLocationRemedy(res, status); ok {
			return &llm.ConfigurationError{
				ProviderInstance: res.Instance,
				Message:          fmt.Sprintf("%s; provider said: %s", remedy, out.Error()),
				Cause:            out,
			}
		}
		return out
	}
}

func regionalVertexLocationRemedy(res registry.Resolved, status int) (string, bool) {
	loc, derived := registry.VertexLocationDerived(res.Transport, res.HostDerivedByRule)
	if !derived || status != http.StatusNotFound {
		return "", false
	}
	if loc == "global" || loc == "us" || loc == "eu" {
		return "", false
	}
	return vertexInteractionsLocationRemedy(res.Instance, loc, res.WireID), true
}

func vertexInteractionsLocationRemedy(instance, loc, wireID string) string {
	lower := strings.ToLower(strings.TrimSpace(loc))
	const (
		usDesc     = "GOOGLE_VERTEX_LOCATION=us (United States multi-region data residency)"
		euDesc     = "GOOGLE_VERTEX_LOCATION=eu (European Union multi-region data residency)"
		globalDesc = "GOOGLE_VERTEX_LOCATION=global (global routing with no data-residency guarantee)"
	)
	const disclosure = "switching from %q changes the geographic data-residency boundary"
	const directionalFmt = "instance %q: Vertex location %q does not serve /interactions for %q; use %s (or %s / %s; " + disclosure + ")"
	switch {
	case strings.HasPrefix(lower, "us-"):
		return fmt.Sprintf(directionalFmt,
			instance, loc, wireID, usDesc, euDesc, globalDesc, loc)
	case strings.HasPrefix(lower, "europe-") && lower != "europe-west2" && lower != "europe-west6":
		return fmt.Sprintf(directionalFmt,
			instance, loc, wireID, euDesc, usDesc, globalDesc, loc)
	default:
		return fmt.Sprintf("instance %q: Vertex location %q does not serve /interactions for %q; supported endpoints are %s, %s, or %s ("+disclosure+")",
			instance, loc, wireID, usDesc, euDesc, globalDesc, loc)
	}
}

func (p interactionBodyPolicy) allowsPre2xxSignatureRetry() bool {
	return p.anchorID == "" && p.hasUnsignedNonNativeCalls
}

func (p interactionBodyPolicy) allowsPost2xxSignatureRetry() bool {
	return p.allowsPre2xxSignatureRetry() && !p.storeOmitted && !p.effectiveStore
}

func isSignatureValidation400(err error) bool {
	if err == nil {
		return false
	}
	le, ok := errors.AsType[llm.Error](err)
	if !ok || le.StatusCode() != http.StatusBadRequest {
		return false
	}
	lower := strings.ToLower(le.Error())
	return strings.Contains(lower, "thought_signature") || strings.Contains(lower, "thought signature") || strings.Contains(lower, "signature")
}

// Complete implements llm.Protocol.
func (p *Protocol) Complete(ctx context.Context, req llm.Request, res registry.Resolved) (llm.Response, error) {
	if protocolhttp.RequiresStreamingCompleteContext(ctx, res) {
		return protocolhttp.CompleteViaStream(ctx, res.Instance, func(ctx context.Context) (llm.Stream, error) { return p.Stream(ctx, req, res) })
	}
	resp, policy, settledFrom2xx, err := p.completeOnce(ctx, req, res, false)
	if err != nil && isSignatureValidation400(err) &&
		((!settledFrom2xx && policy.allowsPre2xxSignatureRetry()) || (settledFrom2xx && policy.allowsPost2xxSignatureRetry())) {
		retryResp, _, _, retryErr := p.completeOnce(ctx, req, res, true)
		return retryResp, retryErr
	}
	return resp, err
}

func (p *Protocol) completeOnce(ctx context.Context, req llm.Request, res registry.Resolved, fallbackUnsignedCalls bool) (llm.Response, interactionBodyPolicy, bool, error) {
	body, policy, err := buildBodyWithPolicy(req, res, false, fallbackUnsignedCalls)
	if err != nil {
		return llm.Response{}, policy, false, llm.RewriteErrorProvider(err, res.Instance)
	}
	call, err := p.completionCall(ctx, "interactions.create", "google_interactions", http.MethodPost, protocolhttp.URL(res, res.Transport.Endpoint), body, policy, req, res, nil)
	if err != nil {
		return llm.Response{}, policy, false, llm.RewriteErrorProvider(err, res.Instance)
	}
	settledFrom2xx := false
	resp, err := protocolhttp.Complete(ctx, call, func(r *protocolhttp.Result) (llm.Response, error) {
		settledFrom2xx = true
		decoded, derr := fromInteraction(r.Raw, req.Model, r.Body, res.Instance, res.WireID)
		if derr != nil {
			return llm.Response{}, llm.RewriteErrorProvider(derr, res.Instance)
		}
		llm.StampContinuationIDHash(ctx, &decoded, p.Hasher)
		return decoded, nil
	})
	return resp, policy, settledFrom2xx, err
}

// Stream implements llm.Protocol.
func (p *Protocol) Stream(ctx context.Context, req llm.Request, res registry.Resolved) (llm.Stream, error) {
	body, policy, err := buildBodyWithPolicy(req, res, true, false)
	if err != nil {
		return nil, llm.RewriteErrorProvider(err, res.Instance)
	}
	if !policy.allowsPre2xxSignatureRetry() {
		return p.openStreamWithBody(ctx, req, res, body, policy)
	}
	if !policy.allowsPost2xxSignatureRetry() {
		s1, err := p.openStreamWithBody(ctx, req, res, body, policy)
		if err != nil {
			if isSignatureValidation400(err) {
				return p.openStream(ctx, req, res, true)
			}
			return nil, err
		}
		return s1, nil
	}

	cancelCtx, cancelAll := context.WithCancel(ctx)
	s1, err := p.openStreamWithBody(cancelCtx, req, res, body, policy)
	if err != nil {
		cancelAll()
		if isSignatureValidation400(err) {
			return p.openStream(ctx, req, res, true)
		}
		return nil, err
	}

	var consumerClosed atomic.Bool
	outer := llm.NewChanStream(func() {
		consumerClosed.Store(true)
		cancelAll()
	})
	go func() {
		defer func() {
			cancelAll()
			outer.CloseSend()
		}()
		emittedStart := false
		sawOutput := false
		emitContextErrorIfActive := func(st llm.Stream) {
			if consumerClosed.Load() {
				return
			}
			err := ctx.Err()
			if err == nil {
				return
			}
			if st != nil {
				for {
					select {
					case ev, ok := <-st.Events():
						if !ok {
							outer.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.WrapContextError(res.Instance, err)})
							return
						}
						if ev.Type == llm.StreamEventError || ev.Type == llm.StreamEventFinish {
							outer.Send(ev)
							return
						}
					default:
						outer.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.WrapContextError(res.Instance, err)})
						return
					}
				}
			}
			outer.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.WrapContextError(res.Instance, err)})
		}
		relay := func(st llm.Stream, allowRetry bool) bool {
			defer func() { _ = st.Close() }()
			for {
				var (
					ev llm.StreamEvent
					ok bool
				)
				select {
				case <-cancelCtx.Done():
					emitContextErrorIfActive(st)
					return false
				case ev, ok = <-st.Events():
					if !ok {
						emitContextErrorIfActive(nil)
						return false
					}
				}
				switch ev.Type {
				case llm.StreamEventStreamStart:
					if !emittedStart {
						emittedStart = true
						outer.Send(ev)
					}
				case llm.StreamEventTextStart, llm.StreamEventTextDelta, llm.StreamEventTextEnd,
					llm.StreamEventReasoningStart, llm.StreamEventReasoningDelta, llm.StreamEventReasoningEnd,
					llm.StreamEventToolCallStart, llm.StreamEventToolCallDelta, llm.StreamEventToolCallEnd:
					sawOutput = true
					outer.Send(ev)
				case llm.StreamEventFinish:
					outer.Send(ev)
					return false
				case llm.StreamEventError:
					if allowRetry && !sawOutput && isSignatureValidation400(ev.Err) {
						return true
					}
					outer.Send(ev)
					return false
				default:
					outer.Send(ev)
				}
			}
		}
		if relay(s1, true) {
			s2, err2 := p.openStream(cancelCtx, req, res, true)
			if err2 != nil {
				if !consumerClosed.Load() {
					outer.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.RewriteErrorProvider(err2, res.Instance)})
				}
				return
			}
			relay(s2, false)
		}
	}()
	return outer, nil
}

func (p *Protocol) openStream(ctx context.Context, req llm.Request, res registry.Resolved, fallbackUnsignedCalls bool) (llm.Stream, error) {
	body, policy, err := buildBodyWithPolicy(req, res, true, fallbackUnsignedCalls)
	if err != nil {
		return nil, llm.RewriteErrorProvider(err, res.Instance)
	}
	return p.openStreamWithBody(ctx, req, res, body, policy)
}

func (p *Protocol) openStreamWithBody(ctx context.Context, req llm.Request, res registry.Resolved, body map[string]any, policy interactionBodyPolicy) (llm.Stream, error) {
	summariesDisabled := policy.summariesDisabled
	call, err := p.completionCall(ctx, "interactions.create(stream)", "google_interactions_stream", http.MethodPost, protocolhttp.URL(res, res.Transport.StreamEndpoint), body, policy, req, res, &summariesDisabled)
	if err != nil {
		return nil, llm.RewriteErrorProvider(err, res.Instance)
	}
	return protocolhttp.Stream(ctx, call, func(sctx context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, r *protocolhttp.Result, attempt *transport.APIAttemptCapture) {
		decodeInteractionStream(sctx, cancel, resp, s, req, res.Instance, res.WireID, r.EndpointURL, r.Material, attempt, summariesDisabled, p.Hasher)
	})
}

// ListModels implements llm.Protocol.
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

// CountTokens implements llm.Protocol: Interactions defines no countTokens endpoint.
func (p *Protocol) CountTokens(_ context.Context, _ llm.Request, res registry.Resolved) (int, error) {
	return 0, llm.RewriteErrorProvider(llm.ErrInputTokenCountUnsupported, res.Instance)
}
