package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// mdProvider and mdModel mirror the parts of models.dev's api.json the
// converter reads (spec §6.1). Unknown fields are ignored.
type mdProvider struct {
	ID     string             `json:"id"`
	Name   string             `json:"name"`
	Doc    string             `json:"doc"`
	Env    []string           `json:"env"`
	NPM    string             `json:"npm"`
	API    string             `json:"api"`
	Models map[string]mdModel `json:"models"`
}

type mdModel struct {
	ID               string           `json:"id"`
	Name             string           `json:"name"`
	Family           string           `json:"family"`
	Reasoning        bool             `json:"reasoning"`
	ReasoningOptions []mdReasoningOpt `json:"reasoning_options"`
	ToolCall         bool             `json:"tool_call"`
	StructuredOutput *bool            `json:"structured_output"`
	Temperature      *bool            `json:"temperature"`
	Knowledge        string           `json:"knowledge"`
	Status           string           `json:"status"`
	Modalities       mdModalities     `json:"modalities"`
	Limit            mdLimit          `json:"limit"`
	Cost             *mdCost          `json:"cost"`
	Interleaved      json.RawMessage  `json:"interleaved"`
	Provider         *mdModelProvider `json:"provider"`
}

type mdReasoningOpt struct {
	Type   string   `json:"type"`
	Values []string `json:"values"`
}

type mdModalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

type mdLimit struct {
	Context int `json:"context"`
	Input   int `json:"input"`
	Output  int `json:"output"`
}

type mdCost struct {
	Input      float64      `json:"input"`
	Output     float64      `json:"output"`
	CacheRead  float64      `json:"cache_read"`
	CacheWrite float64      `json:"cache_write"`
	Tiers      []mdCostTier `json:"tiers"`
}

type mdCostTier struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
	Tier       struct {
		Type string `json:"type"`
		Size int    `json:"size"`
	} `json:"tier"`
}

type mdModelProvider struct {
	NPM   string `json:"npm"`
	API   string `json:"api"`
	Shape string `json:"shape"`
}

// Transport preset names the converter attaches to cross-protocol rows; the
// curated overlay defines them (spec §4.3, §6.2).
const (
	PresetVertexAnthropic     = "vertex-anthropic"
	PresetVertexGemini        = "vertex-gemini"
	PresetBedrockMantleOpenAI = "bedrock-mantle-openai"
)

// regionPrefixes are Bedrock's cross-Region inference-profile prefixes (spec §7.2).
var regionPrefixes = []string{"us.", "eu.", "apac.", "au.", "jp.", "global."}

// stripRegionPrefix removes one Bedrock region prefix from id.
func stripRegionPrefix(id string) string {
	for _, p := range regionPrefixes {
		if strings.HasPrefix(id, p) {
			return id[len(p):]
		}
	}
	return id
}

// hiddenNPM lists SDKs evener has no protocol for (spec §6.1).
var hiddenNPM = map[string]bool{
	"@ai-sdk/cohere": true, "watsonx-ai-provider": true, "@jerome-benoit/sap-ai-provider-v2": true,
	"@qvac/ai-sdk-provider": true, "@saladtechnologies-oss/ai-sdk-provider": true,
	"merge-gateway-ai-sdk-provider": true, "ai-gateway-provider": true, "@aihubmix/ai-sdk-provider": true,
	"gitlab-ai-provider": true, "venice-ai-sdk-provider": true,
}

// npmProtocol is the spec §6.1 npm → protocol/auth table. known is false for
// the "anything else" branch, which callers record as a warning.
func npmProtocol(npm string) (protocol, auth string, hidden, known bool) {
	if hiddenNPM[npm] {
		return "", "", true, true
	}
	switch npm {
	case "@ai-sdk/openai-compatible", "@ai-sdk/groq", "@ai-sdk/cerebras", "@ai-sdk/togetherai",
		"@ai-sdk/deepinfra", "@ai-sdk/perplexity", "@ai-sdk/mistral", "@openrouter/ai-sdk-provider",
		"@ai-sdk/gateway", "@ai-sdk/vercel":
		return ProtocolOpenAIChat, AuthBearer, false, true
	case "@ai-sdk/openai", "@ai-sdk/xai":
		return ProtocolOpenAIResponses, AuthBearer, false, true
	case "@ai-sdk/azure":
		return ProtocolOpenAIResponses, AuthHeader, false, true
	case "@ai-sdk/anthropic", "@ai-sdk/amazon-bedrock":
		return ProtocolAnthropic, AuthHeader, false, true
	case "@ai-sdk/google-vertex/anthropic":
		return ProtocolAnthropic, AuthGCPADC, false, true
	case "@ai-sdk/google":
		return ProtocolGoogle, AuthHeader, false, true
	case "@ai-sdk/google-vertex":
		return ProtocolGoogle, AuthGCPADC, false, true
	}
	return ProtocolOpenAIChat, AuthBearer, false, false
}

