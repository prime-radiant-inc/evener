package migrate

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"primeradiant.com/evener/llm/registry"
)

// oldInstance is the pre-registry [instances.NAME] shape, re-declared here
// because the cut-over deleted its parser; evener migrate is the one place
// that still reads it (spec §14.1).
type oldInstance struct {
	Type              string            `toml:"type"`
	APIStyle          string            `toml:"api_style"`
	BaseURL           string            `toml:"base_url"`
	APIKey            string            `toml:"api_key"`
	Quirks            string            `toml:"quirks"`
	Headers           map[string]string `toml:"headers"`
	CredentialHeaders map[string]string `toml:"credential_headers"`
	Compat            map[string]any    `toml:"compat"`
	Models            map[string]any    `toml:"models"`
}

type oldFileShape struct {
	Schema    int                    `toml:"schema"`
	Default   string                 `toml:"default"`
	Instances map[string]oldInstance `toml:"instances"`
}

// oldTypeTarget maps each pre-registry type (and, for openai, api_style) to
// the registry provider it addressed, grounded in the deleted adapters'
// default endpoints: old bare kimi hit api.moonshot.ai (the platform), old
// kimi-anthropic hit api.kimi.com/coding, old glm hit api.z.ai.
var oldTypeTarget = map[string]struct{ base, protocol string }{
	"openai":               {base: "openai"},
	"anthropic":            {base: "anthropic"},
	"google":               {base: "google"},
	"openrouter":           {base: "openrouter"},
	"openrouter-anthropic": {base: "openrouter", protocol: "anthropic"},
	"kimi":                 {base: "moonshotai"},
	"kimi-anthropic":       {base: "kimi-for-coding"},
	"glm":                  {base: "zai"},
	"minimax":              {base: "minimax"},
	"ollama":               {base: "ollama"},
}

// vendorKeyEnv names each target provider's standard key variable (from the
// registry's models.dev data). The old schema let a vendor instance with a
// base_url inherit the vendor key; the registry deliberately does not, so
// the converter writes the variable explicitly.
var vendorKeyEnv = map[string]string{
	"anthropic":       "ANTHROPIC_API_KEY",
	"openai":          "OPENAI_API_KEY",
	"google":          "GEMINI_API_KEY",
	"openrouter":      "OPENROUTER_API_KEY",
	"moonshotai":      "MOONSHOT_API_KEY",
	"kimi-for-coding": "KIMI_API_KEY",
	"zai":             "ZHIPU_API_KEY",
	"minimax":         "MINIMAX_API_KEY",
}

var pureEnvRefRe = regexp.MustCompile(`^\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?$`)

// isOldProvidersSchema reports whether src is a pre-registry providers.toml —
// exactly the files the registry's own parser refuses with ErrOldSchema.
func isOldProvidersSchema(src []byte) bool {
	_, err := registry.ParseConfig(src)
	return errors.Is(err, registry.ErrOldSchema)
}

// convertProvidersConfig rewrites a pre-registry providers.toml into the
// schema-2 shape, recording every dropped field as a "# migrate:" comment in
// the output and as a note for the report. The result is dry-parsed through
// the registry's own reader before being returned, so a file this produces
// is always one the registry can load.
func convertProvidersConfig(src []byte) ([]byte, []string, error) {
	var old oldFileShape
	if _, err := toml.Decode(string(src), &old); err != nil {
		return nil, nil, fmt.Errorf("parse old providers.toml: %w", err)
	}
	names := make([]string, 0, len(old.Instances))
	for name := range old.Instances {
		names = append(names, name)
	}
	sort.Strings(names)

	defaultName := old.Default
	if defaultName == "" && len(names) > 0 {
		// The old loader's no-default rule picked the first sorted instance
		// name; the registry ranks differently (§5.1), so pin the old pick.
		defaultName = names[0]
	}

	var b strings.Builder
	var notes []string
	b.WriteString("# Converted from the pre-registry providers.toml schema by `evener migrate`.\n")
	b.WriteString("schema = 2\n")
	if defaultName != "" {
		fmt.Fprintf(&b, "default = %q\n", defaultName)
	}
	for _, name := range names {
		inst := old.Instances[name]
		b.WriteString("\n")
		target, known := oldTypeTarget[inst.Type]
		if !known {
			notes = append(notes, fmt.Sprintf("%s: unknown old type %q; instance commented out", name, inst.Type))
			fmt.Fprintf(&b, "# migrate: instance %q had unknown type %q; re-create it with `evener providers add`\n", name, inst.Type)
			fmt.Fprintf(&b, "# [providers.%s]\n", name)
			continue
		}
		protocol := target.protocol
		if inst.Type == "openai" && inst.APIStyle == "chat-completions" {
			protocol = "openai-chat"
		}
		for dropped, present := range map[string]bool{
			"quirks": inst.Quirks != "",
			"compat": len(inst.Compat) > 0,
			"models": len(inst.Models) > 0,
		} {
			if !present {
				continue
			}
			notes = append(notes, fmt.Sprintf("%s: dropped %s (the registry derives this now)", name, dropped))
		}
		if inst.Quirks != "" {
			fmt.Fprintf(&b, "# migrate: dropped quirks = %q — the registry derives protocol behavior now\n", inst.Quirks)
		}
		if len(inst.Compat) > 0 {
			b.WriteString("# migrate: dropped the compat table — the registry derives wire behavior from provider data\n")
		}
		if len(inst.Models) > 0 {
			b.WriteString("# migrate: dropped per-model overrides (models tables) — re-add with [providers.NAME.models] if still needed\n")
		}
		fmt.Fprintf(&b, "[providers.%s]\n", name)
		if target.base != name {
			fmt.Fprintf(&b, "base = %q\n", target.base)
		}
		if protocol != "" {
			fmt.Fprintf(&b, "protocol = %q\n", protocol)
		}
		if inst.BaseURL != "" {
			fmt.Fprintf(&b, "base_url = %q\n", inst.BaseURL)
		}
		switch {
		case inst.APIKey == "":
			if inst.BaseURL != "" {
				if env, ok := vendorKeyEnv[target.base]; ok {
					// See vendorKeyEnv: base_url instances no longer inherit.
					fmt.Fprintf(&b, "api_key_env = [%q]\n", env)
					notes = append(notes, fmt.Sprintf("%s: wrote api_key_env = [%q] — base_url instances no longer inherit the vendor key", name, env))
				}
			}
		case pureEnvRefRe.MatchString(inst.APIKey):
			fmt.Fprintf(&b, "api_key_env = [%q]\n", pureEnvRefRe.FindStringSubmatch(inst.APIKey)[1])
		case !strings.Contains(inst.APIKey, "$"):
			fmt.Fprintf(&b, "api_key = %q\n", inst.APIKey)
		default:
			notes = append(notes, fmt.Sprintf("%s: api_key used $-expansion the new schema does not spell; set it with `evener providers add`", name))
			b.WriteString("# migrate: the old api_key mixed literal text with $VAR expansion; re-create the credential by hand\n")
		}
		writeStringTable(&b, name, "headers", inst.Headers)
		writeStringTable(&b, name, "credential_headers", inst.CredentialHeaders)
	}
	out := []byte(b.String())
	if _, err := registry.ParseConfig(out); err != nil {
		return nil, notes, fmt.Errorf("converted providers.toml does not load: %w", err)
	}
	return out, notes, nil
}

func writeStringTable(b *strings.Builder, provider, key string, m map[string]string) {
	if len(m) == 0 {
		return
	}
	fmt.Fprintf(b, "[providers.%s.%s]\n", provider, key)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "%q = %q\n", k, m[k])
	}
}
