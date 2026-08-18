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

// sandboxModeIsOff reports whether a launch-config sandbox mode denotes no
// confinement — empty (unset) or "off" (case/space-insensitive). sandbox_net has
// no effect in that state (serf ignores --sandbox-net without a sandbox mode), so
// ToArgs suppresses the flag and mergeLayers warns.
func sandboxModeIsOff(mode string) bool {
	m := strings.ToLower(strings.TrimSpace(mode))
	return m == "" || m == "off"
}

// isKnownSandboxMode reports whether mode is one of the sandbox modes the schema
// offers (and serf's --sandbox flag accepts), tolerating surrounding whitespace and
// case the way serf's ParseMode does. Derived from sandboxChoices so the launch UI
// and this validation never drift; the empty choice ("(default)") is unset, not a
// mode. Kept in this package rather than importing agent/sandbox, matching how the
// choice list is already hardcoded here.
func isKnownSandboxMode(mode string) bool {
	m := strings.ToLower(strings.TrimSpace(mode))
	for _, c := range sandboxChoices() {
		if c.Value != "" && c.Value == m {
			return true
		}
	}
	return false
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
		if l.FastCheapModel != "" {
			eff.FastCheapModel = l.FastCheapModel
			prov["fast_cheap_model"] = name
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
		if l.OpenAIResponsesContinuation != "" {
			eff.OpenAIResponsesContinuation = l.OpenAIResponsesContinuation
			prov["openai_responses_continuation"] = name
			nonEmpty = true
		}
		if l.Sandbox != "" {
			eff.Sandbox = l.Sandbox
			prov["sandbox"] = name
			nonEmpty = true
		}
		if l.SandboxNet != nil {
			v := *l.SandboxNet
			eff.SandboxNet = &v
			prov["sandbox_net"] = name
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
		if l.MaxConcurrentDelegateTurns != nil {
			v := *l.MaxConcurrentDelegateTurns
			eff.MaxConcurrentDelegateTurns = &v
			prov["max_concurrent_delegate_turns"] = name
			nonEmpty = true
		}
		if l.MaxRetainedTerminal != nil {
			v := *l.MaxRetainedTerminal
			eff.MaxRetainedTerminal = &v
			prov["max_retained_terminal"] = name
			nonEmpty = true
		}
		if l.NoProjectPrompts != nil {
			v := *l.NoProjectPrompts
			eff.NoProjectPrompts = &v
			prov["no_project_prompts"] = name
			nonEmpty = true
		}
		if l.NonInteractive != nil {
			v := *l.NonInteractive
			eff.NonInteractive = &v
			prov["non_interactive"] = name
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
		if l.SystemPromptMode != "" {
			eff.SystemPromptMode = l.SystemPromptMode
			eff.SystemPromptFile = ""
			eff.SystemPromptText = ""
			if l.SystemPromptMode == "file" {
				eff.SystemPromptFile = l.SystemPromptFile
			}
			if l.SystemPromptMode == "inline" {
				eff.SystemPromptText = l.SystemPromptText
			}
			prov["system_prompt_mode"] = name
			nonEmpty = true
		}
		if l.SystemPromptAppendMode != "" {
			eff.SystemPromptAppendMode = l.SystemPromptAppendMode
			eff.SystemPromptAppendFile = ""
			eff.SystemPromptAppendText = ""
			eff.SystemPromptAppend = nil
			if l.SystemPromptAppendMode == "file" {
				eff.SystemPromptAppendFile = l.SystemPromptAppendFile
			}
			if l.SystemPromptAppendMode == "inline" {
				eff.SystemPromptAppendText = l.SystemPromptAppendText
			}
			prov["system_prompt_append_mode"] = name
			nonEmpty = true
		}
		if l.Verbose != nil {
			v := *l.Verbose
			eff.Verbose = &v
			prov["verbose"] = name
			nonEmpty = true
		}
		if l.TraceFile != "" {
			eff.TraceFile = l.TraceFile
			prov["trace_file"] = name
			nonEmpty = true
		}
		if l.CPUProfile != "" {
			eff.CPUProfile = l.CPUProfile
			prov["cpu_profile"] = name
			nonEmpty = true
		}
		if l.ExportATIFPath != "" {
			eff.ExportATIFPath = l.ExportATIFPath
			prov["export_atif_path"] = name
			nonEmpty = true
		}
		if l.ExportATIFProviderHandles != "" {
			eff.ExportATIFProviderHandles = l.ExportATIFProviderHandles
			prov["export_atif_provider_handles"] = name
			nonEmpty = true
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
		if len(l.SystemPromptAppend) > 0 && l.SystemPromptAppendMode == "" {
			eff.SystemPromptAppendMode = "file"
			eff.SystemPromptAppendFile = l.SystemPromptAppend[0]
			eff.SystemPromptAppendText = ""
			eff.SystemPromptAppend = nil
			prov["system_prompt_append_mode"] = name
			nonEmpty = true
			if len(l.SystemPromptAppend) > 1 {
				diags = append(diags, Diagnostic{
					Layer: name, Field: "system_prompt_append",
					Message: "legacy system_prompt_append has multiple entries; UI supports one append source, using the first entry",
				})
			}
		}
		// ModelFallbacks: higher-precedence layers REPLACE rather than append.
		// A user explicitly setting a fallback chain at the launch layer means
		// "use this exact chain", not "extend the global default chain."
		if l.ModelFallbacks != nil {
			cp := append([]string{}, (*l.ModelFallbacks)...)
			eff.ModelFallbacks = &cp
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

	// A typo'd sandbox mode merges cleanly and would only fail at spawn (serf's
	// ParseMode dies) with no launch-config pointer at the typo. Warn here; the
	// fail-loud-at-spawn stays as the backstop.
	if eff.Sandbox != "" && !isKnownSandboxMode(eff.Sandbox) {
		diags = append(diags, Diagnostic{
			Layer:   prov["sandbox"],
			Field:   "sandbox",
			Message: fmt.Sprintf("unknown sandbox mode %q (want one of: off, read-only, workspace-write, restricted)", eff.Sandbox),
		})
	}

	// sandbox_net only takes effect alongside a non-off sandbox mode. A merged
	// effective config with net set but no (or off) mode is a silent no-op at serf
	// serve (mirroring the delegate path, which now refuses the same combination), so
	// warn. Checked on the EFFECTIVE config, not per layer, because the mode and net
	// may legitimately arrive from different layers.
	if eff.SandboxNet != nil && sandboxModeIsOff(eff.Sandbox) {
		diags = append(diags, Diagnostic{
			Layer:   prov["sandbox_net"],
			Field:   "sandbox_net",
			Message: "sandbox_net has no effect without a non-off sandbox mode",
		})
	}

	return Resolved{
		Effective:   eff,
		Layers:      contributing,
		Provenance:  prov,
		Diagnostics: diags,
	}, diags
}