// authHeaderFor names the header a `header` auth scheme uses per protocol
// (spec §6.1): x-api-key for anthropic, x-goog-api-key for google, api-key
// for Azure.
func authHeaderFor(npm, protocol string) string {
	if npm == "@ai-sdk/azure" {
		return "api-key"
	}
	switch protocol {
	case ProtocolAnthropic:
		return "x-api-key"
	case ProtocolGoogle:
		return "x-goog-api-key"
	}
	return ""
}

// surfaceForFamily is the spec §6.1 family → surface rule. An empty family
// returns "" so the provider fallback applies.
func surfaceForFamily(family string) string {
	switch {
	case family == "":
		return ""
	case strings.HasPrefix(family, "claude"):
		return SurfaceAnthropic
	case strings.HasPrefix(family, "gpt-oss"):
		return SurfaceGeneric
	case strings.HasPrefix(family, "gpt"), family == "o", family == "o-mini", family == "o-pro":
		return SurfaceOpenAI
	case strings.HasPrefix(family, "gemini"), strings.HasPrefix(family, "gemma"):
		return SurfaceGoogle
	}
	return SurfaceGeneric
}

var templateVarRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// templateURL rewrites ${VAR} to {VAR}, trims a trailing slash, and returns
// the variable names it found.
func templateURL(api string) (string, []string) {
	var vars []string
	out := templateVarRe.ReplaceAllStringFunc(strings.TrimRight(api, "/"), func(m string) string {
		name := templateVarRe.FindStringSubmatch(m)[1]
		vars = append(vars, name)
		return "{" + name + "}"
	})
	return out, vars
}

// isKeyVar reports whether an env var name is credential-shaped. Substring
// matches on purpose: AWS_BEARER_TOKEN_BEDROCK and AWS_ACCESS_KEY_ID must
// never become template variables, whose resolved values Resolve exposes.
func isKeyVar(name string) bool {
	return strings.Contains(name, "_KEY") || strings.Contains(name, "_TOKEN") ||
		strings.Contains(name, "_SECRET") || strings.HasSuffix(name, "_PAT")
}

// FromModelsDev converts a raw models.dev api.json into registry providers
// (spec §6.1). It is the only code that knows models.dev's schema, and it
// runs on both the embedded snapshot and the runtime cache.
func FromModelsDev(data []byte) ([]Provider, error) {
	var raw map[string]mdProvider
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("models.dev: %w", err)
	}
	if len(raw) == 0 {
		return nil, errors.New("models.dev: no providers")
	}
	ids := make([]string, 0, len(raw))
	for id := range raw {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Provider, 0, len(ids))
	for _, id := range ids {
		out = append(out, convertProvider(id, raw[id]))
	}
	return out, nil
}

func convertProvider(id string, mp mdProvider) Provider {
	p := Provider{ID: id, Name: mp.Name, Doc: mp.Doc, Models: map[string]Model{}}
	if mp.Name == "" {
		p.Name = id
	}
	protocol, auth, hidden, known := npmProtocol(mp.NPM)
	p.Hidden = hidden
	if !hidden {
		p.Protocol = protocol
		p.Transport.Auth = auth
		if auth == AuthHeader {
			p.Transport.AuthHeader = authHeaderFor(mp.NPM, protocol)
		}
		if !known {
			p.notes = append(p.notes, "protocol unverified: unknown npm "+mp.NPM)
		}
	}
	templated := map[string]bool{}
	if mp.API != "" {
		url, vars := templateURL(mp.API)
		p.Transport.BaseURL = url
		for _, v := range vars {
			templated[v] = true
			p.Transport.VarsEnv = mergeStringMap(p.Transport.VarsEnv, map[string]string{v: v})
		}
	}
	for _, e := range mp.Env {
		switch {
		case templated[e]:
			// already in VarsEnv
		case isKeyVar(e):
			p.APIKeyEnv = append(p.APIKeyEnv, e)
		default:
			p.Transport.VarsEnv = mergeStringMap(p.Transport.VarsEnv, map[string]string{e: e})
		}
	}
	for mid, mm := range mp.Models {
		m, keep := convertModel(id, mid, mm)
		if !keep {
			continue
		}
		p.Models[m.ID] = m
	}
	return p
}

