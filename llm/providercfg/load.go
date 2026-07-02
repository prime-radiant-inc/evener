package providercfg

import (
	"errors"
	"fmt"
	"net/textproto"
	"os"
	"sort"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
	"github.com/spf13/afero"
)

// knownTypes is the set of valid provider type values.
var knownTypes = map[Type]bool{
	"openai":               true,
	"anthropic":            true,
	"google":               true,
	"openrouter":           true,
	"openrouter-anthropic": true,
	"kimi":                 true,
	"kimi-anthropic":       true,
	"glm":                  true,
	"minimax":              true,
	"ollama":               true,
}

// KnownTypeNames returns the sorted list of valid provider type names.
func KnownTypeNames() []string {
	names := make([]string, 0, len(knownTypes))
	for t := range knownTypes {
		names = append(names, string(t))
	}
	sort.Strings(names)
	return names
}

// fileShape is the raw parsed shape of providers.toml before validation.
type fileShape struct {
	Schema    int                       `toml:"schema"`
	Default   string                    `toml:"default"`
	Instances map[string]InstanceConfig `toml:"instances"`
}

// validThinkingFormats is the set of thinking_format values the openai-compat
// request builder implements. Empty means the openai default.
var validThinkingFormats = map[string]bool{
	"":                   true,
	"openai":             true,
	"zai":                true,
	"deepseek":           true,
	"openrouter":         true,
	"together":           true,
	"qwen":               true,
	"qwen-chat-template": true,
	"chat-template":      true,
	"string-thinking":    true,
}

// ThinkingFormatNames returns the sorted non-empty valid thinking_format values.
func ThinkingFormatNames() []string {
	names := make([]string, 0, len(validThinkingFormats))
	for f := range validThinkingFormats {
		if f != "" {
			names = append(names, f)
		}
	}
	sort.Strings(names)
	return names
}

// validThinkingLevelKeys are the serf effort levels a thinking_levels map may
// name. "max" is accepted as an input alias and normalized to "xhigh" at load
// (serf's rank table treats them as one tier). "off" is deliberately absent:
// serf's "none" clears the effort setting to the provider default rather than
// forcing an explicit disable.
var validThinkingLevelKeys = map[string]bool{
	"minimal": true,
	"low":     true,
	"medium":  true,
	"high":    true,
	"xhigh":   true,
}

// CompatFamily reports whether an instance routes through the openai-compat
// adapter — the family that may carry compat/models configuration and whose
// adapters honor header-only authentication (no api_key; the configured
// Authorization header is the credential).
func CompatFamily(typ Type, style APIStyle) bool {
	switch typ {
	case "kimi", "glm", "openrouter", "ollama":
		return true
	case "openai":
		return style == StyleChatCompletions
	}
	return false
}

// validateCompat appends validation errors for one compat table.
func validateCompat(errs []string, instName, where string, c *CompatConfig) []string {
	if c == nil {
		return errs
	}
	if !validThinkingFormats[c.ThinkingFormat] {
		errs = append(errs, fmt.Sprintf("instance %q: %s: unknown thinking_format %q (must be one of %s)",
			instName, where, c.ThinkingFormat, strings.Join(ThinkingFormatNames(), ", ")))
	}
	switch c.MaxTokensField {
	case "", "max_tokens", "max_completion_tokens":
	default:
		errs = append(errs, fmt.Sprintf("instance %q: %s: unknown max_tokens_field %q (must be \"max_tokens\" or \"max_completion_tokens\")",
			instName, where, c.MaxTokensField))
	}
	switch c.CacheControlFormat {
	case "", "anthropic":
	default:
		errs = append(errs, fmt.Sprintf("instance %q: %s: unknown cache_control_format %q (only \"anthropic\" is supported)",
			instName, where, c.CacheControlFormat))
	}
	if c.MaxStopSequences != nil && *c.MaxStopSequences < 0 {
		errs = append(errs, fmt.Sprintf("instance %q: %s: max_stop_sequences must not be negative", instName, where))
	}
	for k, v := range c.ChatTemplateKwargs {
		switch v.(type) {
		case bool, int64, float64, string:
			// The documented contract: a table of scalars. Nested values
			// would silently degrade to fmt.Sprint strings on the first
			// Marshal round-trip (hub rewrites), corrupting the wire kwargs.
		default:
			errs = append(errs, fmt.Sprintf("instance %q: %s: chat_template_kwargs[%q] must be a scalar (bool, integer, float, or string), not %T", instName, where, k, v))
		}
	}
	return errs
}

