// Package providerconfig is the leaf type/behavior-tag vocabulary shared by
// llm, agent, cmdutil, and the cmd/* binaries. It imports none of them.
package providerconfig

import (
	"os"
	"path/filepath"
)

type Type string
type APIStyle string

const (
	StyleResponses       APIStyle = "responses"
	StyleChatCompletions APIStyle = "chat-completions"
)

type InstanceConfig struct {
	Name     string
	Type     Type
	APIStyle APIStyle
	BaseURL  string
	APIKey   string
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

// DefaultStateRoot returns $hubStateRoot (default ~/.serf), relocated here so
// cmd/serf and cmd/serf-hub resolve the identical path.
func DefaultStateRoot() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".serf")
	}
	return ".serf"
}
