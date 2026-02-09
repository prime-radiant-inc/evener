package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// ModelInfo is the normalized model metadata entry, primarily sourced from the LiteLLM catalog
// in Kilroy. This is metadata-only and is not used as a provider call path.
type ModelInfo struct {
	ID              string   `json:"id"`
	Provider        string   `json:"provider"`
	DisplayName     string   `json:"display_name"`
	ContextWindow   int      `json:"context_window"`
	MaxOutputTokens *int     `json:"max_output_tokens,omitempty"`
	SupportsTools   bool     `json:"supports_tools"`
	SupportsVision  bool     `json:"supports_vision"`
	SupportsReasoning bool   `json:"supports_reasoning"`
	InputCostPerMillion  *float64 `json:"input_cost_per_million,omitempty"`
	OutputCostPerMillion *float64 `json:"output_cost_per_million,omitempty"`
	Aliases         []string `json:"aliases,omitempty"`
}

type ModelCatalog struct {
	Models []ModelInfo
	byID   map[string]ModelInfo
}

func (c *ModelCatalog) GetModelInfo(modelID string) *ModelInfo {
	if c == nil {
		return nil
	}
	if c.byID == nil {
		c.buildIndex()
	}
	if mi, ok := c.byID[strings.TrimSpace(modelID)]; ok {
		out := mi
		return &out
	}
	return nil
}

func (c *ModelCatalog) ListModels(provider string) []ModelInfo {
	if c == nil {
		return nil
	}
	p := strings.ToLower(strings.TrimSpace(provider))
	if p == "gemini" {
		p = "google"
	}
	if p == "" {
		return append([]ModelInfo{}, c.Models...)
	}
	var out []ModelInfo
	for _, m := range c.Models {
		if strings.ToLower(m.Provider) == p {
			out = append(out, m)
		}
	}
	return out
}

func (c *ModelCatalog) GetLatestModel(provider string, capability string) *ModelInfo {
	models := c.ListModels(provider)
	capability = strings.ToLower(strings.TrimSpace(capability))

	filtered := models[:0]
	for _, m := range models {
		switch capability {
		case "":
			filtered = append(filtered, m)
		case "tools":
			if m.SupportsTools {
				filtered = append(filtered, m)
			}
		case "vision":
			if m.SupportsVision {
				filtered = append(filtered, m)
			}
		case "reasoning":
			if m.SupportsReasoning {
				filtered = append(filtered, m)
			}
		default:
			// Unknown capability filter => no results.
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].ContextWindow != filtered[j].ContextWindow {
			return filtered[i].ContextWindow > filtered[j].ContextWindow
		}
		// Stable tie-break: lexical ID descending.
		return filtered[i].ID > filtered[j].ID
	})
	out := filtered[0]
	return &out
}

func (c *ModelCatalog) buildIndex() {
	by := make(map[string]ModelInfo, len(c.Models))
	for _, m := range c.Models {
		if _, exists := by[m.ID]; exists {
			// Leave the first entry to avoid silently changing behavior on duplicates.
			continue
		}
		by[m.ID] = m
	}
	c.byID = by
}

// LoadModelCatalogFromLiteLLMJSON loads model metadata from a LiteLLM model catalog JSON file.
// The LiteLLM catalog is metadata-only (pricing + context windows + capability flags).
func LoadModelCatalogFromLiteLLMJSON(path string) (*ModelCatalog, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var raw map[string]map[string]any
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("litellm catalog is empty: %s", path)
	}

	var models []ModelInfo
	for id, v := range raw {
		// Skip the upstream schema example.
		if id == "sample_spec" {
			continue
		}
		mode, _ := v["mode"].(string)
		if strings.TrimSpace(mode) != "" && strings.TrimSpace(mode) != "chat" {
			continue
		}

		prov := normalizeCatalogProvider(fmt.Sprint(v["litellm_provider"]))
		ctxWindow := parseInt(v["max_input_tokens"])
		if ctxWindow == 0 {
			ctxWindow = parseInt(v["max_tokens"])
		}
		maxOut := parseInt(v["max_output_tokens"])
		if maxOut == 0 {
			maxOut = parseInt(v["max_tokens"])
		}
		var maxOutPtr *int
		if maxOut > 0 {
			maxOutPtr = &maxOut
		}

		inCost := parseFloatPtr(v["input_cost_per_token"])
		outCost := parseFloatPtr(v["output_cost_per_token"])
		inPerM := scalePerMillion(inCost)
		outPerM := scalePerMillion(outCost)

		models = append(models, ModelInfo{
			ID:                id,
			Provider:          prov,
			DisplayName:       id,
			ContextWindow:     ctxWindow,
			MaxOutputTokens:   maxOutPtr,
			SupportsTools:     parseBool(v["supports_function_calling"]),
			SupportsVision:    parseBool(v["supports_vision"]),
			SupportsReasoning: parseBool(v["supports_reasoning"]),
			InputCostPerMillion:  inPerM,
			OutputCostPerMillion: outPerM,
			Aliases:           nil,
		})
	}

	// Stable ordering.
	sort.Slice(models, func(i, j int) bool {
		if models[i].Provider != models[j].Provider {
			return models[i].Provider < models[j].Provider
		}
		return models[i].ID < models[j].ID
	})
	return &ModelCatalog{Models: models}, nil
}

func normalizeCatalogProvider(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	switch p {
	case "gemini", "google_ai_studio", "google":
		return "google"
	default:
		return p
	}
}

func parseInt(v any) int {
	switch x := v.(type) {
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	case float64:
		return int(x)
	case int:
		return x
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	default:
		return 0
	}
}

func parseBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		b, _ := strconv.ParseBool(strings.TrimSpace(x))
		return b
	default:
		return false
	}
}

func parseFloatPtr(v any) *float64 {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return nil
		}
		return &f
	case float64:
		return &x
	case int:
		f := float64(x)
		return &f
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return nil
		}
		return &f
	default:
		return nil
	}
}

func scalePerMillion(perToken *float64) *float64 {
	if perToken == nil {
		return nil
	}
	v := *perToken * 1_000_000
	return &v
}

