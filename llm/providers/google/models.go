package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/internal/transport"
)

func (a *Adapter) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	u, err := url.Parse(a.BaseURL + "/v1beta/models")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("key", a.APIKey)
	q.Set("pageSize", "1000")
	u.RawQuery = q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	a.setConfiguredHeaders(httpReq)

	resp, attempt, err := transport.DoWithAPIAttempts(ctx, a.Client, httpReq, func(wireRequest *http.Request, requestBody []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{
			ProviderInstance:   a.Name(),
			RequestModel:       "*",
			EndpointFamily:     "google_models",
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
		Models []struct {
			Name                       string   `json:"name"`
			DisplayName                string   `json:"displayName"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&result)
	_, readErr := io.Copy(io.Discard, resp.Body)
	responseErr := decodeErr
	if readErr != nil {
		responseErr = errors.Join(responseErr, readErr)
	}
	if responseErr != nil {
		attempt.Complete(llm.APIAttemptResult{StatusCode: resp.StatusCode, Err: responseErr}, llm.APITimeoutNone, responseErr, nil)
		return nil, responseErr
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: resp.StatusCode}, llm.APITimeoutNone, nil, nil)

	var models []llm.ModelInfo
	for _, m := range result.Models {
		if !supportsGenerateContent(m.SupportedGenerationMethods) {
			continue
		}
		id := strings.TrimPrefix(m.Name, "models/")
		displayName := m.DisplayName
		if displayName == "" {
			displayName = id
		}
		models = append(models, llm.ModelInfo{
			ID:          id,
			Provider:    "google",
			DisplayName: displayName,
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

func supportsGenerateContent(methods []string) bool {
	for _, m := range methods {
		if m == "generateContent" {
			return true
		}
	}
	return false
}
