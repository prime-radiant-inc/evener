package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"primeradiant.com/serf/llm"
)

func (a *Adapter) CountInputTokens(ctx context.Context, req llm.Request) (llm.InputTokenCount, error) {
	if a.usesCodexBackend() {
		return llm.InputTokenCount{}, llm.ErrInputTokenCountUnsupported
	}
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	body, err := a.buildInputTokenCountBody(req)
	if err != nil {
		return llm.InputTokenCount{}, err
	}
	b, err := json.Marshal(body)
	if err != nil {
		return llm.InputTokenCount{}, err
	}

	ctx, adapterCancel := llm.ApplyAdapterTimeout(ctx, req.AdapterTimeout, false)
	defer adapterCancel()

	endpoint := a.inputTokensURL()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return llm.InputTokenCount{}, err
	}
	a.setHeaders(httpReq)

	client := llm.ClientWithConnectTimeout(a.Client, req.AdapterTimeout)
	resp, err := client.Do(httpReq)
	if err != nil {
		return llm.InputTokenCount{}, llm.WrapContextError("openai", err)
	}
	defer func() { _ = resp.Body.Close() }()

	rawBytes, _ := io.ReadAll(resp.Body)
	var raw map[string]any
	dec := json.NewDecoder(bytes.NewReader(rawBytes))
	dec.UseNumber()
	_ = dec.Decode(&raw)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		ra := llm.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		msg := "responses.input_tokens failed: " + strings.TrimSpace(string(rawBytes))
		return llm.InputTokenCount{}, llm.ErrorFromHTTPStatus("openai", resp.StatusCode, msg, raw, ra)
	}

	return llm.InputTokenCount{
		Tokens:   llm.IntFromAny(raw["input_tokens"]),
		Exact:    true,
		Source:   llm.TokenCountSourceProvider,
		Provider: a.Name(),
		Model:    req.Model,
		Raw:      raw,
	}, nil
}

func (a *Adapter) buildInputTokenCountBody(req llm.Request) (map[string]any, error) {
	body, err := a.buildRequestBody(req)
	if err != nil {
		return nil, err
	}
	stripOpenAIInputTokenCountOutputFields(body)
	return body, nil
}

func stripOpenAIInputTokenCountOutputFields(body map[string]any) {
	for _, key := range []string{
		"background",
		"include",
		"max_output_tokens",
		"max_tool_calls",
		"metadata",
		"prompt_cache_retention",
		"safety_identifier",
		"service_tier",
		"stop",
		"store",
		"temperature",
		"top_p",
	} {
		delete(body, key)
	}
}

func (a *Adapter) inputTokensURL() string {
	return strings.TrimRight(a.responsesURL(), "/") + "/input_tokens"
}
