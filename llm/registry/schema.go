package registry

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Layer tags (spec §5), used in Resolved.Provenance values and warnings.
const (
	LayerSnapshot = "snapshot"
	LayerCache    = "cache"
	LayerOverlay  = "overlay"
	LayerLive     = "live"
	LayerConfig   = "config"
)

// Host rules (spec §9.1): the only host-aware code in the system.
const (
	HostRuleVertexLocation = "vertex-location"
	HostRuleOllamaHost     = "ollama-host"
)

// Layer is one parsed data layer: the curated overlay or providers.toml.
// Provider glob rows live in Provider.Models under their `*` keys; top-level
// [models."<glob>"] rows live in TopGlobs (spec §4.1, §10).
type Layer struct {
	Tag          string
	Default      string
	DefaultOrder []string
	Transports   map[string]Transport
	TopGlobs     map[string]Model
	Providers    map[string]Provider
}

// ErrOldSchema marks a providers.toml written for the pre-registry schema
// (spec §14.1). Callers match it with errors.Is.
var ErrOldSchema = errors.New("providers.toml uses the pre-registry schema ([instances.*], type, api_style, quirks, compat); rewrite it per docs/superpowers/specs/2026-08-28-provider-registry-design.md §14.1")

// Vocabularies validated at load (spec §10).
var (
	protocols   = map[string]bool{ProtocolOpenAIChat: true, ProtocolOpenAIResponses: true, ProtocolAnthropic: true, ProtocolGoogle: true}
	surfaces    = map[string]bool{SurfaceOpenAI: true, SurfaceAnthropic: true, SurfaceGoogle: true, SurfaceGeneric: true}
	authSchemes = map[string]bool{AuthBearer: true, AuthOptionalBearer: true, AuthHeader: true, AuthNone: true, AuthGCPADC: true, AuthOAuthOpenAICodex: true}
	hostRules   = map[string]bool{HostRuleVertexLocation: true, HostRuleOllamaHost: true}

	thinkingFormats       = map[string]bool{"openai": true, "openrouter": true, "zai": true, "deepseek": true, "together": true, "qwen": true, "qwen-chat-template": true, "chat-template": true, "string-thinking": true}
	thinkingShapes        = map[string]bool{"adaptive": true, "budget": true, "budget+effort": true}
	thinkingDisplays      = map[string]bool{"": true, "summarized": true}
	maxTokensFields       = map[string]bool{"max_tokens": true, "max_completion_tokens": true}
	cacheControls         = map[string]bool{"anthropic": true}
	reasoningFields       = map[string]bool{"reasoning_content": true, "reasoning": true, "reasoning_text": true, "reasoning_details": true}
	reasoningSummaries    = map[string]bool{"none": true, "auto": true, "detailed": true}
	imageDetails          = map[string]bool{"original": true, "high": true, "low": true, "omit": true}
	reasoningControlNames = map[string]bool{"effort": true, "budget_tokens": true, "toggle": true}
)

var providerNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// validProviderName enforces spec §10: lowercase, no slash.
func validProviderName(name string) bool { return providerNameRe.MatchString(name) }

// oldSchemaKeys are the pre-registry provider keys (spec §14.1).
var oldSchemaKeys = []string{"type", "api_style", "quirks", "compat"}

// fileSchema mirrors the TOML shape shared by the overlay and providers.toml
// (spec §10). Caps embeds directly so every cap is a key by its snake_case
// name; transportSchema adds the transport keys at the same level.
type fileSchema struct {
	Default      string                     `toml:"default"`
	DefaultOrder []string                   `toml:"default_order"`
	Transports   map[string]transportSchema `toml:"transports"`
	Models       map[string]modelSchema     `toml:"models"`
	Providers    map[string]providerSchema  `toml:"providers"`
}

type transportSchema struct {
	Preset              string            `toml:"transport"`
	Auth                string            `toml:"auth"`
	AuthHeader          string            `toml:"auth_header"`
	BaseURL             string            `toml:"base_url"`
	HostRule            string            `toml:"host_rule"`
	Endpoint            string            `toml:"endpoint"`
	StreamEndpoint      string            `toml:"stream_endpoint"`
	ModelsEndpoint      string            `toml:"models_endpoint"`
	CountTokensEndpoint string            `toml:"count_tokens_endpoint"`
	Vars                map[string]string `toml:"vars"`
	VarsEnv             map[string]string `toml:"vars_env"`
	Body                map[string]any    `toml:"body"`
}

func (ts transportSchema) transport() Transport {
	return Transport(ts)
}

func (ts transportSchema) isZero() bool {
	return ts.Preset == "" && ts.Auth == "" && ts.AuthHeader == "" && ts.BaseURL == "" && ts.HostRule == "" &&
		ts.Endpoint == "" && ts.StreamEndpoint == "" && ts.ModelsEndpoint == "" && ts.CountTokensEndpoint == "" &&
		len(ts.Vars) == 0 && len(ts.VarsEnv) == 0 && len(ts.Body) == 0
}

