package openai

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

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, a.modelsURL(), nil)
	if err != nil {
		return nil, err
	}
	a.setHeaders(httpReq)

	resp, err := a.Client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models: HTTP %d", resp.StatusCode)
	}

	var result struct {
		Data   []openAIModelListEntry `json:"data"`
		Models []codexModelListEntry  `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var models []llm.ModelInfo
	for _, m := range result.Data {
		if skipOpenAIModel(m.ID) {
			continue
		}
		models = append(models, m.modelInfo())
	}
	for _, m := range result.Models {
		info := m.modelInfo()
		if info.ID == "" || skipOpenAIModel(info.ID) {
			continue
		}
		models = append(models, info)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

type openAIModelListEntry struct {
	ID              string `json:"id"`
	OwnedBy         string `json:"owned_by"`
	ContextWindow   int    `json:"context_window"`
	MaxContext      int    `json:"max_context_window"`
	MaxInputTokens  int    `json:"max_input_tokens"`
	InputTokenLimit int    `json:"input_token_limit"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	OutputTokens    int    `json:"output_token_limit"`
}

func (m openAIModelListEntry) modelInfo() llm.ModelInfo {
	info := llm.ModelInfo{
		ID:            m.ID,
		Provider:      "openai",
		DisplayName:   m.ID,
		ContextWindow: firstPositiveInt(m.MaxContext, m.ContextWindow, m.MaxInputTokens, m.InputTokenLimit),
	}
	if maxOut := firstPositiveInt(m.MaxOutputTokens, m.OutputTokens); maxOut > 0 {
		info.MaxOutputTokens = &maxOut
	}
	return info
}

type codexReasoningLevel struct {
	Effort string `json:"effort"`
}

type codexModelListEntry struct {
	Slug                     string                `json:"slug"`
	ID                       string                `json:"id"`
	Model                    string                `json:"model"`
	DisplayName              string                `json:"display_name"`
	ContextWindow            int                   `json:"context_window"`
	MaxContextWindow         int                   `json:"max_context_window"`
	MaxInputTokens           int                   `json:"max_input_tokens"`
	InputTokenLimit          int                   `json:"input_token_limit"`
	MaxOutputTokens          int                   `json:"max_output_tokens"`
	OutputTokenLimit         int                   `json:"output_token_limit"`
	SupportedReasoningLevels []codexReasoningLevel `json:"supported_reasoning_levels"`
	DefaultReasoningLevel    string                `json:"default_reasoning_level"`
	SupportsParallelTools    bool                  `json:"supports_parallel_tool_calls"`
	SupportsImageOriginal    bool                  `json:"supports_image_detail_original"`
	SupportsSearchTool       bool                  `json:"supports_search_tool"`
	WebSearchToolType        string                `json:"web_search_tool_type"`
	InputModalities          []string              `json:"input_modalities"`
	ExperimentalTools        []string              `json:"experimental_supported_tools"`
}

func (m codexModelListEntry) modelInfo() llm.ModelInfo {
	id := firstNonEmpty(m.Slug, m.Model, m.ID)
	info := llm.ModelInfo{
		ID:                    id,
		Provider:              "openai",
		DisplayName:           firstNonEmpty(m.DisplayName, id),
		ContextWindow:         firstPositiveInt(m.MaxContextWindow, m.ContextWindow, m.MaxInputTokens, m.InputTokenLimit),
		SupportsReasoning:     m.DefaultReasoningLevel != "" || len(m.SupportedReasoningLevels) > 0,
		ReasoningEffortLevels: codexReasoningEfforts(m.SupportedReasoningLevels),
		SupportsTools:         m.SupportsParallelTools || len(m.ExperimentalTools) > 0,
		SupportsVision:        codexSupportsVision(m.InputModalities),
	}
	if maxOut := firstPositiveInt(m.MaxOutputTokens, m.OutputTokenLimit); maxOut > 0 {
		info.MaxOutputTokens = &maxOut
	}
	if m.SupportsSearchTool || m.WebSearchToolType != "" || codexHasTool(m.ExperimentalTools, "search") {
		supports := true
		info.SupportsWebSearch = &supports
	}
	return info
}

func codexReasoningEfforts(levels []codexReasoningLevel) []string {
	if len(levels) == 0 {
		return nil
	}
	out := make([]string, 0, len(levels))
	seen := make(map[string]bool, len(levels))
	for _, level := range levels {
		effort := strings.TrimSpace(level.Effort)
		if effort == "" || seen[effort] {
			continue
		}
		seen[effort] = true
		out = append(out, effort)
	}
	return out
}

func codexSupportsVision(modalities []string) bool {
	for _, modality := range modalities {
		switch strings.ToLower(strings.TrimSpace(modality)) {
		case "image", "images", "vision":
			return true
		}
	}
	return false
}

func codexHasTool(tools []string, needle string) bool {
	needle = strings.ToLower(strings.TrimSpace(needle))
	for _, tool := range tools {
		if strings.Contains(strings.ToLower(strings.TrimSpace(tool)), needle) {
			return true
		}
	}
	return false
}

func skipOpenAIModel(id string) bool {
	lower := strings.ToLower(id)
	skip := []string{
		"embedding", "dall-e", "whisper", "davinci", "babbage",
		"tts", "audio", "realtime", "transcribe", "image",
		"moderation", "sora",
	}
	for _, s := range skip {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}
