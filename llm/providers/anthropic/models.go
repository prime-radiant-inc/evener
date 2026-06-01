package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"primeradiant.com/serf/llm"
)

func (a *Adapter) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 0}
	}

	var models []llm.ModelInfo
	var afterID string
	for {
		u := a.BaseURL + "/v1/models?limit=1000"
		if afterID != "" {
			u += "&after_id=" + afterID
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		for k, v := range a.DefaultHeaders {
			httpReq.Header.Set(k, v)
		}
		httpReq.Header.Set("x-api-key", a.APIKey)
		httpReq.Header.Set("anthropic-version", "2023-06-01")

		resp, err := a.Client.Do(httpReq)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("list models: HTTP %d", resp.StatusCode)
		}

		var page struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}

		for _, m := range page.Data {
			displayName := m.DisplayName
			if displayName == "" {
				displayName = m.ID
			}
			models = append(models, llm.ModelInfo{
				ID:          m.ID,
				Provider:    "anthropic",
				DisplayName: displayName,
			})
		}

		if !page.HasMore || page.LastID == "" {
			break
		}
		afterID = page.LastID
	}

	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })

	// Generate synthetic [1m] variants for models that support 1M context.
	// Eligible: claude-opus-4- and claude-sonnet-4- prefixes (not haiku).
	// Inherit pricing and other metadata from the base model so the model
	// picker shows complete info for the variants too.
	eligible1M := []string{"claude-opus-4-", "claude-sonnet-4-"}
	var extras []llm.ModelInfo
	for _, m := range models {
		for _, prefix := range eligible1M {
			if strings.HasPrefix(m.ID, prefix) {
				variant := m
				variant.ID = m.ID + "[1m]"
				variant.DisplayName = m.DisplayName + " (1M context)"
				variant.ContextWindow = 1_000_000
				extras = append(extras, variant)
				break
			}
		}
	}
	models = append(models, extras...)
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })

	return models, nil
}