type providerSchema struct {
	Base              string                 `toml:"base"`
	InheritModels     *bool                  `toml:"inherit_models"`
	Implicit          *bool                  `toml:"implicit"`
	Name              string                 `toml:"name"`
	Doc               string                 `toml:"doc"`
	Protocol          string                 `toml:"protocol"`
	Surface           string                 `toml:"surface"`
	Family            string                 `toml:"family"`
	APIKey            string                 `toml:"api_key"`
	APIKeyEnv         []string               `toml:"api_key_env"`
	Headers           map[string]string      `toml:"headers"`
	CredentialHeaders map[string]string      `toml:"credential_headers"`
	DefaultModel      string                 `toml:"default_model"`
	CheapModel        string                 `toml:"cheap_model"`
	Models            map[string]modelSchema `toml:"models"`
	transportSchema
	Caps
}

type modelSchema struct {
	AliasOf  string            `toml:"alias_of"`
	WireID   string            `toml:"wire_id"`
	Family   string            `toml:"family"`
	Protocol string            `toml:"protocol"`
	Surface  string            `toml:"surface"`
	Headers  map[string]string `toml:"headers"`
	transportSchema
	Caps
}

func (ms modelSchema) model(id string) Model {
	m := Model{ID: id, WireID: ms.WireID, AliasOf: ms.AliasOf, Family: ms.Family, Protocol: ms.Protocol, Surface: ms.Surface, Headers: ms.Headers, Caps: ms.Caps}
	if !ms.isZero() {
		t := ms.transport()
		m.Transport = &t
	}
	return m
}

// parseLayer decodes and validates one TOML layer. curated permits the
// overlay-only keys (spec §10).
func parseLayer(data []byte, tag string, curated bool) (*Layer, error) {
	var fs fileSchema
	md, err := toml.Decode(string(data), &fs)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", tag, err)
	}
	if !curated {
		if md.IsDefined("instances") {
			return nil, fmt.Errorf("%w ([instances.*])", ErrOldSchema)
		}
		for name := range fs.Providers {
			for _, k := range oldSchemaKeys {
				if md.IsDefined("providers", name, k) {
					return nil, fmt.Errorf("%w (providers.%s.%s)", ErrOldSchema, name, k)
				}
			}
		}
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("%s: unknown key(s): %s", tag, strings.Join(keys, ", "))
	}
	if !curated {
		if fs.DefaultOrder != nil {
			return nil, fmt.Errorf("%s: default_order is only valid in the curated overlay", tag)
		}
		if len(fs.Transports) > 0 {
			return nil, fmt.Errorf("%s: [transports.*] is only valid in the curated overlay", tag)
		}
	}
	l := &Layer{Tag: tag, Default: fs.Default, DefaultOrder: fs.DefaultOrder, Transports: map[string]Transport{}, TopGlobs: map[string]Model{}, Providers: map[string]Provider{}}
	for name, ts := range fs.Transports {
		if ts.Preset != "" {
			return nil, fmt.Errorf("%s: transports.%s: a transport preset cannot name another preset", tag, name)
		}
		if err := validateTransport(ts, "transports."+name); err != nil {
			return nil, fmt.Errorf("%s: %w", tag, err)
		}
		l.Transports[name] = ts.transport()
	}
	for key, ms := range fs.Models {
		if !isGlob(key) {
			return nil, fmt.Errorf("%s: models.%q: top-level model rows must be globs", tag, key)
		}
		if ms.Protocol != "" {
			return nil, fmt.Errorf("%s: models.%q: protocol is not allowed on a glob row", tag, key)
		}
		if ms.Preset != "" {
			return nil, fmt.Errorf("%s: models.%q: transport presets are not allowed on a glob row", tag, key)
		}
		if err := validateModel(ms, fmt.Sprintf("models.%q", key)); err != nil {
			return nil, fmt.Errorf("%s: %w", tag, err)
		}
		l.TopGlobs[key] = ms.model(key)
	}
	for name, ps := range fs.Providers {
		where := "providers." + name
		if !validProviderName(name) {
			return nil, fmt.Errorf("%s: %s: provider names are lowercase with no slash", tag, where)
		}
		if !curated && (ps.Implicit != nil || ps.Name != "" || ps.Doc != "") {
			return nil, fmt.Errorf("%s: %s: implicit, name, and doc are only valid in the curated overlay", tag, where)
		}
		if err := validateProvider(ps, where); err != nil {
			return nil, fmt.Errorf("%s: %w", tag, err)
		}
		p := Provider{
			ID: name, Base: ps.Base, InheritModels: ps.InheritModels, Implicit: ps.Implicit, Name: ps.Name, Doc: ps.Doc,
			Protocol: ps.Protocol, Surface: ps.Surface, Family: ps.Family, Transport: ps.transport(),
			APIKey: ps.APIKey, Headers: ps.Headers, CredentialHeaders: ps.CredentialHeaders, Caps: ps.Caps,
			DefaultModel: ps.DefaultModel, CheapModel: ps.CheapModel, Models: map[string]Model{},
		}
		if md.IsDefined("providers", name, "api_key_env") {
			p.APIKeyEnv = append([]string{}, ps.APIKeyEnv...)
		}
		for key, ms := range ps.Models {
			if isGlob(key) && ms.Protocol != "" {
				return nil, fmt.Errorf("%s: %s.models.%q: protocol is not allowed on a glob row", tag, where, key)
			}
			if isGlob(key) && ms.Preset != "" {
				return nil, fmt.Errorf("%s: %s.models.%q: transport presets are not allowed on a glob row", tag, where, key)
			}
			if err := validateModel(ms, fmt.Sprintf("%s.models.%q", where, key)); err != nil {
				return nil, fmt.Errorf("%s: %w", tag, err)
			}
			p.Models[key] = ms.model(key)
		}
		l.Providers[name] = p
	}
	if curated {
		for _, id := range fs.DefaultOrder {
			p, ok := fs.Providers[id]
			if !ok || p.Implicit == nil || !*p.Implicit {
				return nil, fmt.Errorf("%s: default_order entry %q is not an implicit provider in this file", tag, id)
			}
		}
	}
	return l, nil
}

