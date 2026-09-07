package protocolhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/transport"
	"primeradiant.com/evener/llm/registry"
)

// Result is a completed 2xx exchange handed to the protocol's decoder.
type Result struct {
	StatusCode int
	Header     http.Header
	// Body is the full response body of a non-streaming exchange; nil for
	// a stream, whose body the decoder reads live.
	Body []byte
	// Raw is Body decoded as a JSON object, nil when it is not one.
	Raw map[string]any
	// EndpointURL is the final URL after redirects, for llm.StampEndpointURL.
	EndpointURL  string
	Material     llm.APILogCredentialMaterial
	PrunedFields []string
}

// Do performs a non-streaming exchange. finish decodes a 2xx Result into
// the caller's value and returns the *llm.Response (nil for listings and
// token counts) that completes the API-attempt record; it is not called for
// a non-2xx response, which is classified and returned. A finished
// Response carries the instance name as its Provider.
func Do(parentCtx context.Context, c *Call, finish func(r *Result) (*llm.Response, error)) (err error) {
	ctx, cancel := llm.ApplyAdapterTimeout(parentCtx, c.Req.AdapterTimeout, false)
	defer cancel()
	p, err := Prepare(ctx, c)
	if err != nil {
		return err
	}
	var (
		statusCode   int
		responseBody []byte
		decodeErr    error
		transportErr error
		attempt      *transport.APIAttemptCapture
		response     *llm.Response
	)
	defer func() {
		attemptErr := err
		if attemptErr == nil {
			attemptErr = decodeErr
		}
		timeoutSource := llm.APITimeoutSourceForTransport(parentCtx, ctx, transportErr)
		if source := llm.APITimeoutSourceForSSE(decodeErr); source != llm.APITimeoutNone {
			timeoutSource = source
		}
		attempt.Complete(llm.APIAttemptResult{
			StatusCode:   statusCode,
			ResponseBody: responseBody,
			Response:     response,
			Err:          attemptErr,
		}, timeoutSource, decodeErr, transportErr)
	}()
	client := llm.ClientWithAdapterTimeout(c.httpClient(), c.Req.AdapterTimeout)
	resp, att, doErr := transport.DoWithAPIAttempts(parentCtx, client, p.Request, c.metaBuilder(p))
	attempt = att
	if doErr != nil {
		transportErr = doErr
		return llm.WrapContextError(c.Res.Instance, doErr)
	}
	statusCode = resp.StatusCode
	defer func() { _ = resp.Body.Close() }()
	rawBytes, readErr := io.ReadAll(resp.Body)
	responseBody = rawBytes
	var raw map[string]any
	jsonErr := json.Unmarshal(rawBytes, &raw)
	if readErr != nil {
		decodeErr = readErr
	} else {
		decodeErr = jsonErr
	}
	if readErr != nil && (c.Operation == "models.list" || ctx.Err() != nil || errors.Is(readErr, llm.ErrResponseIdleTimeout)) {
		return llm.WrapContextError(c.Res.Instance, readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return c.classify(resp.StatusCode, resp.Header, rawBytes)
	}
	if finish == nil {
		return nil
	}
	r := &Result{StatusCode: resp.StatusCode, Header: resp.Header, Body: rawBytes, Raw: raw, EndpointURL: llm.FinalResponseEndpointURL(resp, c.URL), Material: p.material, PrunedFields: p.PrunedFields}
	response, err = finish(r)
	if err != nil {
		response = nil
		return err
	}
	if response != nil {
		response.Provider = c.Res.Instance
	}
	return nil
}

// Complete runs a non-streaming completion whose 2xx body is a JSON object:
// decode maps that object to the Response, and Complete then stamps the
// endpoint URL and the response's rate-limit headers on it. A body that is
// not a JSON object fails under the call's operation name.
func Complete(ctx context.Context, c *Call, decode func(raw map[string]any) (llm.Response, error)) (llm.Response, error) {
	var out llm.Response
	err := Do(ctx, c, func(r *Result) (*llm.Response, error) {
		if r.Raw == nil {
			return nil, errors.New(c.Operation + ": response is not a JSON object")
		}
		resp, err := decode(r.Raw)
		if err != nil {
			return nil, err
		}
		out = resp
		llm.StampEndpointURL(&out, r.EndpointURL, r.Material)
		out.RateLimit = llm.ParseRateLimitHeaders(r.Header)
		return &out, nil
	})
	if err != nil {
		return llm.Response{}, err
	}
	return out, nil
}

// CompleteViaStream runs a streaming exchange to completion and returns the
// accumulated Response; the Codex backend answers every request as a stream
// (spec §9.5, RequiresStreamingComplete).
func CompleteViaStream(ctx context.Context, instance string, open func(context.Context) (llm.Stream, error)) (llm.Response, error) {
	stream, err := open(ctx)
	if err != nil {
		return llm.Response{}, err
	}
	defer func() { _ = stream.Close() }()
	acc := llm.NewStreamAccumulator()
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventError {
			if ev.Err != nil {
				return llm.Response{}, ev.Err
			}
			return llm.Response{}, fmt.Errorf("%s stream failed", instance)
		}
		acc.Process(ev)
	}
	resp := acc.Response()
	if resp == nil {
		return llm.Response{}, errors.New(instance + " stream completed without final response")
	}
	return *resp, nil
}

// RequiresStreamingComplete reports whether the instance's transport
// answers Complete through Stream (spec §8.1 RequestPreparer).
func RequiresStreamingComplete(res registry.Resolved) bool {
	p, ok := llm.RequestPreparerFor(res.Transport.Auth)
	return ok && p.RequiresStreamingComplete()
}