// validateAndNormalizeModels validates each model entry and normalizes
// thinking_levels keys (lowercase; "max" folds into "xhigh"). It mutates the
// entries in place via the returned map.
func validateAndNormalizeModels(errs []string, instName string, models map[string]ModelConfig) ([]string, map[string]ModelConfig) {
	if len(models) == 0 {
		return errs, models
	}
	out := make(map[string]ModelConfig, len(models))
	for id, mc := range models {
		if strings.TrimSpace(id) == "" {
			errs = append(errs, fmt.Sprintf("instance %q: model id must not be empty", instName))
			continue
		}
		if mc.ContextWindow < 0 {
			errs = append(errs, fmt.Sprintf("instance %q: model %q: context_window must not be negative", instName, id))
		}
		if mc.MaxOutputTokens < 0 {
			errs = append(errs, fmt.Sprintf("instance %q: model %q: max_output_tokens must not be negative", instName, id))
		}
		if len(mc.ThinkingLevels) > 0 {
			norm := make(map[string]string, len(mc.ThinkingLevels))
			for k, v := range mc.ThinkingLevels {
				key := strings.ToLower(strings.TrimSpace(k))
				if key == "max" {
					key = "xhigh"
				}
				if _, dup := norm[key]; dup {
					// Map iteration order would otherwise pick the survivor
					// nondeterministically (e.g. max vs xhigh, LOW vs low).
					errs = append(errs, fmt.Sprintf("instance %q: model %q: thinking_levels has duplicate entries for level %q after normalization (max aliases xhigh; keys are case-insensitive)", instName, id, key))
					continue
				}
				if key == "off" {
					errs = append(errs, fmt.Sprintf("instance %q: model %q: thinking_levels key \"off\" is not supported (serf's \"none\" effort clears to the provider default)", instName, id))
					continue
				}
				if !validThinkingLevelKeys[key] {
					errs = append(errs, fmt.Sprintf("instance %q: model %q: thinking_levels key %q is not a serf effort level (minimal, low, medium, high, xhigh/max)", instName, id, k))
					continue
				}
				if strings.TrimSpace(v) == "" {
					errs = append(errs, fmt.Sprintf("instance %q: model %q: thinking_levels[%q] must map to a non-empty wire value", instName, id, k))
					continue
				}
				norm[key] = v
			}
			mc.ThinkingLevels = norm
		}
		errs = validateCompat(errs, instName, fmt.Sprintf("model %q compat", id), mc.Compat)
		out[id] = mc
	}
	return errs, out
}