func validateProvider(ps providerSchema, where string) error {
	if ps.Protocol != "" && !protocols[ps.Protocol] {
		return fmt.Errorf("%s: unknown protocol %q", where, ps.Protocol)
	}
	if ps.Surface != "" && !surfaces[ps.Surface] {
		return fmt.Errorf("%s: unknown surface %q", where, ps.Surface)
	}
	if err := checkEnvRefs(ps.APIKey, where+".api_key"); err != nil {
		return err
	}
	for k, v := range ps.CredentialHeaders {
		if err := checkEnvRefs(v, fmt.Sprintf("%s.credential_headers.%q", where, k)); err != nil {
			return err
		}
	}
	for k, v := range ps.Headers {
		if err := checkEnvRefs(v, fmt.Sprintf("%s.headers.%q", where, k)); err != nil {
			return err
		}
	}
	if err := validateTransport(ps.transportSchema, where); err != nil {
		return err
	}
	return validateCaps(ps.Caps, where)
}

func validateModel(ms modelSchema, where string) error {
	if ms.Protocol != "" && !protocols[ms.Protocol] {
		return fmt.Errorf("%s: unknown protocol %q", where, ms.Protocol)
	}
	if ms.Surface != "" && !surfaces[ms.Surface] {
		return fmt.Errorf("%s: unknown surface %q", where, ms.Surface)
	}
	for k, v := range ms.Headers {
		if err := checkEnvRefs(v, fmt.Sprintf("%s.headers.%q", where, k)); err != nil {
			return err
		}
	}
	if err := validateTransport(ms.transportSchema, where); err != nil {
		return err
	}
	return validateCaps(ms.Caps, where)
}

func validateTransport(ts transportSchema, where string) error {
	if ts.Auth != "" && !authSchemes[ts.Auth] {
		return fmt.Errorf("%s: unknown auth %q", where, ts.Auth)
	}
	if ts.HostRule != "" && !hostRules[ts.HostRule] {
		return fmt.Errorf("%s: unknown host_rule %q", where, ts.HostRule)
	}
	for k, v := range ts.Vars {
		if err := checkEnvRefs(v, fmt.Sprintf("%s.vars.%s", where, k)); err != nil {
			return err
		}
	}
	return nil
}

func validateCaps(c Caps, where string) error {
	check := func(field string, v *string, vocab map[string]bool) error {
		if v != nil && !vocab[*v] {
			return fmt.Errorf("%s: %s = %q is not one of %s", where, field, *v, vocabList(vocab))
		}
		return nil
	}
	for _, e := range []error{
		check("thinking_format", c.ThinkingFormat, thinkingFormats),
		check("thinking_shape", c.ThinkingShape, thinkingShapes),
		check("thinking_display", c.ThinkingDisplay, thinkingDisplays),
		check("max_tokens_field", c.MaxTokensField, maxTokensFields),
		check("cache_control", c.CacheControl, cacheControls),
		check("reasoning_field", c.ReasoningField, reasoningFields),
		check("reasoning_summary", c.ReasoningSummary, reasoningSummaries),
		check("image_detail", c.ImageDetail, imageDetails),
	} {
		if e != nil {
			return e
		}
	}
	for _, rc := range c.ReasoningControls {
		if !reasoningControlNames[rc] {
			return fmt.Errorf("%s: reasoning_controls entry %q is not one of %s", where, rc, vocabList(reasoningControlNames))
		}
	}
	for _, ev := range c.EffortValues {
		if ev == "" || ev == "off" {
			return fmt.Errorf("%s: effort_values entries must be non-empty and not \"off\"", where)
		}
	}
	return nil
}

func vocabList(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if k == "" {
			k = `""`
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, " | ")
}
