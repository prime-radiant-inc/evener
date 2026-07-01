package providercfg

import (
	"errors"
	"fmt"
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