// Load parses and validates providers.toml content from data.
func Load(data []byte) (Config, error) {
	var raw fileShape
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return Config{}, fmt.Errorf("providers.toml: parse: %w", err)
	}

	if len(raw.Instances) == 0 {
		return Config{}, errors.New("providers.toml: no instances defined")
	}

	// Sort names for deterministic ordering.
	names := make([]string, 0, len(raw.Instances))
	for name := range raw.Instances {
		names = append(names, name)
	}
	sort.Strings(names)

	var errs []string

	// Validate each instance name and fields.
	for _, name := range names {
		inst := raw.Instances[name]

		if containsUppercase(name) {
			errs = append(errs, fmt.Sprintf("instance %q: name must be lowercase", name))
		}
		if strings.Contains(name, "/") {
			errs = append(errs, fmt.Sprintf("instance %q: name must not contain '/'", name))
		}
		if !knownTypes[inst.Type] {
			errs = append(errs, fmt.Sprintf("instance %q: unknown type %q", name, inst.Type))
		}
		if inst.APIStyle != "" {
			if inst.Type != "openai" {
				errs = append(errs, fmt.Sprintf("instance %q: api_style is only valid for type \"openai\", not %q", name, inst.Type))
			} else if inst.APIStyle != StyleResponses && inst.APIStyle != StyleChatCompletions && inst.APIStyle != StyleAuto {
				errs = append(errs, fmt.Sprintf("instance %q: unknown api_style %q (must be %q, %q, or %q)", name, inst.APIStyle, StyleResponses, StyleChatCompletions, StyleAuto))
			}
		}
		if (inst.Compat != nil || len(inst.Models) > 0) && !CompatFamily(inst.Type, inst.APIStyle) {
			what := "compat"
			if len(inst.Models) > 0 {
				what = "models"
			}
			errs = append(errs, fmt.Sprintf("instance %q: %s is only valid for OpenAI-compatible instances (types kimi, glm, openrouter, ollama, or openai with api_style = \"chat-completions\"), not type %q", name, what, inst.Type))
		}
		// Header names must be non-empty and unique after HTTP canonicalization
		// (header names are case-insensitive on the wire; two spellings of one
		// name would collide with a map-iteration-order winner). Values are
		// otherwise unrestricted — they carry $ENV refs resolved later. Sort
		// for deterministic errors.
		canonSeen := make(map[string]string, len(inst.Headers))
		for _, hk := range sortedKeys(inst.Headers) {
			if strings.TrimSpace(hk) == "" {
				errs = append(errs, fmt.Sprintf("instance %q: header name must not be empty", name))
				continue
			}
			canon := textproto.CanonicalMIMEHeaderKey(hk)
			if prev, dup := canonSeen[canon]; dup {
				errs = append(errs, fmt.Sprintf("instance %q: headers %q and %q are the same HTTP header (names are case-insensitive)", name, prev, hk))
				continue
			}
			canonSeen[canon] = hk
		}
		errs = validateCompat(errs, name, "compat", inst.Compat)
		var models map[string]ModelConfig
		errs, models = validateAndNormalizeModels(errs, name, inst.Models)
		inst.Models = models
		raw.Instances[name] = inst
	}

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("providers.toml: %s", strings.Join(errs, "; "))
	}

	// Build ordered instance slice.
	instances := make([]InstanceConfig, 0, len(names))
	for _, name := range names {
		inst := raw.Instances[name]
		inst.Name = name
		instances = append(instances, inst)
	}

	// Resolve default.
	def := raw.Default
	if def == "" {
		def = names[0]
	} else {
		found := false
		for _, name := range names {
			if name == def {
				found = true
				break
			}
		}
		if !found {
			return Config{}, fmt.Errorf("providers.toml: default %q does not name an existing instance", def)
		}
	}

	return Config{
		Default:   def,
		Instances: instances,
	}, nil
}

// LoadFile reads path and parses it. If the file is absent, exists=false and
// err=nil are returned. If the file exists but is invalid, err is non-nil.
func LoadFile(path string) (cfg Config, exists bool, err error) {
	return loadFileFS(afero.NewOsFs(), path)
}

// loadFileFS is the filesystem seam beneath LoadFile: it reads path through an
// injected afero.Fs. Production passes afero.NewOsFs(), whose methods delegate
// directly to os, so behavior is byte-identical to using os calls; tests and
// fuzzers inject an in-memory or sandboxed filesystem to exercise persistence
// without touching real disk.
func loadFileFS(fs afero.Fs, path string) (cfg Config, exists bool, err error) {
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, false, nil
		}
		return Config{}, false, fmt.Errorf("providers.toml: read %s: %w", path, err)
	}
	cfg, err = Load(data)
	if err != nil {
		return Config{}, true, err
	}
	return cfg, true, nil
}

// containsUppercase reports whether s contains any Unicode uppercase letter.
func containsUppercase(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}
