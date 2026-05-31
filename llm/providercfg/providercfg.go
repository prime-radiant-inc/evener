// Package providercfg is the public configuration schema for serf's providers
// (providers.toml) — the leaf instance/type/behavior-tag vocabulary used to
// build an llm Client from configuration (via llm.NewFromProviders) and to
// resolve agent profiles. It imports no other serf package.
package providercfg

type Type string
type APIStyle string

const (
	StyleResponses       APIStyle = "responses"
	StyleChatCompletions APIStyle = "chat-completions"
)

type InstanceConfig struct {
	Name     string   `toml:"-"`
	Type     Type     `toml:"type"`
	APIStyle APIStyle `toml:"api_style"`
	BaseURL  string   `toml:"base_url"`
	APIKey   string   `toml:"api_key"`
	Quirks   string   `toml:"quirks"`
}

type Config struct {
	Default   string
	Instances []InstanceConfig
}

// BehaviorTag is the internal behavior identity every provider-conditional
// behavior keys on. It equals the type for all types except openai, which
// splits by apiStyle.
func BehaviorTag(typ, style string) string {
	if typ == "openai" && style == string(StyleChatCompletions) {
		return "openai-compatible"
	}
	return typ
}

// NameToTag maps each instance's name to its behavior tag.
func NameToTag(cfg Config) map[string]string {
	m := make(map[string]string, len(cfg.Instances))
	for _, in := range cfg.Instances {
		m[in.Name] = BehaviorTag(string(in.Type), string(in.APIStyle))
	}
	return m
}
