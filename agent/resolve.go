package agent

import (
	"fmt"
	"sort"
	"strings"

	"primeradiant.com/serf/llm/providercfg"
)

// ResolveProfileFromConfig maps an instance name to a *Profile using
// the loaded providercfg.Config.
//
// ref is parsed as "<instanceName>/<model>" where model may contain additional
// slashes (e.g. "work/anthropic/claude-opus-4"). The instance name must match
// a configured instance; unknown names return an error that lists all known
// instance names.
//
// The profile is built by (Type, APIStyle) and then renamed to the instance name
// via WithProviderID so the instance's user-assigned name becomes the profile ID
// while the behavior tag stays derived from the provider type (not the name).
//
// The context window is passed as 0 to newOpenAICompatProfile — the catalog
// lookup inside the constructor keys on the behavior tag and resolves the
// right window from the embedded model catalog.
func ResolveProfileFromConfig(cfg providercfg.Config, ref string) (*Profile, error) {
	instName, model, ok := strings.Cut(ref, "/")
	if !ok || instName == "" || model == "" {
		return nil, fmt.Errorf("invalid model ref %q: must be instanceName/model", ref)
	}

	var inst *providercfg.InstanceConfig
	for i := range cfg.Instances {
		if cfg.Instances[i].Name == instName {
			inst = &cfg.Instances[i]
			break
		}
	}
	if inst == nil {
		names := make([]string, 0, len(cfg.Instances))
		for _, ic := range cfg.Instances {
			names = append(names, ic.Name)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("unknown instance %q: configured instances are %s",
			instName, strings.Join(names, ", "))
	}

	typ := string(inst.Type)
	style := string(inst.APIStyle)

	var raw *Profile
	switch typ {
	case "openai":
		if style == string(providercfg.StyleChatCompletions) {
			// Pass "openai-compatible" as the id so BehaviorTag("openai-compatible","")
			// produces the correct tag, then rename to the instance name.
			raw = newOpenAICompatProfile("openai-compatible", model, 0)
		} else {
			// responses or empty style → full OpenAI profile
			raw = NewOpenAIProfile(model)
		}
	case "anthropic":
		raw = newAnthropicProfile(model)
	case "google":
		raw = newGeminiProfile(model)
	case "minimax":
		raw = newMiniMaxProfile(model)
	case "openrouter-anthropic":
		raw = newOpenRouterAnthropicProfile(model)
	case "kimi", "glm", "openrouter", "ollama":
		// Pass the type as the constructor id so the behavior tag is the type.
		raw = newOpenAICompatProfile(typ, model, 0)
	default:
		return nil, fmt.Errorf("unknown provider type %q for instance %q", typ, instName)
	}

	return WithProviderID(raw, instName), nil
}
