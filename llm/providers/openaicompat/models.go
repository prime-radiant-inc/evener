package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/internal/transport"
)

// ListModels fetches available models from the /v1/models endpoint.
func (a *Adapter) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, a.BaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	a.setConfiguredHeaders(httpReq)
	if a.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.APIKey)
	}

	resp, attempt, err := transport.DoWithAPIAttempts(ctx, a.Client, httpReq, func(wireRequest *http.Request, requestBody []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{
			ProviderInstance:   a.apiLogProviderInstance(),
			RequestModel:       "*",
			EndpointFamily:     "openai_compatible_models",
			RequestBody:        requestBody,
			CredentialMaterial: a.apiLogCredentialMaterial(wireRequest),
		}
	})
	if err != nil {
		attempt.Complete(llm.APIAttemptResult{Err: err}, llm.APITimeoutSourceForTransport(ctx, ctx, err), nil, err)
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		returnedErr := fmt.Errorf("list models: HTTP %d", resp.StatusCode)
		attempt.Complete(llm.APIAttemptResult{StatusCode: resp.StatusCode, ResponseBody: body, Err: returnedErr}, llm.APITimeoutNone, nil, nil)
		return nil, returnedErr
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
			// context_length is reported by Kimi (kimi-for-coding = 262144) and
			// OpenRouter; absent on stock OpenAI. When present it flows into the
			// profile via WithLiveModelInfo so context management uses the model's
			// real window instead of the 128K default.
			ContextLength int `json:"context_length"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		attempt.Complete(llm.APIAttemptResult{StatusCode: resp.StatusCode, Err: err}, llm.APITimeoutNone, err, nil)
		return nil, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	attempt.Complete(llm.APIAttemptResult{StatusCode: resp.StatusCode}, llm.APITimeoutNone, nil, nil)

	models := make([]llm.ModelInfo, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, llm.ModelInfo{
			ID:            m.ID,
			Provider:      "openai-compatible",
			DisplayName:   m.ID,
			ContextWindow: m.ContextLength,
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