func convertModel(providerID, key string, mm mdModel) (Model, bool) {
	hasText := false
	for _, o := range mm.Modalities.Output {
		if o == "text" {
			hasText = true
		}
	}
	if !hasText {
		return Model{}, false
	}
	id := key
	if mm.ID != "" {
		id = mm.ID
	}
	m := Model{ID: id, WireID: id, Family: mm.Family, Status: mm.Status, Surface: surfaceForFamily(mm.Family)}
	if trimmed, ok := strings.CutSuffix(id, "@default"); ok {
		m.ID = trimmed
		m.WireID = m.ID
	}
	c := &m.Caps
	if mm.Limit.Input > 0 {
		c.ContextWindow = new(mm.Limit.Input)
	} else if mm.Limit.Context > 0 {
		c.ContextWindow = new(mm.Limit.Context)
	}
	if mm.Limit.Output > 0 {
		c.MaxOutputTokens = new(mm.Limit.Output)
	}
	c.Tools = new(mm.ToolCall)
	c.StructuredOutput = mm.StructuredOutput
	c.Reasoning = new(mm.Reasoning)
	if mm.Temperature != nil && !*mm.Temperature {
		c.Sampling = new(false)
	}
	for _, ro := range mm.ReasoningOptions {
		c.ReasoningControls = append(c.ReasoningControls, ro.Type)
		if ro.Type == "effort" {
			for _, v := range ro.Values {
				if v != "none" {
					c.EffortValues = append(c.EffortValues, v)
				}
			}
		}
	}
	if len(mm.Modalities.Input) > 0 {
		c.InputModalities = append([]string(nil), mm.Modalities.Input...)
	}
	if mm.Knowledge != "" {
		c.KnowledgeCutoff = new(mm.Knowledge)
	}
	if mm.Cost != nil {
		cost := &Cost{Input: mm.Cost.Input, Output: mm.Cost.Output, CacheRead: mm.Cost.CacheRead, CacheWrite: mm.Cost.CacheWrite}
		for _, t := range mm.Cost.Tiers {
			if t.Tier.Type == "context" {
				cost.Tiers = append(cost.Tiers, CostTier{InputTokensAbove: t.Tier.Size, Input: t.Input, Output: t.Output, CacheRead: t.CacheRead, CacheWrite: t.CacheWrite})
			}
		}
		c.Cost = cost
	}
	if len(mm.Interleaved) > 0 && mm.Interleaved[0] == '{' {
		var obj struct {
			Field string `json:"field"`
		}
		if json.Unmarshal(mm.Interleaved, &obj) == nil && obj.Field != "" {
			c.ReasoningField = new(obj.Field)
		}
	}
	hasOverride := false
	if mm.Provider != nil {
		hasOverride = true
		convertModelOverride(&m, mm.Provider)
	}
	// Row-level hiding rules (spec §6.1).
	switch providerID {
	case "amazon-bedrock":
		if !hasOverride && !strings.HasPrefix(stripRegionPrefix(m.ID), "anthropic.") {
			m.Hidden = true
		}
	case "google-vertex":
		if strings.Contains(m.ID, "/") && (mm.Provider == nil || mm.Provider.API == "") {
			m.Hidden = true
		}
	}
	return m, true
}

func convertModelOverride(m *Model, o *mdModelProvider) {
	t := &Transport{}
	switch o.NPM {
	case "@ai-sdk/google-vertex/anthropic":
		m.Protocol = ProtocolAnthropic
		t.Preset = PresetVertexAnthropic
	case "@ai-sdk/google-vertex":
		m.Protocol = ProtocolGoogle
		t.Preset = PresetVertexGemini
	case "@ai-sdk/amazon-bedrock/mantle":
		t.Preset = PresetBedrockMantleOpenAI
		m.Protocol = ProtocolOpenAIResponses
	case "":
	default:
		if proto, _, hidden, _ := npmProtocol(o.NPM); !hidden {
			m.Protocol = proto
		}
	}
	switch o.Shape {
	case "responses":
		m.Protocol = ProtocolOpenAIResponses
	case "completions":
		m.Protocol = ProtocolOpenAIChat
	}
	if o.API != "" {
		url, vars := templateURL(o.API)
		t.BaseURL = url
		for _, v := range vars {
			t.VarsEnv = mergeStringMap(t.VarsEnv, map[string]string{v: v})
		}
	}
	if t.Preset != "" || t.BaseURL != "" {
		m.Transport = t
	}
}
