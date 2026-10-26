package launchconfig

import (
	"fmt"
	"strings"

	"primeradiant.com/evener/appwire"
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
// no effect in that state (evener ignores --sandbox-net without a sandbox mode), so
// ToArgs suppresses the flag and mergeLayers warns.
func sandboxModeIsOff(mode string) bool {
	m := strings.ToLower(strings.TrimSpace(mode))
	return m == "" || m == "off"
}

// isKnownSandboxMode reports whether mode is one of the sandbox modes the schema
// offers (and evener's --sandbox flag accepts), tolerating surrounding whitespace and
// case the way evener's ParseMode does. Derived from sandboxChoices so the launch UI
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
		m := layerMerge{prov: prov, layer: name}

		mergeScalar(&m, &eff.Model, l.Model, "model")
		mergeScalar(&m, &eff.FastCheapModel, l.FastCheapModel, "fast_cheap_model")
		mergeScalar(&m, &eff.Agent, l.Agent, "agent")
		mergeScalar(&m, &eff.ReasoningEffort, l.ReasoningEffort, "reasoning_effort")
		mergeScalar(&m, &eff.ContextStrategy, l.ContextStrategy, "context_strategy")
		mergeScalar(&m, &eff.ProviderIdleTimeout, l.ProviderIdleTimeout, "provider_idle_timeout")
		mergeScalar(&m, &eff.OpenAIResponsesContinuation, l.OpenAIResponsesContinuation, "openai_responses_continuation")
		mergeScalar(&m, &eff.Sandbox, l.Sandbox, "sandbox")
		mergePtr(&m, &eff.SandboxNet, l.SandboxNet, "sandbox_net")
		mergePtr(&m, &eff.MaxRounds, l.MaxRounds, "max_rounds")
		mergePtr(&m, &eff.MaxSubagentDepth, l.MaxSubagentDepth, "max_subagent_depth")
		mergePtr(&m, &eff.MaxConcurrentDelegateTurns, l.MaxConcurrentDelegateTurns, "max_concurrent_delegate_turns")
		mergePtr(&m, &eff.MaxRetainedTerminal, l.MaxRetainedTerminal, "max_retained_terminal")
		mergePtr(&m, &eff.NonInteractive, l.NonInteractive, "non_interactive")
		if l.AppReplaySize != nil {
			if name != LayerGlobal {
				diags = append(diags, Diagnostic{
					Layer: name, Field: "app_replay_size",
					Message: "app_replay_size is only honored at the global layer",
				})
			} else {
				mergePtr(&m, &eff.AppReplaySize, l.AppReplaySize, "app_replay_size")
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
			m.mark("system_prompt_mode")
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
			m.mark("system_prompt_append_mode")
		}
		mergePtr(&m, &eff.Verbose, l.Verbose, "verbose")
		mergePtr(&m, &eff.APILog, l.APILog, "api_log")
		mergeScalar(&m, &eff.TraceFile, l.TraceFile, "trace_file")
		mergeScalar(&m, &eff.CPUProfile, l.CPUProfile, "cpu_profile")
		mergeScalar(&m, &eff.ExportATIFPath, l.ExportATIFPath, "export_atif_path")
		mergeScalar(&m, &eff.ExportATIFProviderHandles, l.ExportATIFProviderHandles, "export_atif_provider_handles")

		// Lists: append in layer order.
		if len(l.SkillsDirs) > 0 {
			eff.SkillsDirs = append(eff.SkillsDirs, l.SkillsDirs...)
			m.mark("skills_dirs")
		}
		if len(l.PluginDirs) > 0 {
			eff.PluginDirs = append(eff.PluginDirs, l.PluginDirs...)
			m.mark("plugin_dirs")
		}
		if len(l.MCPConfigs) > 0 {
			eff.MCPConfigs = append(eff.MCPConfigs, l.MCPConfigs...)
			m.mark("mcp_configs")
		}
		if len(l.SystemPromptAppend) > 0 && l.SystemPromptAppendMode == "" {
			eff.SystemPromptAppendMode = "file"
			eff.SystemPromptAppendFile = l.SystemPromptAppend[0]
			eff.SystemPromptAppendText = ""
			eff.SystemPromptAppend = nil
			m.mark("system_prompt_append_mode")
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
			eff.ModelFallbacks = cloneStringSlicePtr(l.ModelFallbacks)
			m.mark("model_fallbacks")
		}
		// EnabledPlugins: higher-precedence layers REPLACE rather than append.
		// A non-nil empty value intentionally clears plugin selection.
		if l.EnabledPlugins != nil {
			eff.EnabledPlugins = cloneStringSlicePtr(l.EnabledPlugins)
			m.mark("enabled_plugins")
		}
		if len(l.MCPs) > 0 {
			for _, spec := range l.MCPs {
				if prev, ok := mcpNames[spec.Name]; ok {
					diags = append(diags, Diagnostic{
						Layer: name, Field: "mcps",
						Message: fmt.Sprintf("duplicate mcp name %q (previously seen at layer %q); evener launch-check will reject this", spec.Name, prev),
					})
				} else {
					mcpNames[spec.Name] = name
				}
				eff.MCPs = append(eff.MCPs, spec)
			}
			m.mark("mcps")
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
			m.mark("env")
		}
		if m.nonEmpty {
			contributing[name] = l
		}
	}

	// A typo'd sandbox mode merges cleanly and would only fail at spawn (evener's
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
	// effective config with net set but no (or off) mode is a silent no-op at evener
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

// layerMerge records what one layer contributed to the effective layer: which
// fields it set (provenance) and whether it set any.
type layerMerge struct {
	prov     map[string]LayerName
	layer    LayerName
	nonEmpty bool
}

// mark records that the layer set the field key.
func (m *layerMerge) mark(key string) {
	m.prov[key] = m.layer
	m.nonEmpty = true
}

// mergeScalar overrides *dst with v when the layer sets it (non-zero).
func mergeScalar[T comparable](m *layerMerge, dst *T, v T, key string) {
	var zero T
	if v != zero {
		*dst = v
		m.mark(key)
	}
}

// mergePtr overrides *dst with a copy of v when the layer sets it (non-nil), so
// an explicit zero or false still wins over a lower layer.
func mergePtr[T any](m *layerMerge, dst **T, v *T, key string) {
	if v != nil {
		*dst = appwire.ClonePointer(v)
		m.mark(key)
	}
}

func cloneStringSlicePtr(in *[]string) *[]string {
	if in == nil {
		return nil
	}
	out := append([]string{}, (*in)...)
	return &out
}
