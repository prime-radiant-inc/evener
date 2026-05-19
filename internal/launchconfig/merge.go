package launchconfig

import (
	"fmt"
	"strings"
)

// layerOrder is the application order (least → most specific).
var layerOrder = []LayerName{LayerGlobal, LayerRepo, LayerProject, LayerLaunch}

// credentialBlocklistSuffixes are the substring patterns refused inside
// env maps at every layer. Credentials must flow through the credentials
// store, never through a launch layer that might get committed.
var credentialBlocklistSuffixes = []string{
	"API_KEY", "_KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL",
}

func isCredentialEnvKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, s := range credentialBlocklistSuffixes {
		if strings.Contains(upper, s) {
			return true
		}
	}
	return false
}

// IsCredentialEnvKey is the exported version of the internal blocklist
// check, used by hub RPC handlers to refuse credential keys at write time.
func IsCredentialEnvKey(key string) bool { return isCredentialEnvKey(key) }

// mergeLayers composes the supplied layers in canonical order. Layers not
// present in the map are treated as empty. Returns the resolved effective
// view plus any non-fatal diagnostics found while merging (duplicate MCP
// names, blocked credential keys, etc.).
func mergeLayers(layers map[LayerName]Layer) (Resolved, []Diagnostic) {
	var diags []Diagnostic
	eff := Layer{Env: map[string]string{}}
	prov := map[string]LayerName{}
	contributing := map[LayerName]Layer{}

	mcpNames := map[string]LayerName{}

	for _, name := range layerOrder {
		l, ok := layers[name]
		if !ok {
			continue
		}
		nonEmpty := false

		if l.Model != "" {
			eff.Model = l.Model
			prov["model"] = name
			nonEmpty = true
		}
		if l.Agent != "" {
			eff.Agent = l.Agent
			prov["agent"] = name
			nonEmpty = true
		}
		if l.ReasoningEffort != "" {
			eff.ReasoningEffort = l.ReasoningEffort
			prov["reasoning_effort"] = name
			nonEmpty = true
		}
		if l.ContextStrategy != "" {
			eff.ContextStrategy = l.ContextStrategy
			prov["context_strategy"] = name
			nonEmpty = true
		}
		if l.MaxRounds != nil {
			v := *l.MaxRounds
			eff.MaxRounds = &v
			prov["max_rounds"] = name
			nonEmpty = true
		}
		if l.MaxSubagentDepth != nil {
			v := *l.MaxSubagentDepth
			eff.MaxSubagentDepth = &v
			prov["max_subagent_depth"] = name
			nonEmpty = true
		}
		if l.NoProjectPrompts != nil {
			v := *l.NoProjectPrompts
			eff.NoProjectPrompts = &v
			prov["no_project_prompts"] = name
			nonEmpty = true
		}
		if l.AppReplaySize != nil {
			if name != LayerGlobal {
				diags = append(diags, Diagnostic{
					Layer: name, Field: "app_replay_size",
					Message: "app_replay_size is only honored at the global layer",
				})
			} else {
				v := *l.AppReplaySize
				eff.AppReplaySize = &v
				prov["app_replay_size"] = name
				nonEmpty = true
			}
		}

		// Lists: append in layer order.
		if len(l.SkillsDirs) > 0 {
			eff.SkillsDirs = append(eff.SkillsDirs, l.SkillsDirs...)
			prov["skills_dirs"] = name
			nonEmpty = true
		}
		if len(l.PluginDirs) > 0 {
			eff.PluginDirs = append(eff.PluginDirs, l.PluginDirs...)
			prov["plugin_dirs"] = name
			nonEmpty = true
		}
		if len(l.MCPConfigs) > 0 {
			eff.MCPConfigs = append(eff.MCPConfigs, l.MCPConfigs...)
			prov["mcp_configs"] = name
			nonEmpty = true
		}
		if len(l.SystemPromptAppend) > 0 {
			eff.SystemPromptAppend = append(eff.SystemPromptAppend, l.SystemPromptAppend...)
			prov["system_prompt_append"] = name
			nonEmpty = true
		}
		// ModelFallbacks: higher-precedence layers REPLACE rather than append.
		// A user explicitly setting a fallback chain at the launch layer means
		// "use this exact chain", not "extend the global default chain."
		if len(l.ModelFallbacks) > 0 {
			eff.ModelFallbacks = append([]string(nil), l.ModelFallbacks...)
			prov["model_fallbacks"] = name
			nonEmpty = true
		}
		if len(l.MCPs) > 0 {
			for _, m := range l.MCPs {
				if prev, ok := mcpNames[m.Name]; ok {
					diags = append(diags, Diagnostic{
						Layer: name, Field: "mcps",
						Message: fmt.Sprintf("duplicate mcp name %q (previously seen at layer %q); serf launch-check will reject this", m.Name, prev),
					})
				} else {
					mcpNames[m.Name] = name
				}
				eff.MCPs = append(eff.MCPs, m)
			}
			prov["mcps"] = name
			nonEmpty = true
		}
		// Env map: last-write-wins per key, with credential blocklist.
		for k, v := range l.Env {
			if isCredentialEnvKey(k) {
				diags = append(diags, Diagnostic{
					Layer: name, Field: "env." + k,
					Message: fmt.Sprintf("env key %q looks like a credential; route through credentials store", k),
				})
				continue
			}
			eff.Env[k] = v
		}
		if len(l.Env) > 0 {
			prov["env"] = name
			nonEmpty = true
		}
		if nonEmpty {
			contributing[name] = l
		}
	}

	return Resolved{
		Effective:   eff,
		Layers:      contributing,
		Provenance:  prov,
		Diagnostics: diags,
	}, diags
}
